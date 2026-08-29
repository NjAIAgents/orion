package workspace

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/orion-sdlc/orion/internal/provision"
)

// Binding an EXISTING repository to a sandbox.
//
// The problem this solves: `orion new` scaffolds a fresh project inside a
// workspace it owns, but an adopted repo already exists somewhere the user
// works in daily. The supervisor can only run inside a workspace, so an
// adopted repo had no run path at all.
//
// The answer is not to run in the user's working copy. An agent that edits
// the directory you have open in an editor can destroy uncommitted work,
// and there is no undo. So Orion keeps its own clone under ORION_HOME and
// treats your checkout as read-only: it clones from the REMOTE, works in
// the clone, pushes a branch, and afterwards offers to fast-forward your
// copy. Your working tree is never written to.

// BindOptions describes an existing repository to sandbox.
type BindOptions struct {
	// SourcePath is the user's working copy. Used to discover the remote and
	// to refresh afterwards; never written to.
	SourcePath string
	// Remote is the URL to clone. Discovered from SourcePath when empty.
	Remote string
	// WorkBranch and DefaultBranch come from orion.json.
	DefaultBranch string
	WorkBranch    string
	// Force skips the preflight that refuses a dirty or unpushed source.
	Force bool
}

// Preflight reports why the source is not safe to clone from, or nil.
//
// A clone comes from the REMOTE, so anything uncommitted or unpushed in the
// working copy is simply absent from the sandbox. The agent would then solve
// the problem against a base that does not match what the user is looking
// at, and the mismatch surfaces as a confusing merge conflict much later,
// long after the run has been paid for.
func Preflight(sourcePath string) []string {
	var problems []string
	g := func(args ...string) (string, error) {
		out, err := exec.Command("git", append([]string{"-C", sourcePath}, args...)...).CombinedOutput()
		return strings.TrimSpace(string(out)), err
	}
	if _, err := g("rev-parse", "--git-dir"); err != nil {
		return []string{sourcePath + " is not a git repository"}
	}
	if out, err := g("status", "--porcelain"); err == nil && out != "" {
		n := len(strings.Split(out, "\n"))
		problems = append(problems, fmt.Sprintf(
			"%d uncommitted change(s); the sandbox clones from the remote and will not see them:\n%s",
			n, indentBlock(out, 12)))
	}
	branch, _ := g("branch", "--show-current")
	if branch != "" {
		if out, err := g("log", "--oneline", "origin/"+branch+".."+branch); err == nil && out != "" {
			problems = append(problems, fmt.Sprintf(
				"unpushed commit(s) on %s; the sandbox will start without them:\n%s",
				branch, indentBlock(out, 12)))
		}
	}
	return problems
}

func indentBlock(s string, n int) string {
	pad := strings.Repeat(" ", n)
	return pad + strings.ReplaceAll(s, "\n", "\n"+pad)
}

// RemoteOf reads origin's URL from a working copy.
func RemoteOf(sourcePath string) (string, error) {
	out, err := exec.Command("git", "-C", sourcePath, "remote", "get-url", "origin").Output()
	if err != nil {
		return "", fmt.Errorf("no origin remote in %s: Orion clones from the remote, "+
			"so the repository needs one before it can be sandboxed", sourcePath)
	}
	return strings.TrimSpace(string(out)), nil
}

