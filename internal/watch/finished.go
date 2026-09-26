package watch

import (
	"github.com/orion-sdlc/orion/internal/events"
	"github.com/orion-sdlc/orion/internal/ui"
)

// finishedWork reports whether this ticket's own event log says its work
// reached the end of the coding pipeline.
//
// THE GAP THIS FILLS. InFlight already reconciles a claim whose HOLDER IS
// GONE (OR-265): claim.Dead sees a dead process, the labels are cleared, and
// the operator is told where the work is. It cannot help when the holder is
// alive and the WORK is what finished.
//
// OR-269 is the case. It completed at 00:49 -- feature plus eight test files,
// red before green, pushed to orion/or-269 -- and readyForBatch's label swap
// reached Jira as a 2xx that changed nothing: no error, so no warning, so
// nothing to retry on. The ticket then wore orion-working for twelve hours
// while the watcher heartbeat kept claim.Dead answering "alive, and rightly
// so". collect reads labels, not branches, so it never saw the finished work;
// two other tickets on the same file waited behind a slot nobody was using.
//
// The failure REPORTS SUCCESS, which is what makes this the only possible
// defence. A reconciler conditioned on having observed an error would not
// have fired.
//
// WHY THE EVENT LOG AND NOT THE BRANCH. A pushed branch alone proves nothing
// -- work in progress pushes too, and a QA round pushes again. What is
// unambiguous is the pipeline's own terminal boundary: readyForBatch emits
// `stage push -> ready` and returns OutcomeReady, at which point the job slot
// is free and the ticket is the integration queue's problem. A ticket whose
// log carries that boundary is finished by the pipeline's own account of
// itself, whatever its label says.
//
// Deliberately NOT a judgement call, and deliberately not an agent. The
// question "did this reach the terminal stage" has one right answer written
// down in a file; an agent asked to infer it could infer wrongly, and the
// cost of a wrong yes is a second agent started on work already done.
func finishedWork(wsDir, key string) bool {
	evs, err := events.Read(events.Path(wsDir))
	if err != nil {
		// No log is not evidence of anything. A ticket claimed before the
		// log existed, or a workspace whose log was rotated away, must stay
		// exactly as it is rather than be reconciled on a guess.
		return false
	}
	done := false
	for _, e := range evs {
		if e.Key != key || e.Kind != events.KindStage {
			continue
		}
		// The LAST such boundary wins, not the first. A ticket can reach
		// ready, be ejected from a batch as the culprit, and be worked
		// again; only its most recent verdict describes it now.
		if h := ui.HandoffOf(e); h.From == stageBeforeReady && h.To == stageReady {
			done = true
		} else {
			// Any later boundary means the pipeline moved on past ready --
			// a fix round, a new implementation pass -- so the ticket is
			// working again and must not be reconciled.
			done = false
		}
	}
	return done
}

// The two stage names readyForBatch crosses between. Spelled here rather than
// imported from internal/work, which would be an import cycle: work already
// depends on watch's siblings and the names are its vocabulary, not ours.
//
// A drift risk worth naming: renaming the stage in internal/work silently
// stops this reconciler. The test asserts on the real emitted event for that
// reason, so the rename breaks a test rather than a night's run.
const (
	stageBeforeReady = "push"
	stageReady       = "ready"
)
