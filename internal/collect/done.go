package collect

// The done-triage gate (OR-244).
//
//	checks go green -> read the run against the diff -> DONE, and a person is
//	                   asked to approve; NOT DONE, and the ticket is handed
//	                   back with the evidence
//
// It sits between the CI verdict and the approval request because that is the
// only place it is worth anything: after this point a person is being asked to
// say yes, and the whole failure it exists to catch is a run that arrives at
// that moment looking finished. Asking afterwards would be reviewing a merge.
//
// It NEVER merges, approves, or edits the change -- everything below either
// reads, or moves a label. NOT DONE hands the ticket back the way a red build
// does (out of ci-wait, marked orion-failed, branch kept) rather than holding
// the queue open: a gate that stalls is a gate somebody switches off, and
// nothing here has the authority to decide what happens next.
//
// Run ONCE PER HEAD COMMIT. This pass runs a test suite and may run a model,
// and `orion collect` is a poll -- so without a record of what has already
// been triaged, a ticket waiting a day for approval would pay for both on
// every tick. Keyed on the commit rather than a bare flag, exactly as
// Conflicts is: a branch somebody has pushed to is a different change and is
// triaged afresh.

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/orion-sdlc/orion/internal/actors"
	"github.com/orion-sdlc/orion/internal/config"
	"github.com/orion-sdlc/orion/internal/done"
	"github.com/orion-sdlc/orion/internal/events"
	"github.com/orion-sdlc/orion/internal/notify"
	"github.com/orion-sdlc/orion/internal/supervisor"
	"github.com/orion-sdlc/orion/internal/tracker"
	"github.com/orion-sdlc/orion/internal/ui"
	"github.com/orion-sdlc/orion/internal/workspace"
)

// rerunTimeout bounds the -count=2 re-run of the branch's new tests. A var,
// not a const, so a test can shrink it.
//
// The same ceiling internal/work gives the red-before-green check, for the
// same reason: it runs a repository's own suite, and a suite that has not
// finished in ten minutes is not one this pass can wait on inside a watcher
// tick that has every other ticket to reconcile.
var rerunTimeout = 10 * time.Minute

// maxPatch is how much diff text the model is given. Beyond this the patch is
// truncated and SAID to be truncated, because a criterion the model cannot
// find because it was cut off is missing evidence, not missing work.
const maxPatch = 60_000

// triageDone answers "is this genuinely done?" and reports whether the ticket
// may go on to be offered for approval.
func triageDone(res Result, key string, pr PR, cfg config.Config, branch string,
	opts Options, deps Deps, ws *workspace.Workspace, log *events.Log,
	w io.Writer) (bool, Result) {

	// Already answered for this exact commit. Nothing about looking again
	// makes the verdict different, and looking again costs a suite run.
	if pr.Head != "" && loadRequests(ws.Dir).Triaged[key] == pr.Head {
		return true, res
	}
	if opts.DryRun {
		ui.Ok(w, "would", "%s: triage the finished run before asking for approval", key)
		return true, res
	}

	ui.Say(w, key, events.ActorDoneTriage, ui.VerbWorking,
		"reading the run against the diff before anyone is asked to approve it")

	ev := gatherEvidence(key, pr, cfg, branch, deps, ws)
	var ask done.Asker
	if deps.Judge != nil {
		ask = func(e done.Evidence) (string, error) {
			return deps.Judge(ws, key, supervisorDonePrompt(e))
		}
	}
	v := done.Triage(ev, ask)

	if v.Done {
		log.Emit(events.Event{Kind: events.KindNote, Actor: events.ActorDoneTriage,
			Msg: "triaged the finished run: done. " + v.Note})
		ui.Say(w, key, events.ActorDoneTriage, ui.VerbOK,
			"this looks genuinely done; going on to ask for approval")
		// The third question, on the evidence just gathered (OR-158). Done
		// and QA both read the change against the TICKET; this reads it
		// against the confirmed PLAN, and a change can satisfy both of them
		// and still not be what was agreed. It reports and returns nothing:
		// whatever it finds, the ticket goes on to approval exactly as it
		// would have.
		reviewConformance(key, pr, ev.Diff, cfg, branch, opts, deps, ws, log, w)
		if pr.Head != "" {
			if err := markTriaged(ws.Dir, key, pr.Head); err != nil {
				// Not fatal: the cost of losing the record is one repeated
				// triage on the next tick, not a wrong verdict.
				ui.Warn(w, "%s: could not record the triage verdict (%v); "+
					"the next pass will triage again", key, err)
			}
		}
		return true, res
	}
	return false, notDone(res, key, pr, v, cfg, opts, deps, ws, log, w)
}

