package done

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/orion-sdlc/orion/internal/events"
)

// neverAsked fails the test if the model is reached. Most of what follows is
// about the rules, and a rule that quietly delegated to a model would be a
// different design wearing the same name.
func neverAsked(t *testing.T) Asker {
	t.Helper()
	return func(Evidence) (string, error) {
		t.Fatal("the model was asked a question the rules had already answered")
		return "", nil
	}
}

func at(s string) time.Time {
	ts, _ := time.Parse(time.RFC3339, s)
	return ts
}

// ---------------------------------------------------------------------------
// The acceptance test: the three runs of 2026-08-30 that reported success and
// were not done. Each is replayed from the evidence that actually existed at
// the time, and each must come back NOT DONE.
//
// These three are the reason this package exists. If any of them ever passes
// as done again, the pass has stopped doing the only job it was built for.
// ---------------------------------------------------------------------------

// OR-116: the QA run crashed -- claude exited 1 -- so the change went to
// review with nothing verified, and the pull request was green with three
// passing checks. The event internal/work writes when that happens is the
// whole evidence, and it was already in the log.
func TestOR116AQAStageThatCrashedIsNotDone(t *testing.T) {
	ev := Evidence{Key: "OR-116", Events: []events.Event{
		{At: at("2026-08-30T09:00:00Z"), Run: "1", Key: "OR-116",
			Kind: events.KindQA, Actor: events.ActorOrion,
			Msg: "QA stopped: the QA run did not finish: exit 1, the CLI exited non-zero"},
	}}

	v := Triage(ev, neverAsked(t))

	if v.Done {
		t.Fatalf("a change QA never verified was reported done: %s", v.Report())
	}
	if got := v.Findings[0].Check; got != CheckQAVerdict {
		t.Errorf("check = %q, want %q", got, CheckQAVerdict)
	}
	// The evidence, not just the verdict. A hand-back a person cannot check
	// is one they learn to wave through.
	if !strings.Contains(strings.Join(v.Findings[0].Evidence, "\n"), "exit 1") {
		t.Errorf("the finding does not carry the log line it rests on: %v", v.Findings[0].Evidence)
	}
}

// The same defect with its other symptom: QA ran to the end and its closing
// message named neither the clean sentinel nor a finding, so nothing was
// verified and no fix round could be dispatched.
func TestOR116AQAStageThatGaveNoVerdictIsNotDone(t *testing.T) {
	ev := Evidence{Key: "OR-116", Events: []events.Event{
		{Run: "1", Key: "OR-116", Kind: events.KindEscalate, Actor: events.ActorQA,
			Msg: "QA never reported a verdict: its closing message named neither " +
				"ALL CASES PASS nor any finding"},
	}}

	if v := Triage(ev, neverAsked(t)); v.Done {
		t.Fatalf("an unverified change was reported done: %s", v.Report())
	}
}

// OR-217: green and mergeable. The test that caught its off-by-one had been
// written by QA and never committed, so CI tested a commit that did not
// contain it -- committing it by hand turned the pull request red at once.
// The file was sitting in the worktree the whole time.
func TestOR217ATestLeftInTheWorktreeIsNotDone(t *testing.T) {
	ev := Evidence{Key: "OR-217", Diff: Diff{
		Files:    []string{"internal/collect/rebase.go"},
		Stranded: []string{"internal/collect/rebase_test.go (in the worktree, not in the diff)"},
	}}

	v := Triage(ev, neverAsked(t))

	if v.Done {
		t.Fatalf("a branch whose test never reached it was reported done: %s", v.Report())
	}
	if got := v.Findings[0].Check; got != CheckStranded {
		t.Errorf("check = %q, want %q", got, CheckStranded)
	}
	if !strings.Contains(strings.Join(v.Findings[0].Evidence, "\n"), "rebase_test.go") {
		t.Errorf("the finding does not name the file: %v", v.Findings[0].Evidence)
	}
}

