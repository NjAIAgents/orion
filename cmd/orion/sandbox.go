package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/orion-sdlc/orion/internal/ciscaffold"
	"github.com/orion-sdlc/orion/internal/config"
	"github.com/orion-sdlc/orion/internal/registry"
	"github.com/orion-sdlc/orion/internal/ui"
	"github.com/orion-sdlc/orion/internal/workspace"
)

// ensureCI scaffolds the test script and workflow during adoption.
//
// Non-fatal throughout. A repository that cannot be given CI is still worth
// adopting; it just cannot have a merge gated on a verdict, and saying so
// once here is better than discovering it at the first pull request.
func ensureCI(dir string) {
	w := os.Stdout
	res, err := ciscaffold.Ensure(dir)
	if err != nil {
		ui.Warn(w, "could not scaffold CI: %v", err)
		return
	}
	for _, line := range ciscaffold.Describe(res) {
		if strings.HasPrefix(line, "created ") {
			ui.Ok(w, "created", "%s", strings.TrimPrefix(line, "created "))
		} else {
			fmt.Fprintf(w, "          %s\n", ui.Dim(w, line))
		}
	}
	if ciscaffold.NeedsAttention(res) {
		ui.Warn(w, "scripts/test.sh exits 1 until you fill in this repository's test command.")
		fmt.Fprintf(w, "          %s\n", ui.Dim(w,
			"Until then CI fails, which is the correct direction: a script that "+
				"exits 0 having run nothing makes every check green by construction."))
	}
	if res.ScriptCreated || res.FlowCreated {
		fmt.Fprintf(w, "          %s\n", ui.Dim(w,
			"Commit these; a workflow only runs once it is on the default branch."))
	}
}

// orion sandbox -- see and enter where the agent actually worked.
//
// Orion never runs an agent in your checkout, so the code it wrote exists
// somewhere you did not choose and cannot guess: a clone under ORION_HOME,
// and inside it one git worktree per ticket. Until this command that location
// was only ever printed once, mid-run, in a line that scrolled away.
//
// The worktrees are NOT temporary. A dry run removes its own, because a
// rehearsal that leaves a branch behind pushes the real run onto
// orion/fcia-6-2. A real run keeps everything: the branch is pushed but the
// checkout stays, so a failed or blocked job can be inspected, and a
// finished one can be diffed against what was reviewed. They accumulate
// until removed, which is a deliberate trade of disk for forensics.
func runSandbox(args []string) {
	w := os.Stdout
	home := workspace.Home()

	var target string
	for _, a := range args {
		if !strings.HasPrefix(a, "-") {
			target = strings.ToUpper(a)
			break
		}
	}

	if target == "PRUNE" {
		pruneSandboxes(w, home, hasFlag(args, "--dry-run"))
		return
	}
	if target == "" {
		listSandboxes(w, home)
		return
	}

	job, _, err := findJob(home, target)
	exitOn(err)
	// ListWorktrees reads git, which knows the path and the branch but has
	// never heard of a ticket. Carry the key the user typed, so the hints
	// this prints are commands that actually run.
	job.Key = target

	switch {
	case hasFlag(args, "--path"):
		// Nothing but the path, so it composes: cd "$(orion sandbox FCIA-6 --path)"
		fmt.Fprintln(w, job.Path)
	case hasFlag(args, "--code"):
		openInEditor(w, job.Path)
	case hasFlag(args, "--shell"):
		spawnShell(w, job.Path)
	default:
		describeJob(w, job)
	}
}

// agentDirt reports uncommitted work, ignoring Orion's own runtime directory.
//
// Every job worktree contains a .orion/ that Orion itself writes -- state,
// logs, breaker counters -- and git sees it as untracked. Reporting that as
// "uncommitted changes" makes every finished worktree look like it is
// holding unpushed work, which trains the reader to ignore the warning by
// the second time they see it. The warning is worth keeping only if it fires
// on things a person wrote.
//
// workspace.Dirty stays deliberately blunter: it guards DELETION, where
// counting Orion's own files as reasons to refuse is the safe direction.
func agentDirt(path string) (bool, []string) {
	out, err := gitIn(path, "status", "--porcelain")
	if err != nil {
		return true, []string{strings.TrimSpace(out)}
	}
	var lines []string
	for _, l := range strings.Split(strings.TrimSpace(out), "\n") {
		l = strings.TrimSpace(l)
		if l == "" {
			continue
		}
		// Status lines are "XY path"; the path is what identifies the file.
		fields := strings.Fields(l)
		if len(fields) >= 2 && strings.HasPrefix(fields[len(fields)-1], ".orion/") {
			continue
		}
		lines = append(lines, l)
	}
	return len(lines) > 0, lines
}

