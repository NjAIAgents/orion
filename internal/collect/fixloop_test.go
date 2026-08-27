package collect

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/orion-sdlc/orion/internal/lessons"
	"github.com/orion-sdlc/orion/internal/workspace"
)

type fixSpy struct {
	calls  int
	pushed bool
	err    error
	sawAll []string
}

func (f *fixSpy) fix(_ *workspace.Workspace, _, _, failure string) (bool, error) {
	f.calls++
	f.sawAll = append(f.sawAll, failure)
	return f.pushed, f.err
}

// ciRepo builds a bound project with the fix loop switched on.
func ciRepo(t *testing.T, maxAttempts int) (home, wsDir string) {
	t.Helper()
	home = t.TempDir()
	t.Setenv("ORION_HOME", home)

	src := filepath.Join(t.TempDir(), "fcia")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	ws, err := workspace.New(workspace.NewOptions{Idea: "fcia"})
	if err != nil {
		t.Fatal(err)
	}
	cfg := `{"ci":{"auto_fix":true,"max_fix_attempts":` + itoa(maxAttempts) + `},
	         "vcs":{"work_branch":"develop","branch_prefix":"orion/"}}`
	if err := os.WriteFile(filepath.Join(src, "orion.json"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := bindTo(home, ws.ID, src); err != nil {
		t.Fatal(err)
	}
	return home, ws.Dir
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

func runFix(t *testing.T, home string, f *fixSpy, detail string, opts Options) (Result, string) {
	t.Helper()
	var buf bytes.Buffer
	opts.Home, opts.Out = home, &buf
	if len(opts.Keys) == 0 {
		opts.Keys = []string{"FCIA-6"}
	}
	res := Run(opts, Deps{
		Jira:    newTracker(),
		Status:  func(string, string) (PR, error) { return PR{Verdict: VerdictFailing, URL: "u", Detail: detail}, nil },
		Refresh: func(string, string) (string, error) { return "", nil },
		Prune:   func(*workspace.Workspace, string) error { return nil },
		Fix:     f.fix,
	})
	return res[0], buf.String()
}

func TestAFailingBuildIsSentBackToTheAgent(t *testing.T) {
	home, _ := ciRepo(t, 3)
	f := &fixSpy{pushed: true}

	res, out := runFix(t, home, f, "test (failure)\nassert 1 == 2", Options{})

	if f.calls != 1 {
		t.Fatalf("expected one fix attempt, got %d", f.calls)
	}
	if !strings.Contains(f.sawAll[0], "assert 1 == 2") {
		t.Error("the agent was not given the actual failure")
	}
	// It is building again, not failing: reporting it as failing would make
	// the CLI exit non-zero on a run that is making progress.
	if res.Verdict != VerdictPending {
		t.Errorf("verdict = %s, want pending after a fix was pushed", res.Verdict)
	}
	if !strings.Contains(out, "attempt 1 of 3") {
		t.Errorf("the attempt count must be visible: %s", out)
	}
}

// The ceiling. Without it this is a machine that spends money all night.
func TestTheLoopStopsAtTheAttemptCeiling(t *testing.T) {
	home, _ := ciRepo(t, 2)
	f := &fixSpy{pushed: true}

	// Each round must present a DIFFERENT failure, or the repeat brake stops
	// it first -- which is a separate test.
	runFix(t, home, f, "failure A", Options{})
	runFix(t, home, f, "failure B", Options{})
	_, out := runFix(t, home, f, "failure C", Options{})

	if f.calls != 2 {
		t.Fatalf("expected the ceiling to stop the loop at 2, got %d attempts", f.calls)
	}
	if !strings.Contains(out, "giving up") {
		t.Errorf("stopping must be explained, not silent: %s", out)
	}
}

// The brake that matters most. An agent that pushes a fix and gets back a
// byte-identical failure has learned nothing; spending the remaining
// attempts proves only that it can fail the same way three times.
func TestAnIdenticalFailureStopsTheLoopImmediately(t *testing.T) {
	home, _ := ciRepo(t, 5)
	f := &fixSpy{pushed: true}
	same := "test_impact.py::test_delta FAILED\nassert 100 == 99"

	runFix(t, home, f, same, Options{})
	_, out := runFix(t, home, f, same, Options{})

	if f.calls != 1 {
		t.Fatalf("an identical failure should stop the loop at once, got %d attempts", f.calls)
	}
	if !strings.Contains(out, "not making progress") {
		t.Errorf("the reason must distinguish this from the ceiling: %s", out)
	}
}

// Raw CI output carries run ids, timestamps and durations that differ every
// time. Comparing literally would mean the repeat brake never engaged.
func TestTheFingerprintIgnoresWhatChangesEveryRun(t *testing.T) {
	a := "2026-08-26T10:00:01Z test_x FAILED\nassert 1 == 2\nfinished in 4.21 seconds\nhttps://github.com/o/r/runs/111"
	b := "2026-08-26T11:47:52Z test_x FAILED\nassert 1 == 2\nfinished in 9.03 seconds\nhttps://github.com/o/r/runs/222"
	if Fingerprint(a) != Fingerprint(b) {
		t.Fatal("the same failure fingerprinted differently; the repeat brake would never fire")
	}
	if Fingerprint(a) == Fingerprint("assert 3 == 4") {
		t.Fatal("different failures fingerprinted the same; the loop would stop while still making progress")
	}
}

// Exit 0 with nothing pushed means the agent could not see what to change.
// Another identical attempt produces the same nothing at the same price.
func TestAnAgentThatChangesNothingEndsTheLoop(t *testing.T) {
	home, _ := ciRepo(t, 3)
	f := &fixSpy{pushed: false}

	_, out := runFix(t, home, f, "some failure", Options{})

	if f.calls != 1 {
		t.Fatalf("expected exactly one attempt, got %d", f.calls)
	}
	if !strings.Contains(out, "no change") {
		t.Errorf("got: %s", out)
	}
}

func TestAFixRunThatErrorsStopsTheLoop(t *testing.T) {
	home, _ := ciRepo(t, 3)
	f := &fixSpy{err: errors.New("breaker tripped")}

	res, out := runFix(t, home, f, "some failure", Options{})

	if res.Err == nil {
		t.Fatal("the error must be reported")
	}
	if !strings.Contains(out, "giving up") {
		t.Errorf("got: %s", out)
	}
}

// The attempt is counted before the run, so a crash cannot refund it. A
// ceiling that resets whenever the process dies is no ceiling at all -- and
// process death is exactly what a runaway loop tends to cause.
func TestTheAttemptIsCountedBeforeTheRunNotAfter(t *testing.T) {
	home, wsDir := ciRepo(t, 3)
	f := &fixSpy{err: errors.New("killed")}

	runFix(t, home, f, "boom", Options{})

	if got := loadFixes(wsDir).States["FCIA-6"].Count(); got != 1 {
		t.Fatalf("attempts recorded = %d, want 1; a crashed run must still spend its attempt", got)
	}
}

// --no-fix is for looking at a failure yourself before anything spends money
// reacting to it.
func TestNoFixSkipsTheLoopEntirely(t *testing.T) {
	home, _ := ciRepo(t, 3)
	f := &fixSpy{pushed: true}

	runFix(t, home, f, "some failure", Options{NoFix: true})

	if f.calls != 0 {
		t.Fatal("--no-fix still ran the agent")
	}
}

func TestDryRunDoesNotSpendAnAttempt(t *testing.T) {
	home, wsDir := ciRepo(t, 3)
	f := &fixSpy{pushed: true}

	_, out := runFix(t, home, f, "some failure", Options{DryRun: true})

	if f.calls != 0 {
		t.Fatal("dry run launched a real agent")
	}
	if got := loadFixes(wsDir).States["FCIA-6"].Count(); got != 0 {
		t.Fatalf("dry run recorded %d attempts", got)
	}
	if !strings.Contains(out, "would") {
		t.Errorf("got: %s", out)
	}
}

// Off by default: it spends money without being asked, and on a repository
// with flaky tests it will spend it on nothing.
func TestTheFixLoopIsOffUnlessConfigured(t *testing.T) {
	home, _ := bound(t) // no ci.auto_fix
	f := &fixSpy{pushed: true}

	runFix(t, home, f, "some failure", Options{})

	if f.calls != 0 {
		t.Fatal("the fix loop ran without being switched on")
	}
}

// A merged ticket must forget its history, or one reopened months later
// starts with its attempts already spent.
func TestMergingClearsTheAttemptHistory(t *testing.T) {
	home, wsDir := ciRepo(t, 3)
	f := &fixSpy{pushed: true}
	runFix(t, home, f, "some failure", Options{})
	if loadFixes(wsDir).States["FCIA-6"].Count() == 0 {
		t.Fatal("precondition: an attempt should have been recorded")
	}

	var buf bytes.Buffer
	Run(Options{Home: home, Out: &buf, Keys: []string{"FCIA-6"}}, Deps{
		Jira:    newTracker(),
		Status:  func(string, string) (PR, error) { return PR{Verdict: VerdictMerged, URL: "u"}, nil },
		Refresh: func(string, string) (string, error) { return "", nil },
		Prune:   func(*workspace.Workspace, string) error { return nil },
	})

	if got := loadFixes(wsDir).States["FCIA-6"].Count(); got != 0 {
		t.Fatalf("history survived the merge: %d attempts still recorded", got)
	}
}

// The whole point of OR-99: a run that produced a lesson-worthy event must
// record it WITHOUT anyone typing a command. Before this, every writer of the
// lessons store was a hand-typed command, so the store had never held anything
// and the two-strike rule had nothing to count.
//
// A build that went red and then merged is that event: a mistake with its own
// correction attached, both observed rather than inferred.
func TestAFixedAndMergedBuildProposesALesson(t *testing.T) {
	home, _ := ciRepo(t, 3)
	failure := "test_impact.py::test_delta FAILED\nassert 100 == 99"
	store := lessons.New(home)

	redThenMerged(t, home, failure)

	// Once is circumstance. Nobody should be asked about it yet.
	pending, err := store.Pending()
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 0 {
		t.Fatalf("a single occurrence was offered for approval: %+v", pending)
	}
	health, err := store.Health()
	if err != nil {
		t.Fatal(err)
	}
	if !health.Observed() {
		t.Fatal("nothing was observed, so no automatic path wrote to the store")
	}

	// Twice is a pattern.
	redThenMerged(t, home, failure)

	pending, err = store.Pending()
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 {
		t.Fatalf("got %d proposals awaiting approval, want 1", len(pending))
	}
	c := pending[0]
	if !strings.Contains(c.Text, "test_delta FAILED") {
		t.Errorf("the proposal does not say what actually happened: %q", c.Text)
	}
	if len(c.Evidence) != 2 {
		t.Errorf("each sighting must carry its own evidence, got %v", c.Evidence)
	}
	for _, e := range c.Evidence {
		if !strings.Contains(e, "FCIA-6") || !strings.Contains(e, "fcia") {
			t.Errorf("evidence must name the ticket and the project: %q", e)
		}
	}

	// And nothing has been recorded. A proposal is not a lesson.
	records, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 0 {
		t.Fatalf("a lesson was written without anyone approving it: %+v", records)
	}
}

// A ticket that merged green taught nobody anything. Proposing on every merge
// would fill the reviewer's queue with non-events, and a queue of non-events
// gets dismissed without reading -- taking the real lessons with it.
func TestAGreenMergeProposesNothing(t *testing.T) {
	home, _ := ciRepo(t, 3)

	mergeIt(t, home)

	health, err := lessons.New(home).Health()
	if err != nil {
		t.Fatal(err)
	}
	if health.Sightings != 0 {
		t.Fatalf("a merge with no CI failure filed %d proposal(s)", health.Sightings)
	}
}

// redThenMerged drives one full episode: CI fails, an agent pushes a fix, the
// pull request merges.
func redThenMerged(t *testing.T, home, failure string) {
	t.Helper()
	runFix(t, home, &fixSpy{pushed: true}, failure, Options{})
	mergeIt(t, home)
}

func mergeIt(t *testing.T, home string) {
	t.Helper()
	var buf bytes.Buffer
	Run(Options{Home: home, Out: &buf, Keys: []string{"FCIA-6"}}, Deps{
		Jira:    newTracker(),
		Status:  func(string, string) (PR, error) { return PR{Verdict: VerdictMerged, URL: "u"}, nil },
		Refresh: func(string, string) (string, error) { return "", nil },
		Prune:   func(*workspace.Workspace, string) error { return nil },
	})
}