// OR-229: green. Its own test passed at -count=1 and failed at -count=2 --
// under concurrency the fan paired answers with the wrong questions, and one
// run happened to schedule the goroutines the way the assertions expected.
func TestOR229TestsThatFailOnTheSecondRunAreNotDone(t *testing.T) {
	ev := Evidence{Key: "OR-229", Rerun: Rerun{
		Packages: []string{"./internal/supervisor"},
		Failed:   true,
		Output:   "--- FAIL: TestFanPairsEachAnswerWithItsQuestion\nFAIL",
	}}

	v := Triage(ev, neverAsked(t))

	if v.Done {
		t.Fatalf("a test that only passes once was reported done: %s", v.Report())
	}
	if got := v.Findings[0].Check; got != CheckRerun {
		t.Errorf("check = %q, want %q", got, CheckRerun)
	}
	if !strings.Contains(strings.Join(v.Findings[0].Evidence, "\n"), "TestFanPairs") {
		t.Errorf("the finding does not carry the failing test: %v", v.Findings[0].Evidence)
	}
}

// ---------------------------------------------------------------------------
// The other half of the contract: what must NOT be handed back. A pass that
// cries wolf is worse than no pass, because the first thing it costs is the
// attention the real findings needed.
// ---------------------------------------------------------------------------

// A QA stage that reported findings, or reported clean, gave a verdict. That
// is the system working, and it is not this pass's business.
func TestAQAStageThatReportedIsNotAFinding(t *testing.T) {
	for _, msg := range []string{
		"verified the change; every case passes (0 fix round(s))",
		"the boundary at n == 0 is not covered by any test",
		"2 fix round(s) did not clear these findings; escalating to a person",
	} {
		ev := Evidence{Key: "OR-1", Events: []events.Event{
			{Run: "1", Key: "OR-1", Kind: events.KindQA, Actor: events.ActorQA, Msg: msg},
		}}
		if v := Triage(ev, nil); !v.Done {
			t.Errorf("QA saying %q was read as QA not having said anything: %s", msg, v.Summary())
		}
	}
}

// SILENCE IS NOT EVIDENCE. The event log rotates by size, QA can be switched
// off for a project, and a ticket worked before this pass existed has no QA
// events at all. Reading an absence as a failure would hand back good work
// for want of a log file -- the same fault this pass exists to prevent, only
// pointing the other way.
func TestAnAbsentQALogIsNotAFinding(t *testing.T) {
	ev := Evidence{Key: "OR-1", Events: nil}
	if v := Triage(ev, nil); !v.Done {
		t.Errorf("a ticket with no QA events in the log was handed back: %s", v.Report())
	}
}

// A ticket retried after a crash appends a second run to the same log. Without
// scoping, the first run's "QA stopped" is found forever and every later
// attempt is handed back for a failure that has already been fixed.
func TestAnEarlierRunsQAFailureIsNotThisRuns(t *testing.T) {
	evs := []events.Event{
		{At: at("2026-08-30T09:00:00Z"), Run: "1", Key: "OR-1",
			Kind: events.KindQA, Actor: events.ActorOrion, Msg: "QA stopped: exit 1"},
		{At: at("2026-08-30T11:00:00Z"), Run: "2", Key: "OR-1",
			Kind: events.KindQA, Actor: events.ActorQA, Msg: "verified the change; every case passes"},
	}

	ev := Evidence{Key: "OR-1", Events: LastQARun(evs, "OR-1")}

	if v := Triage(ev, nil); !v.Done {
		t.Errorf("the retry was handed back for the first run's crash: %s", v.Report())
	}
}

// Another ticket's QA crash, in the same workspace log, is not this ticket's.
func TestAnotherTicketsQAFailureIsNotThisTickets(t *testing.T) {
	evs := []events.Event{
		{Run: "1", Key: "OR-9", Kind: events.KindQA, Actor: events.ActorOrion,
			Msg: "QA stopped: exit 1"},
		{Run: "2", Key: "OR-1", Kind: events.KindQA, Actor: events.ActorQA,
			Msg: "verified the change; every case passes"},
	}

	ev := Evidence{Key: "OR-1", Events: LastQARun(evs, "OR-1")}

	if v := Triage(ev, nil); !v.Done {
		t.Errorf("OR-1 was handed back for OR-9's crash: %s", v.Report())
	}
}

