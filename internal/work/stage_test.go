package work

import (
	"strings"
	"testing"

	"github.com/orion-sdlc/orion/internal/actors"
	"github.com/orion-sdlc/orion/internal/events"
	"github.com/orion-sdlc/orion/internal/ui"
)

// stageEvents returns the boundaries a run recorded, in order.
func stageEvents(t *testing.T, home string) []events.Event {
	t.Helper()
	evs, err := events.Read(findEventLog(t, home))
	if err != nil {
		t.Fatal(err)
	}
	var out []events.Event
	for _, e := range evs {
		if e.Kind == events.KindStage {
			out = append(out, e)
		}
	}
	return out
}

// transitions reads the crossings out of the recorded events rather than off
// the console, so the assertion does not depend on which punctuation the
// locale earned.
func transitions(evs []events.Event) []string {
	var out []string
	for _, e := range evs {
		h := ui.HandoffOf(e)
		out = append(out, h.From+" -> "+h.To)
	}
	return out
}

// boundaryLine returns the console line one crossing printed.
//
// Found via the recorded event rather than by substring: a branch name like
// orion/fcia-6 contains "ci", so matching stage names loosely picks the wrong
// line. Going through the event also means this only finds a line the shared
// renderer can reproduce.
func boundaryLine(t *testing.T, out, home, from, to string) string {
	t.Helper()
	for _, e := range stageEvents(t, home) {
		h := ui.HandoffOf(e)
		if h.From != from || h.To != to {
			continue
		}
		want := ui.RenderStage(&strings.Builder{}, h)
		for _, l := range strings.Split(out, "\n") {
			if l == want {
				return l
			}
		}
		t.Fatalf("the %s -> %s boundary was recorded but not printed as %q:\n%s",
			from, to, want, out)
	}
	t.Fatalf("no %s -> %s boundary in:\n%s", from, to, out)
	return ""
}

func runToPR(t *testing.T, home string, f *qaFake) string {
	t.Helper()
	var out strings.Builder
	Run(Options{Keys: []string{"FCIA-6"}, Out: &out, Home: home},
		Deps{
			Jira:      &fakeJira{},
			Supervise: f.run,
			Push:      func(string, string) error { return nil },
			OpenPR: func(string, string, string, string, string) (string, error) {
				return "https://pr/1", nil
			},
		})
	return out.String()
}

// The run OR-189 was observed on, end to end. Every crossing a ticket makes
// between being routed and its pull request opening is marked, rather than
// leaving the reader to infer a handoff from the actor names changing between
// two ordinary status lines printed in the same second.
func TestAWholeRunMarksEveryStageBoundary(t *testing.T) {
	home := project(t, qaCfg)
	out := runToPR(t, home, &qaFake{t: t, qaReplies: []string{
		"The rounding case is wrong.",
		"QA CLEAN",
	}})

	got := transitions(stageEvents(t, home))
	want := []string{
		"routing -> implementing",
		"implementing -> qa",
		"qa -> fix round 1",
		// The return leg matters as much as the departure: without it the log
		// shows a run entering a fix round and never leaving, and the time
		// spent in QA stops being derivable from the boundaries at all.
		"fix round 1 -> qa",
		"qa -> push",
		"push -> pull request",
		"pull request -> ci",
	}
	if len(got) != len(want) {
		t.Fatalf("boundaries:\n got %v\nwant %v\n\noutput:\n%s", got, want, out)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("boundary %d = %q, want %q", i, got[i], want[i])
		}
	}
}

// The commit count did not disappear when it stopped being a line of its own.
// It used to be the last thing printed before QA started -- answering "how
// many commits" while the reader was asking what stage the run had reached --
// and it is detail ON the boundary now.
func TestTheCommitCountRidesOnTheBoundaryOutOfImplementation(t *testing.T) {
	home := project(t, qaCfg)
	out := runToPR(t, home, &qaFake{t: t, qaReplies: []string{"QA CLEAN"}})

	line := boundaryLine(t, out, home, "implementing", "qa")
	if !strings.Contains(line, "commit(s) on orion/fcia-6") {
		t.Errorf("the boundary does not carry the commit count: %q", line)
	}
	// Both sides named on the one line, which is the fact the reader wanted.
	for _, id := range []string{events.ActorImplementer, events.ActorQA} {
		if !strings.Contains(line, actors.Display(id)) {
			t.Errorf("the boundary does not name %s: %q", id, line)
		}
	}
}

// With QA switched off there is no QA stage to enter, and a boundary
// announcing one would name an actor that never runs -- the same defect as an
// unmarked handoff, in the other direction.
func TestABoundaryNeverAnnouncesAStageThatDoesNotRun(t *testing.T) {
	home := project(t, `{"vcs":{"default_branch":"main","work_branch":"develop","branch_prefix":"orion/"},
	                     "tracker":{"enabled":true,"project_key":"FCIA","queue_label":"ORION"},
	                     "qa":{"enabled":false}}`)
	out := runToPR(t, home, &qaFake{t: t})

	for _, tr := range transitions(stageEvents(t, home)) {
		if strings.Contains(tr, "qa") {
			t.Errorf("QA is off, so %q is a handoff to an actor that never ran", tr)
		}
	}
	// Implementation still hands off somewhere, or the commit count would
	// have gone missing along with the stage.
	line := boundaryLine(t, out, home, "implementing", "push")
	if !strings.Contains(line, "commit(s) on orion/fcia-6") {
		t.Errorf("the boundary does not carry the commit count: %q", line)
	}
}

// Waiting for CI is the boundary most easily got wrong: the next party is a
// machine, and naming devops there would have the operator watching an agent
// apparently work for the length of the CI run.
func TestTheBoundaryIntoCINamesNoAgent(t *testing.T) {
	home := project(t, qaCfg)
	out := runToPR(t, home, &qaFake{t: t, qaReplies: []string{"QA CLEAN"}})

	line := boundaryLine(t, out, home, "pull request", "ci")
	if !strings.Contains(line, "no agent is running") {
		t.Errorf("the CI boundary does not say no agent is running: %q", line)
	}
	for _, id := range []string{
		events.ActorImplementer, events.ActorQA, events.ActorDevOps, events.ActorDescriber,
	} {
		if strings.Contains(line, actors.Display(id)) {
			t.Errorf("the CI boundary names %s, which is not running: %q", id, line)
		}
	}
}

// ONE LOGGER, NOT TWO. A boundary printed by internal/work must be
// reproducible from its own recorded event through internal/ui -- which it
// only is if it went through the shared helper. A hand-rolled second copy in
// this package (OR-176's failure) would print a line the renderer cannot
// reproduce, or emit no event at all. internal/collect asserts the same thing
// about the fix loop's boundary, which is what makes the two identical.
func TestWorkBoundariesGoThroughTheSharedHelper(t *testing.T) {
	home := project(t, qaCfg)
	out := runToPR(t, home, &qaFake{t: t, qaReplies: []string{"QA CLEAN"}})

	evs := stageEvents(t, home)
	if len(evs) == 0 {
		t.Fatalf("no boundary reached the event log:\n%s", out)
	}
	for _, e := range evs {
		want := ui.RenderStage(&strings.Builder{}, ui.HandoffOf(e))
		if !strings.Contains(out, want) {
			t.Errorf("a boundary was not printed by the shared renderer\nwant: %q\nin:\n%s",
				want, out)
		}
	}
}
