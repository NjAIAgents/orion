package work

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/orion-sdlc/orion/internal/config"
	"github.com/orion-sdlc/orion/internal/events"
	"github.com/orion-sdlc/orion/internal/suite"
	"github.com/orion-sdlc/orion/internal/supervisor"
	"github.com/orion-sdlc/orion/internal/workspace"
)

// fanCases is a list big enough to divide, used by every test here so a
// change to the splitter shows up as one failure rather than five.
const fanCases = "Authentication:\n- rejects an expired token\n- rejects a missing token\n" +
	"Rate limiting:\n- returns 429 past the limit\n- resets after the window"

// recordingFan stands in for supervisor.Fan: it records what it was asked to
// run and reports every child as a clean exit.
func recordingFan(got *[]supervisor.Options) func(*workspace.Workspace, []supervisor.Options) []supervisor.FanResult {
	return func(_ *workspace.Workspace, jobs []supervisor.Options) []supervisor.FanResult {
		*got = append(*got, jobs...)
		out := make([]supervisor.FanResult, len(jobs))
		for i := range jobs {
			out[i] = supervisor.FanResult{Result: &supervisor.Result{ExitCode: 0}}
		}
		return out
	}
}

func fanTestLog(t *testing.T) *events.Log {
	t.Helper()
	log, err := events.Open(filepath.Join(t.TempDir(), "events.jsonl"), events.Event{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = log.Close() })
	return log
}

// TestTheCaseListSurvivesTheFan is the safety property the whole feature
// rests on: QA must still be handed every case, so a fan that writes nothing
// useful still leaves the stage exactly as capable as it was.
func TestTheCaseListSurvivesTheFan(t *testing.T) {
	var dispatched []supervisor.Options
	var buf bytes.Buffer

	got := fanAuthoring(qaJob{Key: "OR-1", Summary: "s"}, config.Config{},
		fanCases, Deps{Fan: recordingFan(&dispatched)}, fanTestLog(t), &buf)

	if got != fanCases {
		t.Errorf("the case list changed across the fan:\ngot:  %q\nwant: %q", got, fanCases)
	}
	if len(dispatched) < 2 {
		t.Fatalf("expected a fan, got %d job(s)", len(dispatched))
	}
}

// TestWithNoFanInjectedTheStageIsUnchanged. Nil Fan is the serial path, and
// it must dispatch nothing at all -- not a fan of one.
func TestWithNoFanInjectedTheStageIsUnchanged(t *testing.T) {
	var buf bytes.Buffer
	got := fanAuthoring(qaJob{Key: "OR-1", Summary: "s"}, config.Config{},
		fanCases, Deps{}, fanTestLog(t), &buf)

	if got != fanCases {
		t.Errorf("cases changed on the serial path: %q", got)
	}
	if buf.Len() != 0 {
		t.Errorf("the serial path announced something: %q", buf.String())
	}
}

// TestAnEmptyCaseListDoesNotFan. Nothing derived means nothing to divide, and
// dispatching agents to write an empty list is spend for no coverage.
func TestAnEmptyCaseListDoesNotFan(t *testing.T) {
	var dispatched []supervisor.Options
	var buf bytes.Buffer

	fanAuthoring(qaJob{Key: "OR-1", Summary: "s"}, config.Config{},
		"   \n  ", Deps{Fan: recordingFan(&dispatched)}, fanTestLog(t), &buf)

	if len(dispatched) != 0 {
		t.Errorf("fanned an empty case list into %d job(s)", len(dispatched))
	}
}

// TestAWidthOfOneTakesTheSerialPath: the operator has switched the fan off,
// and one child is not a fan -- it is the serial path with extra bookkeeping.
func TestAWidthOfOneTakesTheSerialPath(t *testing.T) {
	var dispatched []supervisor.Options
	var buf bytes.Buffer
	cfg := config.Config{QA: config.QA{AuthorAgents: 1}}

	fanAuthoring(qaJob{Key: "OR-1", Summary: "s"}, cfg, fanCases,
		Deps{Fan: recordingFan(&dispatched)}, fanTestLog(t), &buf)

	if len(dispatched) != 0 {
		t.Errorf("a width of 1 dispatched %d job(s)", len(dispatched))
	}
}

// TestTheWidthIsHonoured proves the config reaches the fan rather than the
// default being used regardless of what an operator set.
func TestTheWidthIsHonoured(t *testing.T) {
	var dispatched []supervisor.Options
	var buf bytes.Buffer
	cfg := config.Config{QA: config.QA{AuthorAgents: 2}}

	fanAuthoring(qaJob{Key: "OR-1", Summary: "s"}, cfg, fanCases,
		Deps{Fan: recordingFan(&dispatched)}, fanTestLog(t), &buf)

	if len(dispatched) != 2 {
		t.Errorf("width 2 dispatched %d job(s), want 2", len(dispatched))
	}
}