// Bind creates a sandbox workspace for an existing repository.
func Bind(opts BindOptions) (*Workspace, error) {
	src, err := filepath.Abs(opts.SourcePath)
	if err != nil {
		return nil, err
	}
	remote := strings.TrimSpace(opts.Remote)
	if remote == "" {
		if remote, err = RemoteOf(src); err != nil {
			return nil, err
		}
	}
	if !opts.Force {
		if problems := Preflight(src); len(problems) > 0 {
			return nil, fmt.Errorf("the working copy is not in a state worth cloning:\n\n  %s\n\n"+
				"  Commit and push, or re-run with --force to sandbox the remote as it stands",
				strings.Join(problems, "\n  "))
		}
	}

	slug := Slugify(filepath.Base(src))
	id := slug + "-" + shortID()
	dir := filepath.Join(projectsDir(), id)
	if _, err := os.Stat(dir); err == nil {
		return nil, fmt.Errorf("workspace %s already exists", id)
	}
	if _, err := EnsureHome(); err != nil {
		return nil, err
	}
	for _, d := range []string{
		filepath.Join(dir, "worktrees"),
		filepath.Join(dir, ".orion", "logs"),
		filepath.Join(dir, ".orion", "state"),
	} {
		if err := os.MkdirAll(d, HomeDirMode); err != nil {
			return nil, fmt.Errorf("provisioning %s: %w", d, err)
		}
	}

	ws := &Workspace{
		ID:  id,
		Dir: dir,
		Task: Task{
			ID: id, Idea: "adopted: " + filepath.Base(src), Slug: slug,
			CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
			Stage: "adopted", Status: "provisioned",
			FromRepo: remote, SourcePath: src, Remote: remote,
		},
	}

	// A FULL clone, not --depth 1.
	//
	// `orion new` clones shallow because it only needs a working tree from a
	// template. Here the agent must branch from the work branch, diff against
	// it, and open a pull request -- none of which a shallow clone can do.
	// `git log develop..HEAD` is empty in a shallow clone, so the PR body
	// would describe no changes.
	if out, err := exec.Command("git", "clone", remote, ws.RepoDir()).CombinedOutput(); err != nil {
		_ = os.RemoveAll(dir)
		return nil, fmt.Errorf("cloning %s: %w\n%s", remote, err, out)
	}

	// Establish the branch model in the clone. `orion new`'s clone path
	// skipped this, so a cloned workspace never got a work branch and the
	// first commit had nowhere correct to land.
	mainB, workB := opts.DefaultBranch, opts.WorkBranch
	if mainB == "" {
		mainB = "main"
	}
	if workB == "" {
		workB = "develop"
	}
	// Prefer the branch that already exists on the remote.
	//
	// git clone materialises only the default branch locally; develop exists
	// solely as remotes/origin/develop. InitBranches would then CREATE a
	// local develop from main -- so the sandbox starts from main's tip,
	// silently missing every commit made on develop, and its develop has
	// already diverged from the remote before any work begins. A pull request
	// from that base is unreviewable.
	//
	// Observed: the sandbox came up one commit behind and without the
	// orion.json that had just been pushed to develop.
	tracked, err := checkoutTracking(ws.RepoDir(), workB)
	if err != nil {
		return nil, err
	}
	if tracked {
		ws.Task.Branches = []string{mainB, workB}
	} else {
		created, err := provision.InitBranches(ws.RepoDir(), mainB, workB)
		if err != nil {
			return nil, fmt.Errorf("establishing the branch model in the sandbox: %w", err)
		}
		ws.Task.Branches = created
	}

	if err := writeSettings(ws); err != nil {
		return nil, err
	}
	if err := ws.SaveTask(); err != nil {
		return nil, err
	}
	return ws, nil
}

// Refresh fast-forwards the user's working copy after Orion has pushed.
//
// Fetch always, fast-forward only, and never when the tree is dirty. A tool
// that rebases or merges into a checkout someone has open in an editor can
// destroy work with no undo; leaving a command to run is strictly better
// than guessing. Reports what it did, and what it declined to do.
func Refresh(sourcePath, branch string) (string, error) {
	g := func(args ...string) (string, error) {
		out, err := exec.Command("git", append([]string{"-C", sourcePath}, args...)...).CombinedOutput()
		return strings.TrimSpace(string(out)), err
	}
	if out, err := g("fetch", "--prune", "origin"); err != nil {
		return "", fmt.Errorf("fetching in %s: %w\n%s", sourcePath, err, out)
	}
	cur, _ := g("branch", "--show-current")
	if branch == "" {
		branch = cur
	}
	if cur != branch {
		return fmt.Sprintf("fetched; %s is checked out, so %s was not fast-forwarded.\n"+
			"  Update it with: git -C %s checkout %s && git pull --ff-only",
			cur, branch, sourcePath, branch), nil
	}
	if out, _ := g("status", "--porcelain"); out != "" {
		return "fetched; not fast-forwarding because the working tree has uncommitted changes.\n" +
			"  Commit or stash, then: git pull --ff-only", nil
	}
	if out, err := g("merge", "--ff-only", "origin/"+branch); err != nil {
		return fmt.Sprintf("fetched; %s could not be fast-forwarded (it has diverged).\n"+
			"  Reconcile it yourself: %s", branch, firstLineOf(out)), nil
	}
	return "fetched and fast-forwarded " + branch, nil
}

