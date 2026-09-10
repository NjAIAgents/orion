package work

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/orion-sdlc/orion/internal/events"
	"github.com/orion-sdlc/orion/internal/supervisor"
	"github.com/orion-sdlc/orion/internal/ui"
)

// The case OR-429 is about: interrupted after implementing, during QA. The
// implementation is on the branch and must not be written again.
func TestAResumeAfterImplementingRestartsAtQA(t *testing.T) {
	dir := stageLog(t,
		crossed("OR-300", "routing", "implementing"),
		crossed("OR-300", "implementing", "qa"),
	)
	got := resumeAt(dir, "OR-300")
	if got.Stage != "qa" {
		t.Fatalf("expected to resume at qa, got %q (%s)", got.Stage, got.Why)
	}
	if got.Actor != events.ActorQA {
		t.Errorf("qa must be held by the QA actor, got %q", got.Actor)
	}
	if got.Why == "" {
		t.Error("a skipped implementation with no stated reason is indistinguishable " +
			"from an implementer that did nothing")
	}
}

// The data-model stage is reached BEFORE QA, and resuming into it is the same
// kind of saving.
func TestAResumeAtTheDataModelStageIsRecognised(t *testing.T) {
	dir := stageLog(t,
		crossed("OR-301", "implementing", "dba"),
	)
	if got := resumeAt(dir, "OR-301"); got.Stage != "dba" || got.Actor != events.ActorDBA {
		t.Errorf("expected dba/%s, got %q/%q", events.ActorDBA, got.Stage, got.Actor)
	}
}

// THE DANGEROUS DIRECTION. Interrupted while still implementing means the
// implementation is unfinished, and skipping it would hand QA a tree with
// nothing in it to verify.
func TestAnInterruptionDuringImplementationStartsOver(t *testing.T) {
	dir := stageLog(t, crossed("OR-302", "routing", "implementing"))
	if got := resumeAt(dir, "OR-302"); got.Stage != "" {
		t.Errorf("a run interrupted while implementing must re-implement, got %q", got.Stage)
	}
}

// A fix round is QA's own loop, not a stage. A run that died mid-fix has an
// implementation QA has already READ AND REJECTED -- re-entering at QA would
// ask it to verify the very tree it just failed.
func TestAnInterruptedFixRoundStartsOver(t *testing.T) {
	dir := stageLog(t,
		crossed("OR-303", "implementing", "qa"),
		crossed("OR-303", "qa", "fix round 2"),
	)
	if got := resumeAt(dir, "OR-303"); got.Stage != "" {
		t.Errorf("a fix round must re-run the implementer, got %q", got.Stage)
	}
}

// Past push, internal/work no longer owns the ticket -- collect does. There
// is nothing here to resume into.
func TestAStagePastPushIsNotResumedInto(t *testing.T) {
	for _, to := range []string{"push", "ready", "pull request", "ci"} {
		dir := stageLog(t, crossed("OR-304", "qa", to))
		if got := resumeAt(dir, "OR-304"); got.Stage != "" {
			t.Errorf("%q is past the coding pipeline, got resume at %q", to, got.Stage)
		}
	}
}

// A stage name this code has never been taught must read as "start over".
// The alternative is resuming into a stage nobody holds.
func TestAnUnknownStageStartsOver(t *testing.T) {
	dir := stageLog(t, crossed("OR-305", "implementing", "security-review"))
	if got := resumeAt(dir, "OR-305"); got.Stage != "" {
		t.Errorf("an unrecognised stage must start over, got %q", got.Stage)
	}
}

// One ticket's history must never answer for another's: the log is shared by
// every ticket in the project.
func TestAnotherTicketsStagesAreIgnored(t *testing.T) {
	dir := stageLog(t, crossed("OR-306", "implementing", "qa"))
	if got := resumeAt(dir, "OR-307"); got.Stage != "" {
		t.Errorf("OR-307 resumed on OR-306's history, at %q", got.Stage)
	}
}

// No log at all is not evidence of anything.
func TestAMissingLogStartsOver(t *testing.T) {
	if got := resumeAt(t.TempDir(), "OR-308"); got.Stage != "" {
		t.Errorf("with no log to read, a full run is the safe reading; got %q", got.Stage)
	}
}

// The skipped implementer leaves no session, and two call sites read one off
// it. Empty rather than a panic, and empty degrades: supervisor omits
// --resume when it is blank.
func TestTheSessionOfNoRunIsEmptyRatherThanAPanic(t *testing.T) {
	if got := sessionOf(nil); got != "" {
		t.Errorf("a run that never happened has no session, got %q", got)
	}
	if got := sessionOf(&supervisor.Result{SessionID: "abc"}); got != "abc" {
		t.Errorf("a real run's session must come through, got %q", got)
	}
}

// The last boundary wins. A ticket that reached QA, was sent back, and
// reached QA again is at QA -- and one sent back and interrupted there is
// not.
func TestTheLatestBoundaryDecides(t *testing.T) {
	dir := stageLog(t,
		crossed("OR-309", "implementing", "qa"),
		crossed("OR-309", "qa", "fix round 1"),
		crossed("OR-309", "fix round 1", "qa"),
	)
	if got := resumeAt(dir, "OR-309"); got.Stage != "qa" {
		t.Errorf("the run was back at qa when it died, got %q", got.Stage)
	}
}

// --- helpers ---

func stageLog(t *testing.T, evs ...events.Event) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Dir(events.Path(dir)), 0o755); err != nil {
		t.Fatal(err)
	}
	log, err := events.Open(events.Path(dir), events.Event{})
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range evs {
		log.Emit(e)
	}
	if err := log.Close(); err != nil {
		t.Fatal(err)
	}
	return dir
}

// crossed builds the event ui.Stage emits, via HandoffOf's own detail keys,
// so the fixture cannot drift from what the pipeline actually writes.
func crossed(key, from, to string) events.Event {
	var e events.Event
	e.Kind, e.Key, e.Actor = events.KindStage, key, events.ActorOrion
	e.Msg = from + " -> " + to
	e.Detail = map[string]any{"from": from, "to": to}
	// Proves the fixture round-trips through the same reader the code uses.
	if h := ui.HandoffOf(e); h.To != to || h.From != from {
		panic("fixture does not round-trip through ui.HandoffOf")
	}
	return e
}