// TestNoAuthorIsToldToRunTheTests. The other writers are still working, so a
// child that compiles sees failures that are not its own -- ADR 0016's first
// hazard, which this stage escapes only by nobody building until every writer
// has stopped.
func TestNoAuthorIsToldToRunTheTests(t *testing.T) {
	var dispatched []supervisor.Options
	var buf bytes.Buffer

	fanAuthoring(qaJob{Key: "OR-1", Summary: "s"}, config.Config{},
		fanCases, Deps{Fan: recordingFan(&dispatched)}, fanTestLog(t), &buf)

	for i, j := range dispatched {
		if !strings.Contains(j.Prompt, "DO NOT RUN THE TESTS") {
			t.Errorf("author %d was not told to leave the suite alone", i)
		}
	}
}

// TestAFailedAuthorIsReportedAndCostsNoCoverage. A child that dies must not
// take its cases with it: they stay in the list QA verifies, so the stage
// loses time and not tests.
func TestAFailedAuthorIsReportedAndCostsNoCoverage(t *testing.T) {
	var buf bytes.Buffer
	failing := func(_ *workspace.Workspace, jobs []supervisor.Options) []supervisor.FanResult {
		out := make([]supervisor.FanResult, len(jobs))
		for i := range jobs {
			out[i] = supervisor.FanResult{Result: &supervisor.Result{ExitCode: 1}}
		}
		return out
	}

	got := fanAuthoring(qaJob{Key: "OR-1", Summary: "s"}, config.Config{},
		fanCases, Deps{Fan: failing}, fanTestLog(t), &buf)

	if got != fanCases {
		t.Errorf("a failed fan changed the case list: %q", got)
	}
	if !strings.Contains(buf.String(), "did not finish") {
		t.Errorf("a failed author was not reported:\n%s", buf.String())
	}
}

// TestTheFanIsAnnouncedBeforeItSpends, per nj-agents
// CONVENTIONS-orchestration §C: an agent count first seen in the bill was
// disclosed too late to decline.
func TestTheFanIsAnnouncedBeforeItSpends(t *testing.T) {
	var dispatched []supervisor.Options
	var buf bytes.Buffer

	fanAuthoring(qaJob{Key: "OR-1", Summary: "s"}, config.Config{},
		fanCases, Deps{Fan: recordingFan(&dispatched)}, fanTestLog(t), &buf)

	out := buf.String()
	if !strings.Contains(out, "authors") {
		t.Errorf("the fan did not say how wide it was:\n%s", out)
	}
}

// TestOrionRunsTheSuiteAfterTheAuthors is the OR-306 contract: the fan writes,
// then Orion runs. A green suite is reported by Orion rather than narrated by
// an agent that may not have run it.
func TestOrionRunsTheSuiteAfterTheAuthors(t *testing.T) {
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, "scripts"), 0o755); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(repo, "scripts", "test.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	var dispatched []supervisor.Options
	runAuthoredSuiteAt(t, repo, &buf, &dispatched)

	if !strings.Contains(buf.String(), "green") {
		t.Errorf("Orion did not report running the suite:\n%s", buf.String())
	}
}

// TestARedSuiteIsReportedRed. The whole point of Orion running it is that the
// verdict comes from an exit code rather than a narration.
func TestARedSuiteIsReportedRed(t *testing.T) {
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, "scripts"), 0o755); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(repo, "scripts", "test.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho FAIL\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	var dispatched []supervisor.Options
	runAuthoredSuiteAt(t, repo, &buf, &dispatched)

	if !strings.Contains(buf.String(), "red") {
		t.Errorf("a failing suite was not reported red:\n%s", buf.String())
	}
}

// TestAnUndetectableSuiteFallsBackAndSaysSo. Degrading to the agent path is
// allowed; degrading silently is not, because a stage that quietly stopped
// verifying reads exactly like one that did not.
func TestAnUndetectableSuiteFallsBackAndSaysSo(t *testing.T) {
	var buf bytes.Buffer
	var dispatched []supervisor.Options
	runAuthoredSuiteAt(t, t.TempDir(), &buf, &dispatched)

	if !strings.Contains(buf.String(), "QA runs the tests itself") {
		t.Errorf("the fallback was silent:\n%s", buf.String())
	}
}