// SyncSandbox fast-forwards the sandbox clone's own checked-out branch.
//
// Orion reads a project's POLICY -- orion.json -- from the sandbox clone,
// not from the user's working copy, because policy must be the committed
// version rather than whatever is on one machine. The consequence is that a
// sandbox created days ago serves a days-old config: turning on
// require_approval, pushing it, and watching Orion carry on as though
// nothing had changed. The setting was correct, committed and pushed; the
// thing reading it was behind.
//
// AddWorktree already fetches and branches from origin/<base>, so agent work
// was never cut from a stale base. This closes the remaining gap, which is
// the clone's own checkout.
//
// Fast-forward only, and never over local changes. A sandbox should not have
// any -- but "should not" is not "cannot", and discarding an agent's
// uncommitted work to freshen a config file would be a spectacularly bad
// trade.
// Locked against the other git that runs in this clone. Every concurrent job
// calls this at its start, and a fetch plus a fast-forward is exactly the kind
// of ref write that a simultaneous `worktree add` loses to.
func SyncSandbox(ws *Workspace, branch string) (string, error) {
	defer LockRepo(ws)()

	repo := ws.RepoDir()
	g := func(args ...string) (string, error) {
		out, err := exec.Command("git", append([]string{"-C", repo}, args...)...).CombinedOutput()
		return strings.TrimSpace(string(out)), err
	}
	if _, err := os.Stat(repo); err != nil {
		return "", err
	}
	if out, err := g("fetch", "--prune", "origin"); err != nil {
		return "", fmt.Errorf("fetching in the sandbox: %w\n%s", err, out)
	}
	cur, _ := g("branch", "--show-current")
	if branch == "" {
		branch = cur
	}
	if cur != branch {
		return fmt.Sprintf("the sandbox has %s checked out, not %s", cur, branch), nil
	}
	if dirty, _ := g("status", "--porcelain"); dirty != "" {
		return "the sandbox has uncommitted changes; not fast-forwarding", nil
	}
	before, _ := g("rev-parse", "HEAD")
	if out, err := g("merge", "--ff-only", "origin/"+branch); err != nil {
		return fmt.Sprintf("%s has diverged from origin: %s", branch, firstLineOf(out)), nil
	}
	after, _ := g("rev-parse", "HEAD")
	if before == after {
		return "", nil // already current; nothing worth saying
	}
	return "fast-forwarded the sandbox to " + after[:min(7, len(after))], nil
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func firstLineOf(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// FindBySource returns the workspace already bound to a working copy, if any.
//
// Adoption must be idempotent: re-running `orion init` is how a repo is
// repaired, and re-cloning on every run would both waste time and orphan the
// previous sandbox along with any unmerged branch still in it.
func FindBySource(sourcePath string) *Workspace {
	abs, err := filepath.Abs(sourcePath)
	if err != nil {
		return nil
	}
	entries, err := os.ReadDir(projectsDir())
	if err != nil {
		return nil
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		ws, err := Open(e.Name())
		if err != nil {
			continue
		}
		if ws.Task.SourcePath == abs {
			return ws
		}
	}
	return nil
}

// checkoutTracking checks out a branch that exists on origin, tracking it.
// Reports false when the remote has no such branch, so the caller can create
// the branch model from scratch instead.
func checkoutTracking(repo, branch string) (bool, error) {
	if err := exec.Command("git", "-C", repo, "rev-parse", "--verify", "--quiet",
		"refs/remotes/origin/"+branch).Run(); err != nil {
		return false, nil
	}
	out, err := exec.Command("git", "-C", repo,
		"checkout", "-q", "-B", branch, "--track", "origin/"+branch).CombinedOutput()
	if err != nil {
		return false, fmt.Errorf("checking out %s from origin: %w\n%s", branch, err, out)
	}
	return true, nil
}
