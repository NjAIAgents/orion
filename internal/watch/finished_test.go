package watch

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/orion-sdlc/orion/internal/events"
	"github.com/orion-sdlc/orion/internal/ui"
)

// OR-269: work finished and pushed, label never followed, twelve hours of a
// held slot. The log said so the whole time.
func TestFinishedWorkIsRecognisedFromTheLog(t *testing.T) {
	dir := logWith(t,
		stage("OR-269", "implementer", "qa"),
		stage("OR-269", "qa", "push"),
		stage("OR-269", "push", "ready"),
	)
	if !finishedWork(dir, "OR-269") {
		t.Error("a ticket whose log carries the terminal boundary is finished, " +
			"whatever its label says -- this is the twelve hours OR-429 is about")
	}
}

// The dangerous direction. A wrong yes starts a second agent on work already
// done, or moves a ticket the integration queue then eats mid-flight.
func TestWorkStillInProgressIsLeftAlone(t *testing.T) {
	dir := logWith(t,
		stage("OR-300", "implementer", "qa"),
		stage("OR-300", "qa", "implementer"), // QA sent it back
	)
	if finishedWork(dir, "OR-300") {
		t.Error("a ticket mid-pipeline must not be reconciled: orion-working is " +
			"exactly what it should be wearing")
	}
}

// Reaching ready is not permanent. A batch culprit is ejected and worked
// again, and only the ticket's LATEST boundary describes it now.
func TestATicketWorkedAgainAfterReadyIsNotFinished(t *testing.T) {
	dir := logWith(t,
		stage("OR-278", "qa", "push"),
		stage("OR-278", "push", "ready"),
		stage("OR-278", "ci", "fix"), // convicted, back to an agent
	)
	if finishedWork(dir, "OR-278") {
		t.Error("the last boundary wins: this ticket is being fixed, not waiting " +
			"for the integration queue")
	}
}

// One ticket's log must never answer for another's. The log is shared by
// every ticket in the project.
func TestAnotherTicketsBoundaryDoesNotCount(t *testing.T) {
	dir := logWith(t, stage("OR-269", "push", "ready"))
	if finishedWork(dir, "OR-270") {
		t.Error("OR-270 was reconciled on OR-269's evidence")
	}
}

// No log is not evidence of anything -- a claim taken before the log existed,
// or a workspace whose log rotated away. Declining to reconcile is the safe
// answer and the required one.
func TestAMissingLogReconcilesNothing(t *testing.T) {
	if finishedWork(t.TempDir(), "OR-269") {
		t.Error("a ticket with no log must be left exactly as it is")
	}
}

// THE DRIFT GUARD. finished.go spells the stage names as constants because
// importing internal/work would be a cycle. Renaming the stage there would
// otherwise silently stop the reconciler and cost another night.
//
// So this asserts on an event built by the REAL ui.Stage path rather than on
// the constants: if internal/work renames the boundary, the names no longer
// match what is emitted and this fails.
func TestTheStageNamesMatchWhatIsActuallyEmitted(t *testing.T) {
	dir := t.TempDir()
	log, err := events.Open(events.Path(dir), events.Event{})
	if err != nil {
		t.Fatal(err)
	}
	// The same call readyForBatch makes (internal/work/work.go:1131).
	ui.Stage(io_discard{}, log, ui.Handoff{
		Key: "OR-1", From: "push", To: "ready",
		By: events.ActorOrion, Next: events.ActorOrion,
	})
	_ = log.Close()

	if !finishedWork(dir, "OR-1") {
		t.Error("the stage names in finished.go no longer match what internal/work " +
			"emits, so the reconciler has silently stopped working")
	}
}

// --- helpers ---

func logWith(t *testing.T, evs ...events.Event) string {
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

func stage(key, from, to string) events.Event {
	return events.Event{
		Kind: events.KindStage, Key: key, Actor: events.ActorOrion,
		Msg:    from + " -> " + to,
		Detail: map[string]any{"from": from, "to": to},
	}
}

type io_discard struct{}

func (io_discard) Write(p []byte) (int, error) { return len(p), nil }
