package ui

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/orion-sdlc/orion/internal/actors"
	"github.com/orion-sdlc/orion/internal/events"
)

func renderStage(h Handoff) string {
	var b bytes.Buffer
	return RenderStage(&b, h)
}

func openTestLog(t *testing.T) *events.Log {
	t.Helper()
	log, err := events.Open(filepath.Join(t.TempDir(), "events.jsonl"), events.Event{})
	if err != nil {
		t.Fatalf("opening the event log: %v", err)
	}
	t.Cleanup(func() { _ = log.Close() })
	return log
}

func readTestLog(t *testing.T, log *events.Log) []events.Event {
	t.Helper()
	got, err := events.Read(log.Path())
	if err != nil {
		t.Fatalf("reading the event log: %v", err)
	}
	return got
}

// The two lines OR-189 was filed on, side by side. A boundary and an ordinary
// status line must not be mistakable for each other -- the whole complaint was
// that "2 commit(s) on orion/or-183" followed by "verifying with ..." looked
// like two status lines and said nothing about the run crossing into QA.
func TestABoundaryRendersDistinctlyFromAFiveVerbLine(t *testing.T) {
	at := time.Date(2026, 8, 29, 13, 34, 52, 0, time.Local)
	status := render(Line{At: at, Key: "OR-183", Actor: events.ActorImplementer,
		Verb: VerbOK, Msg: "2 commit(s) on orion/or-183"})
	boundary := renderStage(Handoff{At: at, Key: "OR-183",
		From: "implementing", To: "qa",
		By: events.ActorImplementer, Next: events.ActorQA,
		Detail: "2 commit(s) on orion/or-183"})

	if !strings.Contains(boundary, stageWord) {
		t.Errorf("a boundary must say what it is, in words:\n%s", boundary)
	}
	// The distinction is LAYOUT, not vocabulary: the boundary drops the
	// icon/verb columns entirely rather than borrowing a sixth verb.
	for _, verb := range []string{VerbOK, VerbWorking, VerbWaiting, VerbWarn, VerbFail} {
		if strings.Contains(boundary, iconFor(verb)) {
			t.Errorf("a boundary must not wear the status icon for %q:\n%s", verb, boundary)
		}
	}
	if strings.Contains(status, stageWord) {
		t.Errorf("an ordinary status line must not read as a boundary:\n%s", status)
	}
	// Both still lead with the time and the ticket thread, so a reader
	// following one key down the page does not lose their place.
	for _, line := range []string{status, boundary} {
		if !strings.HasPrefix(line, "13:34:52 OR-183") {
			t.Errorf("the time and key columns moved: %q", line)
		}
	}
}

// The five verbs are a closed vocabulary and this change must not have grown
// it: the verb column answers "do I have to do something", which has five
// answers, and a handoff asks nothing of the operator.
func TestTheStatusVocabularyIsUnchanged(t *testing.T) {
	if got := VerbFor(events.KindStage); got != VerbOK {
		t.Errorf("a stage boundary must not have its own verb, got %q", got)
	}
	verbs := map[string]bool{VerbOK: true, VerbWorking: true,
		VerbWaiting: true, VerbWarn: true, VerbFail: true}
	for _, kind := range []string{
		events.KindClaimed, events.KindBranch, events.KindRunStart, events.KindRunEnd,
		events.KindAsk, events.KindAnswer, events.KindRefuse, events.KindEscalate,
		events.KindDecision, events.KindCommit, events.KindPush, events.KindPR,
		events.KindCI, events.KindQA, events.KindMerge, events.KindRefresh,
		events.KindBlocked, events.KindFailed, events.KindBudget, events.KindUsage,
		events.KindTool, events.KindSay, events.KindStage, events.KindNote,
	} {
		if !verbs[VerbFor(kind)] {
			t.Errorf("%s produced the verb %q, which is not one of the five",
				kind, VerbFor(kind))
		}
	}
}

// Words carry the meaning, colour and glyphs only reinforce (OR-163). A
// boundary recognisable only by its box-drawing rule is invisible on a
// non-UTF-8 terminal -- so the transition has to survive the degradation.
func TestABoundaryDegradesToASCIIWithTheTransitionLegible(t *testing.T) {
	h := Handoff{Key: "OR-183", From: "implementing", To: "qa",
		By: events.ActorImplementer, Next: events.ActorQA}

	// All three, in POSIX precedence order. utf8Locale reads LC_ALL, then
	// LC_CTYPE, then LANG, and returns on the FIRST one that is set, so
	// setting only LANG leaves the result at the mercy of the runner's
	// environment. A macOS CI runner exports LC_CTYPE, which made this
	// pass locally and on Linux while failing there.
	t.Setenv("LC_ALL", "C")
	t.Setenv("LC_CTYPE", "C")
	t.Setenv("LANG", "C")
	ascii := renderStage(h)
	if strings.ContainsAny(ascii, stageRuleGlyph+stageArrowGlyph) {
		t.Errorf("a non-UTF-8 terminal was sent glyphs: %q", ascii)
	}
	for _, want := range []string{
		stageWord, "implementing", "qa", stageArrowASCII,
		actors.Display(events.ActorImplementer), actors.Display(events.ActorQA),
	} {
		if !strings.Contains(ascii, want) {
			t.Errorf("the ASCII boundary lost %q:\n%s", want, ascii)
		}
	}

	t.Setenv("LC_ALL", "en_GB.UTF-8")
	t.Setenv("LC_CTYPE", "en_GB.UTF-8")
	t.Setenv("LANG", "en_GB.UTF-8")
	glyph := renderStage(h)
	// The same facts, in the same words. Only the punctuation changes.
	for _, want := range []string{stageWord, "implementing", "qa"} {
		if !strings.Contains(glyph, want) {
			t.Errorf("the glyph boundary lost %q:\n%s", want, glyph)
		}
	}
}

