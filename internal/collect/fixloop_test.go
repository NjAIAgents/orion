package collect

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/orion-sdlc/orion/internal/events"
	"github.com/orion-sdlc/orion/internal/lessons"
	"github.com/orion-sdlc/orion/internal/workspace"
)

type fixSpy struct {
	calls   int
	pushed  bool
	err     error
	summary string
	denied  *PolicyDenial
	sawAll  []string
}

func (f *fixSpy) fix(_ *workspace.Workspace, _, _, failure string, _ *events.Log) (bool, string, *PolicyDenial, error) {
	f.calls++
	f.sawAll = append(f.sawAll, failure)
	return f.pushed, f.summary, f.denied, f.err
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

// The message must not say the agent doesn't know how to fix this when it
// was never permitted to try -- OR-172 printed exactly that contradiction:
// a root cause diagnosed correctly, reported as evidence of not knowing how.
// The two failures need different remedies, so the message must name what
// was blocked, and the agent's own diagnosis must survive as a hand-off
// rather than being thrown away with the run (OR-174).
func TestAPolicyDenialIsDistinctFromGivingUp(t *testing.T) {
	home, wsDir := ciRepo(t, 3)
	f := &fixSpy{pushed: false, denied: &PolicyDenial{
		Tool: "Edit", Path: ".github/workflows/ci.yml", Rule: ".github/workflows/**",
		HandOff: "Root cause: ci.yml's concurrency.group needs a pull_request.number fallback.",
	}}

	_, out := runFix(t, home, f, "some failure", Options{})

	if strings.Contains(out, "does not know how to fix this") {
		t.Errorf("a policy denial must not read as the agent not knowing how: %s", out)
	}
	if !strings.Contains(out, "blocked by policy") {
		t.Errorf("the message must say it was blocked by policy: %s", out)
	}
	if !strings.Contains(out, "Edit(.github/workflows/ci.yml)") {
		t.Errorf("the tool and path must be named: %s", out)
	}
	if !strings.Contains(out, ".github/workflows/**") {
		t.Errorf("the matched rule must be named: %s", out)
	}
	if !strings.Contains(out, "concurrency.group needs a pull_request.number fallback") {
		t.Errorf("the agent's own hand-off must reach the console, not be thrown away: %s", out)
	}
	if got := loadFixes(wsDir).States["FCIA-6"].Count(); got != 0 {
		t.Fatalf("a policy-denied attempt must not spend the fix budget, got %d attempts recorded", got)
	}
}

// Proven by driving it past a ceiling that would stop a counted attempt
// after one round: a denial can never succeed by retrying (the sandbox does
// not change its mind), but it also must never be blamed on the agent's
// three-attempt budget, which exists to measure the agent.
func TestAPolicyDenialNeverSpendsTheCeiling(t *testing.T) {
	home, wsDir := ciRepo(t, 1) // a ceiling that would stop a real attempt at 1
	f := &fixSpy{pushed: false, denied: &PolicyDenial{Tool: "Edit", Path: "orion.json", Rule: "orion.json"}}

	for i := 0; i < 3; i++ {
		runFix(t, home, f, "some failure", Options{})
	}

	if f.calls != 3 {
		t.Fatalf("a retracted attempt must not trip the ceiling: expected 3 agent runs, got %d", f.calls)
	}
	if got := loadFixes(wsDir).States["FCIA-6"].Count(); got != 0 {
		t.Fatalf("attempts recorded = %d, want 0 -- a policy denial proves nothing about the agent", got)
	}
}

// retractAttempt is documented to remove only the single most recent
// attempt, not the ticket's whole history -- a prior GENUINE attempt (one
// that actually ran and taught nothing) must still count toward the
// three-attempt budget even after a later round on the same ticket is
// denied by policy and retracted. Both existing denial tests start from an
// empty history, so neither would catch retractAttempt trimming too much.
func TestAPolicyDenialRetractsOnlyItsOwnAttemptNotPriorHistory(t *testing.T) {
	home, wsDir := ciRepo(t, 3)
	f := &fixSpy{pushed: false}

	// Round 1: a genuine attempt that changed nothing and was not denied --
	// spends the budget, as TestAnAgentThatChangesNothingEndsTheLoop covers.
	runFix(t, home, f, "failure A", Options{})
	if got := loadFixes(wsDir).States["FCIA-6"].Count(); got != 1 {
		t.Fatalf("after a genuine no-change attempt, count = %d, want 1", got)
	}

	// Round 2: a DIFFERENT failure (so the repeat brake does not intervene),
	// this time denied by policy.
	f.denied = &PolicyDenial{Tool: "Edit", Path: "orion.json", Rule: "orion.json"}
	runFix(t, home, f, "failure B", Options{})

	if got := loadFixes(wsDir).States["FCIA-6"].Count(); got != 1 {
		t.Fatalf("attempts recorded = %d, want 1 -- retracting the denied round must not "+
			"also erase the prior genuine attempt", got)
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
	rootCause := "test_delta compares against a stale fixture baseline instead of the regenerated one"
	store := lessons.New(home)

	redThenMerged(t, home, failure, rootCause)

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
	redThenMerged(t, home, failure, rootCause)

	pending, err = store.Pending()
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 {
		t.Fatalf("got %d proposals awaiting approval, want 1", len(pending))
	}
	c := pending[0]
	// The text is the transferable diagnosis, not a restatement of the CI
	// check name -- that is the whole point of OR-177.
	if !strings.Contains(c.Text, "stale fixture baseline") {
		t.Errorf("the proposal does not carry the stated root cause: %q", c.Text)
	}
	if strings.Contains(c.Text, "test_delta FAILED") {
		t.Errorf("the CI failure line belongs in the evidence, not the text: %q", c.Text)
	}
	if len(c.Evidence) != 2 {
		t.Errorf("each sighting must carry its own evidence, got %v", c.Evidence)
	}
	for _, e := range c.Evidence {
		if !strings.Contains(e, "FCIA-6") || !strings.Contains(e, "fcia") {
			t.Errorf("evidence must name the ticket and the project: %q", e)
		}
		if !strings.Contains(e, "test_delta FAILED") {
			t.Errorf("evidence must carry the CI check/failure that was seen: %q", e)
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

// The bug OR-177 fixes: a repo with one job matrix produces the same CI
// check name for every failure it will ever have, so keying the lesson on
// the check name collapses a gofmt violation in one file and a broken build
// in another into a single vacuous "CI sometimes fails" lesson. Keying on
// the normalized root cause instead means two occurrences of the SAME
// mistake in DIFFERENT files still collide into one lesson -- which is the
// two-strike rule actually working, not two unrelated failures pretending
// to agree because they share a job name.
func TestTwoRootCausesInDifferentFilesCollapseToOneLesson(t *testing.T) {
	home, _ := ciRepo(t, 3)
	store := lessons.New(home)

	redThenMerged(t, home, "go (ubuntu-latest) (failure)",
		"internal/work/work_test.go, added by this branch's commits, wasn't "+
			"gofmt-formatted -- scripts/test.sh's gofmt -l gate runs before build/test")
	redThenMerged(t, home, "go (ubuntu-latest) (failure)",
		"internal/lessons/propose_test.go, added by this branch's commits, wasn't "+
			"gofmt-formatted -- scripts/test.sh's gofmt -l gate runs before build/test")

	pending, err := store.Pending()
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 {
		t.Fatalf("two sightings of the same mechanism in different files produced %d lessons, want 1: %+v",
			len(pending), pending)
	}
	if strings.Contains(pending[0].Text, "work_test.go") || strings.Contains(pending[0].Text, "propose_test.go") {
		t.Errorf("the lesson text still names one sighting's file: %q", pending[0].Text)
	}
}

// A run with no stated root cause -- an older run, or a fix loop that gave up
// before ever stating one -- has nothing transferable to say. Falling back to
// the CI check name is exactly the bug OR-177 fixes, so this must propose
// nothing rather than that fallback.
func TestAFixWithNoStatedRootCauseProposesNothing(t *testing.T) {
	home, _ := ciRepo(t, 3)

	redThenMerged(t, home, "go (ubuntu-latest) (failure)", "")
	redThenMerged(t, home, "go (ubuntu-latest) (failure)", "")

	health, err := lessons.New(home).Health()
	if err != nil {
		t.Fatal(err)
	}
	if health.Sightings != 0 {
		t.Fatalf("a fix with no stated root cause filed %d proposal(s)", health.Sightings)
	}
}

// A whitespace-only root cause is not a stated one. recordRootCause trims
// before deciding whether to write it, so this must propose nothing rather
// than filing a lesson whose text is blank.
func TestAWhitespaceOnlyRootCauseProposesNothing(t *testing.T) {
	home, _ := ciRepo(t, 3)

	redThenMerged(t, home, "go (ubuntu-latest) (failure)", "   ")
	redThenMerged(t, home, "go (ubuntu-latest) (failure)", "   ")

	health, err := lessons.New(home).Health()
	if err != nil {
		t.Fatal(err)
	}
	if health.Sightings != 0 {
		t.Fatalf("a whitespace-only root cause filed %d proposal(s)", health.Sightings)
	}
}

// A root cause built entirely out of what normalizeRootCause strips -- a bare
// ticket key, with no surrounding mechanism -- normalizes to the empty
// string even though the agent did state something. This must propose
// nothing for the same reason an actually-empty root cause does: falling
// back to whatever text happens to survive is exactly the vacuous-lesson bug
// OR-177 fixes, and an empty text would file a lesson nobody could read.
func TestARootCauseThatNormalizesToNothingProposesNothing(t *testing.T) {
	home, _ := ciRepo(t, 3)

	redThenMerged(t, home, "go (ubuntu-latest) (failure)", "OR-114")
	redThenMerged(t, home, "go (ubuntu-latest) (failure)", "OR-114")

	health, err := lessons.New(home).Health()
	if err != nil {
		t.Fatal(err)
	}
	if health.Sightings != 0 {
		t.Fatalf("a root cause that normalizes to nothing filed %d proposal(s)", health.Sightings)
	}
}

// proposeLesson reads the root cause off state.Attempts[len-1] -- the LAST
// attempt, the one whose fix actually stuck since the merge proves it. An
// episode can push more than one fix before it merges (the first fix didn't
// fully work, CI failed again with a different failure, a second fix did);
// the lesson must reflect what actually fixed it, not what the first attempt
// guessed.
func TestLessonReflectsTheLastAttemptsRootCauseNotAnEarlierOnes(t *testing.T) {
	home, _ := ciRepo(t, 3)

	twoAttemptEpisode := func() {
		runFix(t, home, &fixSpy{pushed: true, summary: "an unrelated helper had a stale cache key"},
			"failure A", Options{})
		runFix(t, home, &fixSpy{pushed: true, summary: "the real fix: a nil check was missing on the parsed config"},
			"failure B", Options{})
		mergeIt(t, home)
	}
	twoAttemptEpisode()
	twoAttemptEpisode()

	pending, err := lessons.New(home).Pending()
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 {
		t.Fatalf("got %d proposals, want 1: %+v", len(pending), pending)
	}
	if !strings.Contains(pending[0].Text, "nil check was missing") {
		t.Errorf("the lesson must carry the LAST attempt's root cause, got %q", pending[0].Text)
	}
	if strings.Contains(pending[0].Text, "stale cache key") {
		t.Errorf("the lesson must not carry an earlier attempt's root cause that didn't actually fix it, got %q", pending[0].Text)
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

// redThenMerged drives one full episode: CI fails, an agent pushes a fix
// stating the given root cause as its closing message, the pull request
// merges.
func redThenMerged(t *testing.T, home, failure, rootCause string) {
	t.Helper()
	runFix(t, home, &fixSpy{pushed: true, summary: rootCause}, failure, Options{})
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

// The one-line summary from the agent's own closing message rides along on
// both the "pushed a fix" console line and the event log, not just the cost
// stats that were already there (OR-129).
func TestPushedFixCarriesTheAgentsOwnSummary(t *testing.T) {
	home, _ := ciRepo(t, 3)
	f := &fixSpy{pushed: true, summary: "renamed the stale base helper to behind()"}

	_, out := runFix(t, home, f, "test (failure)\nassert 1 == 2", Options{})

	if !strings.Contains(out, "renamed the stale base helper to behind()") {
		t.Errorf("the agent's summary must reach the console:\n%s", out)
	}
}

// A held branch is left alone entirely -- the fix loop must not spend part
// of a ticket's attempt budget, or touch the branch, while a person has it
// locked for manual work (OR-130).
func TestFixLoopSkipsAManuallyLockedBranch(t *testing.T) {
	home, wsDir := ciRepo(t, 3)
	repoDir := filepath.Join(wsDir, "repo")
	if err := os.MkdirAll(repoDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repoDir, manualLockName), []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}
	f := &fixSpy{pushed: true}

	_, out := runFix(t, home, f, "test (failure)\nassert 1 == 2", Options{})

	if f.calls != 0 {
		t.Errorf("the fix loop must not run at all while the branch is locked, got %d calls", f.calls)
	}
	if !strings.Contains(out, manualLockName) {
		t.Errorf("the lock must be named in the output so it is discoverable:\n%s", out)
	}
}

// normalizeRootCause must strip exactly what varies between two sightings of
// the same mistake -- the file path, the branch, the ticket key -- and leave
// the mechanism readable, since the result is both the dedup key and the
// text a human reads in the approval prompt and, later, in CLAUDE.md.
func TestNormalizeRootCause(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "strips a file path but keeps the mechanism",
			in:   "internal/work/work_test.go wasn't gofmt-formatted before the build/test gate ran",
			want: "a file wasn't gofmt-formatted before the build/test gate ran",
		},
		{
			name: "two different files normalize to the same text",
			in:   "internal/lessons/propose_test.go wasn't gofmt-formatted before the build/test gate ran",
			want: "a file wasn't gofmt-formatted before the build/test gate ran",
		},
		{
			name: "strips a branch name",
			in:   "fix/OR-114-gofmt reintroduced the same missing nil check",
			want: "a branch reintroduced the same missing nil check",
		},
		{
			name: "strips a bare ticket key",
			in:   "OR-114 regressed the same nil check",
			want: "regressed the same nil check",
		},
		{
			name: "empty input normalizes to empty",
			in:   "",
			want: "",
		},
		{
			name: "whitespace-only input normalizes to empty",
			in:   "   ",
			want: "",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := normalizeRootCause(c.in); got != c.want {
				t.Errorf("normalizeRootCause(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}

	a := normalizeRootCause("internal/work/work_test.go wasn't gofmt-formatted before the build/test gate ran")
	b := normalizeRootCause("internal/lessons/propose_test.go wasn't gofmt-formatted before the build/test gate ran")
	if a != b || a == "" {
		t.Errorf("two sightings differing only by file path must normalize identically, got %q and %q", a, b)
	}
}
