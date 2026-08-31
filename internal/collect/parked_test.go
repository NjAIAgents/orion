package collect

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/orion-sdlc/orion/internal/registry"
	"github.com/orion-sdlc/orion/internal/workspace"
)

// What the operator is TOLD when a breaker parks a branch (OR-232).
//
// The situation these reproduce is OR-217's, exactly: a run stopped by a
// breaker, its worktree left with uncommitted tracked changes, and the landing
// pass then refusing to rebase it every poll. Everything Orion knew was on
// record -- which breaker, which session, what to type -- and the only surface
// the operator was watching received none of it, fifteen times over fifteen
// minutes.
//
// So these assert on the OUTPUT, not on a helper's return value. A helper that
// formats a perfect sentence nobody prints is the bug this ticket is about.

// parkedWorktree leaves a worktree in the state a tripped run leaves it: a
// modified tracked file, the breaker's session state, and the note it wrote.
//
// Written as the JSON state.Session actually produces rather than through the
// hook, for the reason internal/work's equivalent gives: what is under test is
// what COLLECT does about a trip, and if that file format changes this must
// fail rather than quietly stop recognising trips.
func parkedWorktree(t *testing.T, wtDir, session, kind, detail string) {
	t.Helper()
	dirtyTracked(t, wtDir, "half-finished\n")
	dir := filepath.Join(wtDir, ".orion", "state")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := `{"id":"` + session + `","tripped":"` + kind + `","tripped_detail":"` + detail + `"}`
	if err := os.WriteFile(filepath.Join(dir, session+".json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// dirtyTracked leaves an uncommitted change to a TRACKED file, which is the
// only kind rebaseLocal refuses -- untracked files survive a rebase, and
// refusing on a stray build artefact would disable the automation everywhere.
// The fixture repositories are built from empty commits, so the file has to be
// committed before it can be modified.
func dirtyTracked(t *testing.T, wtDir, content string) {
	t.Helper()
	path := filepath.Join(wtDir, "work.txt")
	if err := os.WriteFile(path, []byte("committed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, wtDir, "add", "work.txt")
	gitRun(t, wtDir, "commit", "--quiet", "-m", "the run's work")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// jobWorktree is where worktreeOrRepo looks for FCIA-6's tree, for the
// fixtures that build one without handing it back.
func jobWorktree(t *testing.T, home string) string {
	t.Helper()
	entry, err := registry.Lookup(home, "FCIA-6")
	if err != nil {
		t.Fatal(err)
	}
	ws, err := workspace.Open(entry.Workspace)
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(ws.Dir, "worktrees", "orion-fcia-6")
}

// The acceptance criterion, in one test: the reset command reaches the watch
// log, not only plans/BLOCKED.md.
//
// On OR-217 the operator learned `orion reset --session <id>` existed by
// reading BLOCKED.md off disk by hand, inside a hashed path under ORION_HOME.
// Everything else they saw named the symptom.
func TestABreakerParkedBranchNamesTheBreakerAndTheResetCommandInTheWatchLog(t *testing.T) {
	home, _, _, wtDir, sha := staleTicket(t)
	parkedWorktree(t, wtDir, "4b6af93d", "breaker/loop", "Bash repeated 4 times")

	_, out, _ := run(t, home, newTracker(), PR{
		Verdict: VerdictPassing, Head: sha, URL: "https://example/pr/1",
	}, Options{})

	for _, want := range []string{
		"breaker/loop",                   // which breaker
		"FCIA-6",                         // which ticket
		"orion reset --session 4b6af93d", // and the exact remedy
		"plans/BLOCKED.md",               // where the long account of it is
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the watch log never says %q; it is the only surface the operator "+
				"is looking at:\n%s", want, out)
		}
	}
}

// The cause, not only the symptom. "has uncommitted changes" is a true report
// of something downstream: it sends the reader looking for a person who left
// work in a worktree, when what happened is that a breaker stopped the run.
func TestARebaseRefusedByAParkedWorktreeSaysWhyItIsDirty(t *testing.T) {
	home, _, _, wtDir, sha := staleTicket(t)
	parkedWorktree(t, wtDir, "sess-impl", "breaker/unverified-edits", "12 edits with no passing verification")

	_, out, _ := run(t, home, newTracker(), PR{
		Verdict: VerdictPassing, Head: sha, URL: "https://example/pr/1",
	}, Options{})

	if !strings.Contains(out, "could not rebase") {
		t.Fatalf("the refused rebase was not reported at all:\n%s", out)
	}
	if !strings.Contains(out, "parked that worktree") ||
		!strings.Contains(out, "breaker/unverified-edits") {
		t.Errorf("the refusal reports the dirty tree and never the breaker that caused "+
			"it, which is the whole of OR-232:\n%s", out)
	}
}

// A dirty worktree with NO trip on record is reported exactly as before.
//
// The trip is read for what it lets Orion SAY, never for permission to act
// (internal/work/residue.go settles this). A missing flag has to mean "nothing
// to add" and never "a breaker did it", or the message invents a cause.
func TestADirtyWorktreeWithNoTripOnRecordIsReportedAsItAlwaysWas(t *testing.T) {
	home, _, _, wtDir, sha := staleTicket(t)
	dirtyTracked(t, wtDir, "by hand\n")

	_, out, _ := run(t, home, newTracker(), PR{
		Verdict: VerdictPassing, Head: sha, URL: "https://example/pr/1",
	}, Options{})

	if !strings.Contains(out, "uncommitted changes") {
		t.Fatalf("the refused rebase was not reported:\n%s", out)
	}
	if strings.Contains(out, "parked that worktree") || strings.Contains(out, "orion reset") {
		t.Errorf("a breaker was blamed for a dirty tree with no trip on record:\n%s", out)
	}
}

// Say it once, then stop. Fifteen identical lines is not fifteen times the
// information -- it is the reader learning to skip the block that was meant to
// get their attention.
//
// The FIRST reminder still comes on the poll after the hand-over: a branch that
// vanishes from the log has been forgotten as far as anybody can tell
// (TestAHandedOverBranchIsAnnouncedOnceNotOncePerPoll asserts exactly that).
// What must not happen is the third, fourth and fifteenth.
func TestTheStillBehindReminderIsRateLimitedNotEmittedEveryPoll(t *testing.T) {
	home, _, _, sha := staleTicketAtCap(t)
	parkedWorktree(t, jobWorktree(t, home), "4b6af93d", "breaker/loop", "Bash repeated 4 times")
	jira := newTracker()
	pr := PR{Verdict: VerdictPassing, Head: sha, URL: "https://example/pr/1"}

	_, first, _ := run(t, home, jira, pr, Options{})  // the hand-over
	_, second, _ := run(t, home, jira, pr, Options{}) // the one reminder
	_, third, _ := run(t, home, jira, pr, Options{})  // and no more
	_, fourth, _ := run(t, home, jira, pr, Options{})

	if strings.Contains(first, "still behind") {
		t.Errorf("the first poll announced the hand-over AND reminded of it:\n%s", first)
	}
	if !strings.Contains(second, "still behind") {
		t.Fatalf("the branch was dropped from the log entirely on the next poll:\n%s", second)
	}
	for i, out := range []string{third, fourth} {
		if strings.Contains(out, "still behind") {
			t.Errorf("poll %d repeated the reminder inside the %s window; this is the "+
				"line OR-217's operator read fifteen times:\n%s", i+3, remindEvery, out)
		}
	}
}

// And when it IS said, it carries the cause and the remedy. This is the line
// the operator actually reads over and over, so it is the one that has to
// answer the question rather than restating it.
func TestTheStillBehindReminderNamesTheBreakerAndTheResetCommand(t *testing.T) {
	home, _, _, sha := staleTicketAtCap(t)
	parkedWorktree(t, jobWorktree(t, home), "4b6af93d", "breaker/loop", "Bash repeated 4 times")
	jira := newTracker()
	pr := PR{Verdict: VerdictPassing, Head: sha, URL: "https://example/pr/1"}

	run(t, home, jira, pr, Options{})
	_, second, _ := run(t, home, jira, pr, Options{})

	for _, want := range []string{"still behind", "breaker/loop", "orion reset --session 4b6af93d"} {
		if !strings.Contains(second, want) {
			t.Errorf("the reminder does not say %q:\n%s", want, second)
		}
	}
}

// A branch that MOVES is news again, reminder clock and all. Throttling the
// reminder must not become throttling the announcement -- the person who
// pushed deserves to be told it is still behind.
func TestAMovedBranchResetsTheReminderClock(t *testing.T) {
	home, _, _, sha := staleTicketAtCap(t)
	jira := newTracker()

	run(t, home, jira, PR{Verdict: VerdictPassing, Head: sha, URL: "u"}, Options{})
	run(t, home, jira, PR{Verdict: VerdictPassing, Head: sha, URL: "u"}, Options{}) // reminded

	moved := PR{Verdict: VerdictPassing, Head: sha + "0", URL: "u"}
	run(t, home, jira, moved, Options{}) // announced afresh
	_, out, _ := run(t, home, jira, moved, Options{})

	if !strings.Contains(out, "still behind") {
		t.Errorf("the reminder stayed suppressed across a push, so the branch went "+
			"quiet at the moment somebody was working on it:\n%s", out)
	}
}
