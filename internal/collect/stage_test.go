package collect

import (
	"strings"
	"testing"

	"github.com/orion-sdlc/orion/internal/actors"
	"github.com/orion-sdlc/orion/internal/events"
	"github.com/orion-sdlc/orion/internal/ui"
)

func stageEvents(t *testing.T, wsDir string) []events.Event {
	t.Helper()
	evs, err := events.Read(events.Path(wsDir))
	if err != nil {
		t.Fatalf("reading the event log: %v", err)
	}
	var out []events.Event
	for _, e := range evs {
		if e.Kind == events.KindStage {
			out = append(out, e)
		}
	}
	return out
}

// The third boundary after a pull request opens, and the only one where the
// next party really is an agent. It was completely unmarked: the build went
// red and devops picked it up with nothing in the log saying the run had
// changed hands (OR-189).
func TestARedBuildMarksTheHandoffToDevops(t *testing.T) {
	home, wsDir := ciRepo(t, 3)
	_, out := runFix(t, home, &fixSpy{pushed: true}, "test (failure)\nassert 1 == 2", Options{})

	evs := stageEvents(t, wsDir)
	if len(evs) != 1 {
		t.Fatalf("expected one boundary, got %d\n%s", len(evs), out)
	}
	h := ui.HandoffOf(evs[0])
	if h.From != "ci" || h.To != "fix" {
		t.Errorf("crossing = %s -> %s, want ci -> fix", h.From, h.To)
	}
	if h.Next != events.ActorDevOps {
		t.Errorf("the fix is picked up by %q, want devops", h.Next)
	}

	line := ui.RenderStage(&strings.Builder{}, h)
	if !strings.Contains(out, line) {
		t.Errorf("the boundary was recorded but not printed as %q:\n%s", line, out)
	}
	// An agent IS running here, and money is being spent -- so this boundary
	// must NOT carry the disclaimer the CI and approval boundaries do.
	if strings.Contains(line, "no agent is running") {
		t.Errorf("a fix round is an agent spending money: %q", line)
	}
	if !strings.Contains(line, actors.Display(events.ActorDevOps)) {
		t.Errorf("the boundary does not name who picks the build up: %q", line)
	}
	if !strings.Contains(line, "attempt 1 of 3") {
		t.Errorf("the attempt count must ride on the boundary: %q", line)
	}
}

// ONE LOGGER, NOT TWO. The fix loop lives in a different package from
// internal/work, and OR-176 was exactly a hand-rolled second copy of a shared
// logger in this position -- one that printed unattributed lines and emitted
// nothing to the event log. A boundary printed here must be reproducible from
// its own recorded event by the same internal/ui renderer internal/work uses,
// which is what makes the two packages' boundary lines identical.
func TestTheFixLoopBoundaryGoesThroughTheSharedHelper(t *testing.T) {
	home, wsDir := ciRepo(t, 3)
	_, out := runFix(t, home, &fixSpy{pushed: true}, "boom", Options{})

	evs := stageEvents(t, wsDir)
	if len(evs) == 0 {
		t.Fatalf("the fix loop recorded no boundary at all:\n%s", out)
	}
	for _, e := range evs {
		want := ui.RenderStage(&strings.Builder{}, ui.HandoffOf(e))
		if !strings.Contains(out, want) {
			t.Errorf("a boundary was not printed by the shared renderer\nwant: %q\nin:\n%s",
				want, out)
		}
	}
}

// The queue's actual bottleneck, made visible as one. Green checks hand the
// run to a PERSON -- not to the devops agent, which only wakes for a red
// build -- and the gap between this event and the merge is the waiting time
// that could not be queried before.
func TestGreenChecksHandTheRunToAPersonAndNameNoAgent(t *testing.T) {
	home, _ := approvalRepo(t, `"navjyot"`)
	_, out := runApproval(t, home, &slackSpy{}, &mergeSpy{},
		PR{Verdict: VerdictPassing, URL: "https://pr/1"}, Options{})

	var line string
	for _, l := range strings.Split(out, "\n") {
		if strings.Contains(l, "approval") && strings.Contains(l, "stage") {
			line = l
		}
	}
	if line == "" {
		t.Fatalf("no ci -> approval boundary:\n%s", out)
	}
	if !strings.Contains(line, "no agent is running") {
		t.Errorf("waiting for a person costs nothing and must say so: %q", line)
	}
	for _, id := range []string{
		events.ActorImplementer, events.ActorQA, events.ActorDevOps, events.ActorDescriber,
	} {
		if strings.Contains(line, actors.Display(id)) {
			t.Errorf("the approval boundary names %s, which is not running: %q", id, line)
		}
	}
	// It names the person's side rather than leaving it blank.
	if !strings.Contains(line, actors.Display(events.ActorHuman)) {
		t.Errorf("the approval boundary does not name who is next: %q", line)
	}
}