// supervisorDonePrompt builds the intent question from the gathered evidence.
//
// Absent input is stated as absent rather than left blank. An agent handed an
// empty "WHAT IT ASKED FOR" section will invent a plausible requirement and
// then judge the diff against it, which is a hand-back nobody can trace to
// anything.
func supervisorDonePrompt(ev done.Evidence) string {
	criteria := strings.TrimSpace(ev.Criteria)
	if criteria == "" {
		criteria = "The ticket's description could not be read. You therefore have " +
			"nothing to check the diff against: answer " + done.ReplyDone + "."
	}
	stat := strings.TrimSpace(ev.Diff.Stat)
	if stat == "" {
		stat = "(the file summary could not be read)"
	}
	patch := strings.TrimSpace(ev.Diff.Patch)
	if patch == "" {
		patch = "(the diff could not be read; see the note above)"
	}
	return supervisor.DonePrompt(ev.Key, ev.Summary, criteria, stat, patch, ev.Diff.Truncated)
}

// notDone hands the ticket back, with the evidence.
//
// Marked orion-failed rather than re-queued, for the reason failing() gives:
// the branch already exists with commits on it, so a re-queue would start a
// second agent from develop and produce a competing branch for the same
// ticket. Taken out of ci-wait so nothing polls it forever -- the point is
// that this does not block anything, only that it stops pretending.
func notDone(res Result, key string, pr PR, v done.Verdict, cfg config.Config,
	opts Options, deps Deps, ws *workspace.Workspace, log *events.Log,
	w io.Writer) Result {

	report := v.Report()

	if err := deps.Jira.SetLabels(key, []string{tracker.LabelFailed},
		[]string{tracker.LabelCIWait}); err != nil {
		res.Err = err
		ui.Warn(w, "%s: %s, but the ticket could not be relabelled: %v", key, v.Summary(), err)
		return res
	}
	_ = deps.Jira.Comment(key, actors.Comment(events.ActorDoneTriage,
		"The checks on "+pr.URL+" are green, and this change is NOT DONE.\n\n"+report+
			"\nGreen means the build compiles and the existing tests pass. It is not "+
			"evidence that this ticket was finished, and nobody has been asked to "+
			"approve it.\n\nThe branch is kept. Nothing was merged, nothing was "+
			"changed, and no approval was requested -- this pass only reports."))

	log.Emit(events.Event{Kind: events.KindBlocked, Actor: events.ActorDoneTriage,
		Model: actors.Model(events.ActorDoneTriage),
		Msg:   "triaged the finished run: " + v.Summary()})
	res.Changed = true

	tell(w, log, notify.Event{
		Key: key, Channel: channelOf(ws), Level: notify.Blocked, Workspace: ws.ID,
		Actor: events.ActorDoneTriage,
		Title: key + ": green, and not done",
		Body: mention(cfg) + strings.Join([]string{
			"*The checks pass and this change is not finished.* Nobody has been asked",
			"to approve it.",
			"",
			quote(report),
			"",
			"• pull request  " + link(pr.URL, "open it"),
			"",
			"_Nothing was merged and nothing was changed. The branch is kept._",
		}, "\n"),
	})

	ui.Fail(w, "%s: the checks pass and this is not done -- %s", key, v.Summary())
	for _, f := range v.Findings {
		ui.Say(w, key, events.ActorDoneTriage, ui.VerbFail, "%s", f.What)
	}
	return res
}

// gatherEvidence reads everything the verdict rests on: the run's events, the
// diff, and what the branch's own new tests do when run twice.
//
// Every step degrades to a stated reason rather than an error. A verdict is
// only as good as its evidence, and evidence that could not be read has to say
// so -- an unreadable check is not a pass, but it is also not grounds to hand
// finished work back.
func gatherEvidence(key string, pr PR, cfg config.Config, branch string,
	deps Deps, ws *workspace.Workspace) done.Evidence {

	ev := done.Evidence{Key: key}
	if issue, ok := issueFor(deps.Jira, key); ok {
		ev.Summary, ev.Criteria = issue.Summary, issue.Description
	}
	if evs, err := events.Read(events.Path(ws.Dir)); err == nil {
		ev.Events = done.LastQARun(evs, key)
	}

	dir := worktreeOrRepo(ws, branch)
	base, named := baseOf(pr, cfg)
	if !named {
		ev.Diff.Unreadable = "the pull request does not name a base branch"
		return ev
	}
	ev.Diff = readDiff(dir, base, branch)
	ev.Diff.Stranded = strandedTests(ws, branch, ev.Diff.Files)
	ev.Rerun = rerunAtCountTwo(dir, base, branch, ev.Diff)
	return ev
}