// Both sides, named. "Mahesh finished, Sana is starting" is the fact a reader
// wants; two adjacent lines with different names leaves it as an exercise.
func TestABoundaryNamesBothSides(t *testing.T) {
	got := renderStage(Handoff{Key: "OR-183", From: "implementing", To: "qa",
		By: events.ActorImplementer, Next: events.ActorQA,
		Detail: "2 commit(s) on orion/or-183"})

	for _, want := range []string{
		actors.Display(events.ActorImplementer), actors.Display(events.ActorQA),
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the boundary does not name %q:\n%s", want, got)
		}
	}
	// The commit count did not disappear when it stopped being a line of its
	// own; it is detail ON the boundary now.
	if !strings.Contains(got, "2 commit(s) on orion/or-183") {
		t.Errorf("the boundary dropped its detail:\n%s", got)
	}
}

// THE ONE MOST EASILY GOT WRONG. At "opened the pull request, awaiting CI" the
// next party is a machine and at "checks pass" it is a person. Naming an agent
// at either would have the operator watching it apparently work for the length
// of a CI run or an overnight approval -- an agent that is not running and is
// costing nothing.
func TestABoundaryHandingToAMachineOrAPersonNamesNoAgent(t *testing.T) {
	agents := []string{
		events.ActorImplementer, events.ActorFrontend, events.ActorDocs,
		events.ActorArchitect, events.ActorPM, events.ActorDevOps,
		events.ActorQA, events.ActorDescriber, events.ActorLogTriage,
		events.ActorExplore, events.ActorRouter,
	}
	for _, c := range []struct {
		name string
		h    Handoff
	}{
		{"ci", Handoff{Key: "OR-183", From: "pull request", To: "ci",
			By: events.ActorOrion, Next: events.ActorCI, Detail: "the job slot is free"}},
		{"human", Handoff{Key: "OR-183", From: "ci", To: "approval",
			By: events.ActorCI, Next: events.ActorHuman, Detail: "checks pass"}},
	} {
		got := renderStage(c.h)
		for _, id := range agents {
			if strings.Contains(got, actors.Display(id)) {
				t.Errorf("the %s boundary names an agent that is not running (%s):\n%s",
					c.name, id, got)
			}
		}
		// And it says so, in words rather than by omission.
		if !strings.Contains(got, "no agent is running") {
			t.Errorf("the %s boundary does not say no agent is running:\n%s", c.name, got)
		}
	}

	// The counterpart: a red build DOES hand to an agent, and that boundary
	// must not carry the disclaimer.
	red := renderStage(Handoff{Key: "OR-183", From: "ci", To: "fix",
		By: events.ActorCI, Next: events.ActorDevOps, Detail: "attempt 1 of 3"})
	if strings.Contains(red, "no agent is running") {
		t.Errorf("CI-red hands to an agent, which IS running:\n%s", red)
	}
	if !strings.Contains(red, actors.Display(events.ActorDevOps)) {
		t.Errorf("the CI-red boundary does not name who picks it up:\n%s", red)
	}
}

// One party holding both sides is a real case -- Orion pushes a branch and
// then opens the pull request for it -- and "orion hands to orion" is a
// sentence nobody should have to read.
func TestABoundaryWithOnePartyOnBothSidesSaysItContinues(t *testing.T) {
	got := renderStage(Handoff{Key: "OR-183", From: "push", To: "pull request",
		By: events.ActorOrion, Next: events.ActorOrion})
	if !strings.Contains(got, actors.Display(events.ActorOrion)+" continues") {
		t.Errorf("expected a continues clause:\n%s", got)
	}
	if strings.Contains(got, "hands to") {
		t.Errorf("one party cannot hand to itself:\n%s", got)
	}
}

// The event log is what answers "how long did this run spend in QA" and "how
// long did it sit waiting for a human" -- the second being the number OR-184's
// case rests on, and neither being queryable before a boundary had a kind.
func TestABoundaryIsRecordedAsItsOwnEventKind(t *testing.T) {
	var buf bytes.Buffer
	log := openTestLog(t)
	Stage(&buf, log, Handoff{Key: "OR-183", From: "ci", To: "approval",
		By: events.ActorCI, Next: events.ActorHuman, Detail: "checks pass"})

	got := readTestLog(t, log)
	if len(got) != 1 {
		t.Fatalf("expected one event, got %d", len(got))
	}
	e := got[0]
	if e.Kind != events.KindStage {
		t.Errorf("a boundary must have its own kind, got %q", e.Kind)
	}
	if e.Key != "OR-183" {
		t.Errorf("the boundary event lost its ticket: %q", e.Key)
	}
	if e.Actor != events.ActorHuman {
		t.Errorf("the boundary event must be attributed to whoever now holds "+
			"the run, got %q", e.Actor)
	}
	for k, want := range map[string]string{
		"from": "ci", "to": "approval",
		"by": events.ActorCI, "next": events.ActorHuman, "detail": "checks pass",
	} {
		if got, _ := e.Detail[k].(string); got != want {
			t.Errorf("detail %q = %q, want %q", k, got, want)
		}
	}
	// Identifiers, never display names: internal/actors resolves those at
	// render time, so a log written today renders with tomorrow's roster.
	for _, id := range []string{events.ActorQA, events.ActorImplementer} {
		if strings.Contains(e.Msg, actors.Display(id)) {
			t.Errorf("a display name was written into the log: %q", e.Msg)
		}
	}
	// And the recorded form re-renders to the same line the run printed.
	if replayed := renderStage(HandoffOf(e)); !strings.Contains(replayed, "no agent is running") {
		t.Errorf("replaying the recorded boundary lost its meaning:\n%s", replayed)
	}
}