// A re-run that could not START proves nothing. Handing a ticket back because
// a checkout failed or no Go toolchain was on PATH would be a worse fault than
// the one this catches -- and it would be indistinguishable, to the person
// reading it, from a real failure.
func TestARerunThatCouldNotRunIsNotAFailure(t *testing.T) {
	for _, why := range []string{
		"this branch adds or changes no Go test file, and -count=2 is a `go test` flag",
		"no Go toolchain on PATH to re-run them with",
		"the re-run did not finish within 10m0s",
	} {
		ev := Evidence{Key: "OR-1", Rerun: Rerun{Skipped: why}}
		if v := Triage(ev, nil); !v.Done {
			t.Errorf("%q was reported as a test failure: %s", why, v.Summary())
		}
	}
}

// A test file the branch DOES carry is not stranded -- that is simply a
// committed test, which is the outcome everything here wants.
func TestACommittedTestIsNotStranded(t *testing.T) {
	ev := Evidence{Key: "OR-1", Diff: Diff{
		Files: []string{"internal/x/x.go", "internal/x/x_test.go"},
	}}
	if v := Triage(ev, nil); !v.Done {
		t.Errorf("a committed test was reported stranded: %s", v.Report())
	}
}

// ---------------------------------------------------------------------------
// The model half.
// ---------------------------------------------------------------------------

// The cheap path, and the honest one: a rule that has already found a stranded
// test has answered the question, and a second opinion on an answered question
// is spend with no decision attached to it.
func TestTheModelIsNotAskedWhenARuleHasAlreadyAnswered(t *testing.T) {
	ev := Evidence{Key: "OR-1", Diff: Diff{Stranded: []string{"x_test.go"}}}

	v := Triage(ev, neverAsked(t))

	if v.Judged {
		t.Error("the verdict claims a model was asked")
	}
	if v.Done {
		t.Error("the rule's finding was lost")
	}
}

func TestAModelSayingNotDoneCarriesItsReason(t *testing.T) {
	ask := func(Evidence) (string, error) {
		return "NOT DONE: the ticket asks for the --json flag and nothing in the " +
			"diff parses or emits JSON.", nil
	}

	v := Triage(Evidence{Key: "OR-1"}, ask)

	if v.Done {
		t.Fatal("the model said NOT DONE and the verdict says done")
	}
	if got := v.Findings[0].Check; got != CheckIntent {
		t.Errorf("check = %q, want %q", got, CheckIntent)
	}
	if !strings.Contains(strings.Join(v.Findings[0].Evidence, " "), "--json") {
		t.Errorf("the reason the model gave was dropped: %v", v.Findings[0].Evidence)
	}
	if !v.Judged {
		t.Error("the verdict does not record that a model was asked")
	}
}

// A reply nobody could parse carries no evidence, and the contract is NOT DONE
// WITH the specific evidence. So an unreadable answer leaves the intent
// question unanswered -- it does not hand work back on a formatting accident,
// which would cost a person a ticket and teach them the verdict is noise.
func TestAnUnreadableReplyDoesNotHandWorkBack(t *testing.T) {
	ask := func(Evidence) (string, error) {
		return "I had a look and, on balance, it seems broadly fine to me.", nil
	}

	v := Triage(Evidence{Key: "OR-1"}, ask)

	if !v.Done {
		t.Fatalf("work was handed back on a reply with no reason in it: %s", v.Report())
	}
	// Silence about it would be the real fault: a pass whose model half did
	// not answer reads exactly like one where it answered "done".
	if !strings.Contains(v.Note, "unanswered") {
		t.Errorf("the verdict does not say the intent question went unanswered: %q", v.Note)
	}
}

// Same reasoning for a bare "NOT DONE:" with nothing after it.
func TestANotDoneWithNoReasonIsNotAHandBack(t *testing.T) {
	v := Triage(Evidence{Key: "OR-1"}, func(Evidence) (string, error) { return "NOT DONE:", nil })
	if !v.Done {
		t.Errorf("work was handed back with no reason attached: %s", v.Report())
	}
}

