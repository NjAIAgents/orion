package work

// Where a resumed run re-enters the pipeline (OR-429).
//
// OR-265 taught a resumed run to keep its BRANCH: the claim record names it,
// ResumeWorktree reattaches instead of cutting a fresh one, and an hour of
// work stops being thrown away. What it did not do is keep the run's PLACE.
// The pipeline always re-entered at `routing -> implementing`, so a ticket
// interrupted halfway through QA reattached to its finished implementation
// and then ran the implementer over it again.
//
// The evidence is already written down. Every stage crossing emits a
// KindStage event carrying from and to (internal/ui/stage.go), so the last
// boundary a ticket crossed says where it got to. That is authoritative in a
// way an inference about the branch is not: it is the pipeline's own record
// of its own position.
//
// DELIBERATELY NOT AN AGENT. The question is "which stage did this reach",
// and it has one answer in a file. An agent asked to infer it from commit
// shapes could infer wrongly, and a wrong skip means an unimplemented ticket
// handed to QA. When THIS is wrong the cost is bounded and self-correcting --
// QA reads the tree, finds nothing to verify, and sends it back a round --
// because the answer came from the record rather than from a guess.

import (
	"strings"

	"github.com/orion-sdlc/orion/internal/events"
	"github.com/orion-sdlc/orion/internal/supervisor"
	"github.com/orion-sdlc/orion/internal/ui"
)

// resumePoint is where an interrupted run should pick up.
//
// The zero value means "start from the beginning", which is what every run
// that is not a resume gets and what a resume with no usable evidence gets
// too. Falling back to a full run is always safe: it costs the work again,
// where skipping wrongly costs the work being skipped.
type resumePoint struct {
	// Stage is the pipeline stage to re-enter, empty for a fresh start.
	Stage string
	// Actor is who holds that stage.
	Actor string
	// Why is what the operator is told. A resume that silently skips the
	// implementer is indistinguishable from an implementer that did nothing.
	Why string
}

// stagesAfterImplementing are the only stages worth resuming INTO.
//
// Anything at or before implementing means the implementer has not finished,
// and re-running it is exactly right. Anything after push has left the
// coding pipeline entirely -- collect owns the ticket then, and internal/work
// has nothing left to do with it.
//
// A map rather than a list of strings compared by hand: a stage this does not
// know is a stage it must not resume into, and the default of an unknown name
// has to be "start over" rather than "guess".
var stagesAfterImplementing = map[string]string{
	"dba": events.ActorDBA,
	"qa":  events.ActorQA,
}

// resumeAt reads the ticket's own log and reports where to re-enter.
//
// Only the LAST stage boundary counts. A ticket can reach QA, be sent back
// for a fix round, and reach QA again; only where it was when the run died
// describes it now.
func resumeAt(wsDir, key string) resumePoint {
	evs, err := events.Read(events.Path(wsDir))
	if err != nil {
		// No log is not evidence. A full run is the safe reading.
		return resumePoint{}
	}

	last := ""
	for _, e := range evs {
		if e.Key != key || e.Kind != events.KindStage {
			continue
		}
		last = ui.HandoffOf(e).To
	}
	if last == "" {
		return resumePoint{}
	}

	// A FIX ROUND IS QA'S OWN LOOP, not a stage of its own. QA hands back to
	// the implementer as "fix round N" (internal/work/qa.go), and a run that
	// died mid-fix has an implementation QA has already read and rejected.
	// Re-entering at QA would ask it to verify the very tree it just failed;
	// the implementer has to run, and QA's rounds start again with it.
	if strings.HasPrefix(last, "fix round") {
		return resumePoint{}
	}

	actor, ok := stagesAfterImplementing[last]
	if !ok {
		// Includes "implementing" itself, "routing", "push", "ready", "ci"
		// and anything added later that this has not been taught. Each is
		// either too early to skip or past the point work owns.
		return resumePoint{}
	}
	return resumePoint{
		Stage: last, Actor: actor,
		Why: "the interrupted run had finished implementing and reached " + last,
	}
}

// sessionOf is the implementer's session id, or empty when there was no
// implementer to have one (OR-429).
//
// A skipped implementation leaves runRes nil, and two call sites read only
// this field off it. An accessor rather than a nil check at each: the next
// call site added gets this right by construction, which is the same
// argument PreFailure makes in internal/tracker.
//
// Empty degrades correctly all the way down. supervisor.Options omits
// --resume when Resume is empty, so a fix round starts a cold session and
// re-reads the tree instead of recalling the conversation -- slower, and not
// wrong.
func sessionOf(r *supervisor.Result) string {
	if r == nil {
		return ""
	}
	return r.SessionID
}