// findJob locates the worktree for a ticket key.
func findJob(home, key string) (workspace.Job, *workspace.Workspace, error) {
	var zero workspace.Job
	entry, err := registry.Lookup(home, key)
	if err != nil {
		return zero, nil, err
	}
	ws, err := workspace.Open(entry.Workspace)
	if err != nil {
		return zero, nil, err
	}
	jobs, err := workspace.ListWorktrees(ws)
	if err != nil {
		return zero, nil, err
	}
	want := strings.ToLower(key)
	for _, j := range jobs {
		if strings.Contains(strings.ToLower(j.Branch), want) ||
			strings.Contains(strings.ToLower(filepath.Base(j.Path)), want) {
			return j, ws, nil
		}
	}
	return zero, ws, fmt.Errorf("no sandbox for %s.\n"+
		"  It has not been worked yet, or its worktree was removed.\n"+
		"  See what exists: orion sandbox", key)
}

func listSandboxes(w io.Writer, home string) {
	f, err := registry.Load(home)
	exitOn(err)

	keys := f.Keys()
	if len(keys) == 0 {
		fmt.Fprintln(w, "no projects are bound yet. Run orion init inside a repository.")
		return
	}

	found := 0
	for _, key := range keys {
		entry, err := registry.Lookup(home, key)
		if err != nil {
			continue
		}
		ws, err := workspace.Open(entry.Workspace)
		if err != nil {
			ui.Warn(w, "%s: sandbox %s is unreadable: %v", key, entry.Workspace, err)
			continue
		}
		fmt.Fprintf(w, "\n%s  %s\n", ui.Label(w, key, ""), ui.Dim(w, ws.RepoDir()))

		jobs, err := workspace.ListWorktrees(ws)
		if err != nil {
			ui.Warn(w, "  could not list worktrees: %v", err)
			continue
		}
		if len(jobs) == 0 {
			fmt.Fprintf(w, "  %s\n", ui.Dim(w, "no job worktrees; nothing has been worked here yet"))
			continue
		}
		for _, j := range jobs {
			found++
			state := "clean"
			if dirty, lines := agentDirt(j.Path); dirty {
				// Worth flagging rather than hiding: uncommitted changes in a
				// finished job's worktree are work that was never pushed and
				// is in no pull request.
				state = fmt.Sprintf("UNCOMMITTED: %d file(s)", len(lines))
			}
			fmt.Fprintf(w, "  %-24s %s\n", j.Branch, ui.Dim(w, state))
			fmt.Fprintf(w, "  %s\n", ui.Dim(w, "  "+j.Path))
		}
	}
	if found > 0 {
		fmt.Fprintf(w, "\n%s\n", ui.Dim(w,
			"open one:  orion sandbox <KEY> --code    enter it:  cd \"$(orion sandbox <KEY> --path)\""))
	}
}