// issueFor fetches the ticket, for its acceptance criteria. Best effort: a
// tracker that will not answer costs the intent check its input, and the
// mechanical checks do not read it at all.
func issueFor(j TrackerAPI, key string) (tracker.Issue, bool) {
	if j == nil {
		return tracker.Issue{}, false
	}
	issues, err := j.Search(tracker.JQLEq("key", key), 1)
	if err != nil || len(issues) == 0 {
		return tracker.Issue{}, false
	}
	return issues[0], true
}

// readDiff reads what the branch carries against its base.
//
// Three dots, not two: `base...branch` is the diff since the two diverged,
// which is what the pull request shows and what a reviewer will read. Two dots
// would fold in everything that landed on the base since the branch was cut,
// and the model would be asked whether the ticket's criteria appear in three
// other people's work.
func readDiff(dir, base, branch string) done.Diff {
	var d done.Diff
	if dir == "" {
		return done.Diff{Unreadable: "no checkout to read the diff from"}
	}
	// Fetch first, for the reason upToDate does: the sandbox clone is shared
	// and may be behind, and a diff against a stale remote-tracking ref
	// describes a branch nobody is merging.
	if err := gitQuiet(dir, "fetch", "--quiet", "origin"); err != nil {
		return done.Diff{Unreadable: "could not fetch the remote: " + err.Error()}
	}
	spec := "origin/" + base + "...origin/" + branch
	names, err := gitOut(dir, "diff", "--name-only", "--diff-filter=ACMR", spec)
	if err != nil {
		return done.Diff{Unreadable: "could not read the diff: " + err.Error()}
	}
	for _, line := range strings.Split(names, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			d.Files = append(d.Files, line)
		}
	}
	d.Stat, _ = gitOut(dir, "diff", "--stat", spec)
	patch, err := gitOut(dir, "diff", spec)
	if err != nil {
		d.Unreadable = "could not read the diff text: " + err.Error()
		return d
	}
	if len(patch) > maxPatch {
		patch, d.Truncated = patch[:maxPatch], true
	}
	d.Patch = patch
	return d
}

// strandedTests lists test files sitting in this ticket's own worktree that
// the branch does not carry.
//
// This is OR-217, mechanically: QA wrote the test that caught the off-by-one,
// it was never committed, CI tested a commit without it, and the pull request
// went green on evidence that did not exist. internal/work now commits what
// the QA stage leaves behind (OR-234) -- so anything still here is a commit
// that FAILED, which is the case that produced the fault in the first place.
//
// Only a real job worktree is read. worktreeOrRepo falls back to the shared
// clone, whose dirty files belong to whatever else is using it, and reporting
// those against this ticket would be a hand-back on somebody else's residue.
func strandedTests(ws *workspace.Workspace, branch string, inDiff []string) []string {
	dir := jobTree(ws, branch)
	if dir == "" {
		return nil
	}
	out, err := gitOut(dir, "status", "--porcelain", "--untracked-files=all")
	if err != nil {
		return nil
	}
	carried := make(map[string]bool, len(inDiff))
	for _, f := range inDiff {
		carried[f] = true
	}
	var stranded []string
	for _, line := range strings.Split(out, "\n") {
		// Porcelain v1: two status columns, a space, then the path.
		if len(line) < 4 {
			continue
		}
		path := strings.TrimSpace(line[3:])
		// A rename reads as "old -> new"; the new name is the one on disk.
		if i := strings.LastIndex(path, " -> "); i >= 0 {
			path = path[i+4:]
		}
		path = strings.Trim(path, `"`)
		if path == "" || carried[path] || !isTestPath(path) {
			continue
		}
		stranded = append(stranded, path+" (in the worktree, not in the diff)")
	}
	return stranded
}