// Degrade, don't fail: the model being unreachable must not become a verdict
// about the change. The mechanical checks stand on their own.
func TestAModelThatCouldNotBeReachedDoesNotHandWorkBack(t *testing.T) {
	ask := func(Evidence) (string, error) { return "", errors.New("claude CLI not found on PATH") }

	v := Triage(Evidence{Key: "OR-1"}, ask)

	if !v.Done {
		t.Fatalf("an unreachable model was read as a defect in the change: %s", v.Report())
	}
	if !strings.Contains(v.Note, "claude CLI not found") {
		t.Errorf("the verdict hides why the model was not asked: %q", v.Note)
	}
}

// No model configured is a supported configuration, not a degraded one: the
// rules are the part with recorded evidence behind them, and all three of the
// 2026-08-30 cases are theirs.
func TestNoModelStillRunsTheRules(t *testing.T) {
	ev := Evidence{Key: "OR-1", Rerun: Rerun{Failed: true, Packages: []string{"./x"}}}
	if v := Triage(ev, nil); v.Done {
		t.Errorf("with no model configured the rules stopped running: %s", v.Report())
	}
}

func TestAModelSayingDoneIsDone(t *testing.T) {
	v := Triage(Evidence{Key: "OR-1"}, func(Evidence) (string, error) {
		return "DONE", nil
	})
	if !v.Done {
		t.Errorf("the model said DONE and the verdict does not: %s", v.Report())
	}
}

// A model that names a missing criterion and then writes DONE has still named
// one, and the reason is the part a person can check.
func TestNotDoneWinsWhenAReplyContainsBoth(t *testing.T) {
	f, ok := ParseReply("NOT DONE: the --json flag is missing.\nDONE")
	if !ok || f == nil {
		t.Fatalf("ParseReply = (%v, %v), want the NOT DONE finding", f, ok)
	}
}

// ---------------------------------------------------------------------------
// The shape of the answer.
// ---------------------------------------------------------------------------

// Every finding, and every line of evidence under it, reaches the report. The
// report is what goes on the ticket, so anything dropped here is a reason
// nobody can act on.
func TestTheReportCarriesEveryFindingAndItsEvidence(t *testing.T) {
	ev := Evidence{
		Key:   "OR-1",
		Diff:  Diff{Stranded: []string{"internal/x/x_test.go (in the worktree, not in the diff)"}},
		Rerun: Rerun{Failed: true, Packages: []string{"./internal/x"}, Output: "--- FAIL: TestRace"},
	}

	report := Triage(ev, nil).Report()

	for _, want := range []string{
		"NOT DONE", CheckStranded, "x_test.go", CheckRerun, "-count=2", "TestRace",
	} {
		if !strings.Contains(report, want) {
			t.Errorf("the report does not mention %q:\n%s", want, report)
		}
	}
}

// Findings is empty exactly when Done. Anything else would let a caller ask
// for the verdict and the reasons and get two different answers.
func TestFindingsAndDoneNeverDisagree(t *testing.T) {
	cases := []Evidence{
		{Key: "OR-1"},
		{Key: "OR-1", Diff: Diff{Stranded: []string{"a_test.go"}}},
		{Key: "OR-1", Rerun: Rerun{Failed: true}},
	}
	for _, ev := range cases {
		v := Triage(ev, nil)
		if v.Done != (len(v.Findings) == 0) {
			t.Errorf("Done=%v with %d finding(s)", v.Done, len(v.Findings))
		}
	}
}

// Every rule that fired is reported, not just the first. A person handed one
// reason will fix it and come straight back for the second.
func TestEveryRuleThatFiredIsReported(t *testing.T) {
	ev := Evidence{
		Key: "OR-1",
		Events: []events.Event{{Run: "1", Key: "OR-1", Kind: events.KindQA,
			Actor: events.ActorOrion, Msg: "QA stopped: exit 1"}},
		Diff:  Diff{Stranded: []string{"a_test.go"}},
		Rerun: Rerun{Failed: true},
	}

	if got := len(Triage(ev, nil).Findings); got != 3 {
		t.Errorf("%d findings, want all 3 rules reported", got)
	}
}