// pruneSandboxes removes the worktrees that have stopped being evidence.
//
// The retention rule is one question: could anyone still need to look at
// this? A merged branch is fully reachable from the work branch, so the
// worktree holds nothing the repository does not, and the only thing it
// still costs is disk and a confusing entry in `orion sandbox`. A branch
// that is NOT merged is the opposite -- a blocked run, a failed one, or a
// pull request nobody approved -- and that checkout is the fastest way to
// see what the agent actually did.
//
// So: merged and clean goes, everything else stays, and every decision is
// printed. Nothing here is silent, because a cleanup that quietly removed
// the one worktree someone wanted is worse than a directory that grows.
func pruneSandboxes(w io.Writer, home string, dryRun bool) {
	f, err := registry.Load(home)
	exitOn(err)

	removed, kept := 0, 0
	for _, key := range f.Keys() {
		entry, err := registry.Lookup(home, key)
		if err != nil {
			continue
		}
		ws, err := workspace.Open(entry.Workspace)
		if err != nil {
			continue
		}
		base := config.Load(ws.RepoDir()).VCS.WorkBranch
		jobs, err := workspace.ListWorktrees(ws)
		if err != nil || len(jobs) == 0 {
			continue
		}
		for _, j := range jobs {
			if dirty, lines := agentDirt(j.Path); dirty {
				ui.Warn(w, "keeping %s: %d uncommitted file(s), in no pull request", j.Branch, len(lines))
				kept++
				continue
			}
			if !mergedInto(j.Path, j.Branch, base) {
				ui.Ok(w, "keeping", "%s is not merged into %s yet", j.Branch, base)
				kept++
				continue
			}
			if dryRun {
				ui.Ok(w, "would", "remove %s (merged into %s)", j.Branch, base)
				removed++
				continue
			}
			if err := workspace.RemoveWorktree(ws, j.Path, false); err != nil {
				ui.Warn(w, "could not remove %s: %v", j.Branch, err)
				kept++
				continue
			}
			// Drop the local branch too. It is merged, so every commit on it
			// is reachable from the base and nothing is lost -- and leaving
			// it means the next ticket of the same number lands on
			// orion/fcia-6-2, a name that claims a second attempt.
			_, _ = gitIn(ws.RepoDir(), "branch", "-d", j.Branch)
			ui.Ok(w, "removed", "%s (merged into %s)", j.Branch, base)
			removed++
		}
	}
	if removed == 0 && kept == 0 {
		fmt.Fprintln(w, "nothing to prune; no job worktrees exist.")
		return
	}
	fmt.Fprintf(w, "\n%s\n", ui.Dim(w,
		fmt.Sprintf("%d removed, %d kept. Keeping means the work is unmerged or uncommitted.", removed, kept)))
}

// mergedInto reports whether a branch is fully contained in the base.
//
// Checks the REMOTE base first. The merge happens on GitHub, so the local
// develop in a sandbox clone can be days behind and would report a merged
// branch as unmerged -- refusing to clean up exactly the worktrees that
// should go.
func mergedInto(dir, branch, base string) bool {
	for _, ref := range []string{"origin/" + base, base} {
		if _, err := gitIn(dir, "merge-base", "--is-ancestor", branch, ref); err == nil {
			return true
		}
	}
	return false
}

func describeJob(w io.Writer, job workspace.Job) {
	ui.Ok(w, "branch", "%s", job.Branch)
	fmt.Fprintf(w, "          %s\n", ui.Dim(w, job.Path))

	if dirty, lines := agentDirt(job.Path); dirty {
		ui.Warn(w, "uncommitted changes; this work is in no pull request")
		for _, line := range lines {
			fmt.Fprintf(w, "          %s\n", ui.Dim(w, line))
		}
	} else {
		ui.Ok(w, "clean", "everything the agent produced is committed")
	}

	if out, err := gitIn(job.Path, "log", "--oneline", "-5"); err == nil && strings.TrimSpace(out) != "" {
		fmt.Fprintln(w, "\n  recent commits")
		for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
			fmt.Fprintf(w, "    %s\n", line)
		}
	}
	fmt.Fprintf(w, "\n%s\n", ui.Dim(w, "  open:  orion sandbox "+job.Key+" --code"))
}

func gitIn(dir string, args ...string) (string, error) {
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	return string(out), err
}

// openInEditor launches VS Code, or says plainly that it cannot.
//
// Checks for the launcher first rather than running it and interpreting the
// failure: `code` is a shell shim that VS Code installs separately from the
// application, so "not installed" and "installed without the shim" are
// different problems with different fixes, and only the second is common.
func openInEditor(w io.Writer, path string) {
	for _, bin := range []string{"code", "cursor", "codium"} {
		if p, err := exec.LookPath(bin); err == nil {
			cmd := exec.Command(p, path)
			if err := cmd.Start(); err != nil {
				exitOn(fmt.Errorf("starting %s: %w", bin, err))
			}
			ui.Ok(w, "opened", "%s in %s", filepath.Base(path), bin)
			return
		}
	}
	ui.Warn(w, "no VS Code launcher found on PATH.")
	fmt.Fprintf(w, "  In VS Code: Command Palette, \"Shell Command: Install 'code' command in PATH\".\n"+
		"  Or open it yourself:\n    %s\n", path)
}

// spawnShell drops the user into the worktree with their own shell.
func spawnShell(w io.Writer, path string) {
	sh := os.Getenv("SHELL")
	if sh == "" {
		sh = "/bin/sh"
	}
	ui.Ok(w, "entering", "%s  (exit to return)", path)
	cmd := exec.Command(sh)
	cmd.Dir = path
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	// A non-zero exit here is the user's shell exiting, not a failure of
	// Orion, so it is reported as neither.
	_ = cmd.Run()
}