// runAuthoredSuiteAt points a workspace at repo and runs the suite step
// against it. RepoPath is the runtime override a real job already uses for
// its own worktree, so this is the same path production takes.
func runAuthoredSuiteAt(t *testing.T, repo string, buf *bytes.Buffer, _ *[]supervisor.Options) {
	t.Helper()
	prev := suiteTimeout
	suiteTimeout = 30 * time.Second
	t.Cleanup(func() { suiteTimeout = prev })

	job := qaJob{Key: "OR-1", Summary: "s", WS: &workspace.Workspace{RepoPath: repo}}
	runAuthoredSuite(job, config.Config{}, fanTestLog(t), buf)
}

// TestTheSuiteRunsEvenWhenNothingFanned is the regression guard for a bug
// this code had before review: runAuthoredSuite was called from inside
// fanAuthoring, so a ticket too small to fan -- the common case -- never had
// its suite run by Orion at all, and was silently verified the old way.
//
// The two features are independent. Fanning is about how tests get WRITTEN;
// running them is about who decides whether they PASS.
func TestTheSuiteRunsEvenWhenNothingFanned(t *testing.T) {
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, "scripts"), 0o755); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(repo, "scripts", "test.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	prev := suiteTimeout
	suiteTimeout = 30 * time.Second
	t.Cleanup(func() { suiteTimeout = prev })

	var buf bytes.Buffer
	job := qaJob{Key: "OR-1", Summary: "s", WS: &workspace.Workspace{RepoPath: repo}}
	log := fanTestLog(t)

	// One case: far too little to fan, so fanAuthoring returns immediately.
	cases := fanAuthoring(job, config.Config{}, "- one case", Deps{}, log, &buf)
	if cases != "- one case" {
		t.Errorf("the serial path changed the case list: %q", cases)
	}
	if buf.Len() != 0 {
		t.Fatalf("the serial path announced a fan: %q", buf.String())
	}

	// The suite must still run.
	runAuthoredSuite(job, config.Config{}, log, &buf)
	if !strings.Contains(buf.String(), "green") {
		t.Errorf("Orion did not run the suite on the un-fanned path:\n%s", buf.String())
	}
}

// TestARedSuiteReachesTheVerdict is OR-312's regression guard.
//
// The first cut of the suite runner printed "the suite is red", logged the
// output, and returned nothing. QA then formed its verdict having never been
// told, so four tickets in one run reported "every case passes" over a suite
// Orion had already failed -- and a gofmt error reached a shared branch and
// failed CI twenty minutes later.
func TestARedSuiteReachesTheVerdict(t *testing.T) {
	red := &suite.Result{Cmd: "./scripts/test.sh", Passed: false,
		Output: "these files are not gofmt'd:\ninternal/decide/decide_test.go"}

	got := suiteEvidence(red)
	if got == "" {
		t.Fatal("a failing suite produced no evidence for the verdict")
	}
	if !strings.Contains(got, "not gofmt'd") {
		t.Error("the failure OUTPUT is missing; the fix round has nothing to act on")
	}
	if !strings.Contains(got, "FAILED") {
		t.Error("the evidence does not say the suite failed")
	}
}

// TestAGreenSuiteAddsNothing. QA runs its own cases regardless, so a passing
// suite is not news -- and padding every prompt with it would crowd out the
// cases that are.
func TestAGreenSuiteAddsNothing(t *testing.T) {
	if got := suiteEvidence(&suite.Result{Passed: true}); got != "" {
		t.Errorf("a green suite added %q to the prompt", got)
	}
}

// TestAnUnrunnableSuiteIsNotEvidence. Not knowing is not the same as failing.
// A suite that could not be detected, or a command that would not start, must
// not read to QA as a failure.
func TestAnUnrunnableSuiteIsNotEvidence(t *testing.T) {
	for name, res := range map[string]*suite.Result{
		"never ran":     nil,
		"could not run": {Err: errors.New("exec: not found")},
	} {
		if got := suiteEvidence(res); got != "" {
			t.Errorf("%s: produced evidence %q", name, got)
		}
	}
}

// TestATimedOutSuiteSaysSoRatherThanFailing. A hung suite and a failing suite
// call for different responses, and reporting one as the other sends a person
// looking for a defect that is not there.
func TestATimedOutSuiteSaysSoRatherThanFailing(t *testing.T) {
	got := suiteEvidence(&suite.Result{TimedOut: true, Cmd: "./scripts/test.sh"})
	if !strings.Contains(got, "DID NOT FINISH") {
		t.Errorf("a timeout was not distinguished from a failure:\n%s", got)
	}
}