// rerunAtCountTwo runs the branch's new or changed tests a second time.
//
// This is OR-229: its own test passed at -count=1 and failed at -count=2,
// because under concurrency the fan paired answers with the wrong questions
// and one run happened to schedule the goroutines the way the assertions
// expected. One green run of a concurrent test is a sample, not a result, and
// the flag that turns it into one costs nothing.
//
// GO ONLY, and it says so when it declines. -count is a `go test` flag, and
// internal/work/redgreen.go already records why this codebase does not invent
// a per-framework test selector: Orion's one contract for running a
// repository's suite is scripts/test.sh, which has no way to ask for a single
// test twice. Naming a convention this cannot actually drive would report on
// nothing, so the other stacks get a stated skip rather than a false pass.
//
// AN EPHEMERAL CHECKOUT OF origin/<branch>, never the job worktree. The
// worktree may hold exactly the uncommitted work strandedTests is looking for,
// and running the suite over it would test something the pull request does not
// carry -- which is the fault this whole pass exists to catch, committed by
// the pass itself.
func rerunAtCountTwo(dir, base, branch string, d done.Diff) done.Rerun {
	if d.Unreadable != "" {
		return done.Rerun{Skipped: "the diff could not be read: " + d.Unreadable}
	}
	pkgs := goTestPackages(d.Files)
	if len(pkgs) == 0 {
		return done.Rerun{Skipped: "this branch adds or changes no Go test file, " +
			"and -count=2 is a `go test` flag"}
	}
	if _, err := exec.LookPath("go"); err != nil {
		return done.Rerun{Skipped: "no Go toolchain on PATH to re-run them with"}
	}

	tmp, err := os.MkdirTemp("", "orion-recount-")
	if err != nil {
		return done.Rerun{Skipped: "could not prepare a checkout of the branch: " + err.Error()}
	}
	defer os.RemoveAll(tmp)
	if err := gitQuiet(dir, "worktree", "add", "--detach", "--quiet", tmp,
		"origin/"+branch); err != nil {
		return done.Rerun{Skipped: "could not check out the branch: " + err.Error()}
	}
	defer func() { _ = gitQuiet(dir, "worktree", "remove", "--force", tmp) }()

	ctx, cancel := context.WithTimeout(context.Background(), rerunTimeout)
	defer cancel()
	args := append([]string{"test", "-count=2"}, pkgs...)
	cmd := exec.CommandContext(ctx, "go", args...)
	cmd.Dir = tmp
	out, runErr := cmd.CombinedOutput()

	switch {
	case runErr == nil:
		return done.Rerun{Packages: pkgs}
	case ctx.Err() != nil:
		// A timeout is not a failing test. It may BE the defect -- a
		// deadlock that only appears on the second run -- but this pass
		// cannot tell that from a slow suite, and guessing would hand back
		// work on a wall clock.
		return done.Rerun{Packages: pkgs,
			Skipped: "the re-run did not finish within " + rerunTimeout.String()}
	default:
		if _, isExit := runErr.(*exec.ExitError); !isExit {
			return done.Rerun{Packages: pkgs,
				Skipped: "the re-run could not be started: " + runErr.Error()}
		}
		return done.Rerun{Packages: pkgs, Failed: true, Output: tailOf(string(out), 40)}
	}
}

// goTestPackages maps the changed Go test files to the package paths that
// hold them, deduplicated and in a stable order.
func goTestPackages(files []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, f := range files {
		if !strings.HasSuffix(f, "_test.go") {
			continue
		}
		dir := filepath.ToSlash(filepath.Dir(f))
		if dir == "." {
			dir = "./"
		}
		p := "./" + strings.TrimPrefix(dir, "./")
		if seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, p)
	}
	return out
}

// isTestPath recognises the same test-naming conventions
// internal/work/redgreen.go does, and for the same reason: they are the
// stacks Orion can actually scaffold and run a suite for.
func isTestPath(path string) bool {
	base := filepath.Base(path)
	switch {
	case strings.HasSuffix(base, "_test.go"):
		return true
	case strings.HasPrefix(base, "test_") && strings.HasSuffix(base, ".py"):
		return true
	case strings.HasSuffix(base, "_test.py"):
		return true
	case strings.Contains(filepath.ToSlash(path), "/__tests__/"):
		return true
	}
	for _, ext := range []string{".js", ".jsx", ".ts", ".tsx"} {
		if strings.HasSuffix(base, ".test"+ext) || strings.HasSuffix(base, ".spec"+ext) {
			return true
		}
	}
	return false
}

// tailOf keeps the last n lines. A failing suite prints its summary last, so
// the end is the part that says what broke.
func tailOf(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

// markTriaged records that this exact commit has already been triaged done.
func markTriaged(wsDir, key, head string) error {
	f := loadRequests(wsDir)
	f.Triaged[key] = head
	return writeRequests(wsDir, f)
}

// clearTriaged forgets a ticket's triage record. Called when it merges, so a
// ticket reopened later is triaged afresh rather than on a commit that is no
// longer what anyone would be approving.
func clearTriaged(wsDir, key string) error {
	f := loadRequests(wsDir)
	if _, ok := f.Triaged[key]; !ok {
		return nil
	}
	delete(f.Triaged, key)
	return writeRequests(wsDir, f)
}

func gitOut(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return strings.TrimRight(string(out), "\n"), nil
}
