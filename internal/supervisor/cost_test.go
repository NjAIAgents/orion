package supervisor

import (
	"strings"
	"testing"
	"time"

	"github.com/orion-sdlc/orion/internal/cost"
	"github.com/orion-sdlc/orion/internal/events"
)

const resultJSON = `{"type":"result","is_error":false,"num_turns":7,"session_id":"s",` +
	`"total_cost_usd":1.25,"usage":{"input_tokens":900,"cache_creation_input_tokens":100,` +
	`"cache_read_input_tokens":50000,"output_tokens":400}}`

func aggregate(t *testing.T, wsDir, key string) cost.Report {
	t.Helper()
	return cost.Aggregate(cost.ReadAll(events.Path(wsDir)), key)
}

// The supervisor is the only layer that sees EVERY run: the ones that
// succeeded, the ones a caller abandoned on failure, and the quota attempts
// that never reached a caller at all. So it is the layer that books them.
func TestEveryRunIsBookedAgainstTheTicket(t *testing.T) {
	w := ws(t, "")
	opts := Options{Stage: "ticket", Actor: events.ActorImplementer, Key: "OR-9"}

	recordTicketCost(w, opts, &Result{Duration: 90 * time.Second}, resultJSON)
	// A killed run. It spent everything it sent before the wall clock fired,
	// and leaving it out is how a ticket's true cost goes missing.
	recordTicketCost(w, opts, &Result{ExitCode: 124, Reason: "timed out",
		Duration: 30 * time.Minute}, resultJSON)

	rep := aggregate(t, w.Dir, "OR-9")
	if len(rep.Runs) != 2 {
		t.Fatalf("booked %d runs, want 2", len(rep.Runs))
	}
	if rep.Total.Failed != 1 {
		t.Errorf("the killed run was not marked as failed: %+v", rep.Total)
	}
	if rep.Total.CostUSD != 2.5 || rep.Total.Turns != 14 {
		t.Errorf("totals = $%.2f / %d turns, want $2.50 / 14", rep.Total.CostUSD, rep.Total.Turns)
	}
	// The three input counts stay apart, because they are billed differently.
	if rep.Total.Prompt != 1800 || rep.Total.CacheW != 200 || rep.Total.CacheR != 100_000 {
		t.Errorf("input/cache split lost: in=%d cache w=%d cache r=%d",
			rep.Total.Prompt, rep.Total.CacheW, rep.Total.CacheR)
	}
	if rep.Rows[0].ID != events.ActorImplementer {
		t.Errorf("attributed to %q, want the actor id the caller named", rep.Rows[0].ID)
	}
}

// A run whose result JSON never arrived -- it crashed, or the CLI died -- is
// recorded as a run with nothing to show. The report then says the total is a
// floor, which is the honest answer; dropping the row would present a lowball
// number as complete.
func TestARunThatReportedNothingIsStillRecorded(t *testing.T) {
	w := ws(t, "")
	recordTicketCost(w, Options{Actor: events.ActorImplementer, Key: "OR-9"},
		&Result{ExitCode: 1, Reason: "claude exited 1"}, "no json here")

	rep := aggregate(t, w.Dir, "OR-9")
	if len(rep.Runs) != 1 || rep.Total.Missing != 1 {
		t.Fatalf("a run with no usage was dropped: %d runs, %d missing",
			len(rep.Runs), rep.Total.Missing)
	}
	out := cost.Render(rep)
	for _, want := range []string{"FLOOR", "usage missing"} {
		if !strings.Contains(out, want) {
			t.Errorf("the report presented an incomplete total as complete:\n%s", out)
		}
	}
}

// A stage run driven by hand belongs to no ticket. Booking it against one
// would attribute a person's own experiment to whatever ticket happened to be
// open, so an unattributed run records nothing at all.
func TestARunWithNoTicketBooksNothing(t *testing.T) {
	w := ws(t, "")
	recordTicketCost(w, Options{Stage: "intent"}, &Result{}, resultJSON)
	recordTicketCost(w, Options{Actor: events.ActorImplementer, Key: "OR-9", DryRun: true},
		&Result{}, resultJSON)

	if rep := aggregate(t, w.Dir, "OR-9"); !rep.Empty() {
		t.Errorf("booked %d runs against a ticket that started none", len(rep.Runs))
	}
}
