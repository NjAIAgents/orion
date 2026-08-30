package cost

import (
	"strings"
	"testing"

	"github.com/orion-sdlc/orion/internal/events"
)

// OR-219, second problem. The report is a TABLE dropped into a stream of
// one-line status records, and it used to begin with a bare title and end with
// a wall-time sentence -- so in a concurrent watch log there was nothing to
// say where it started or where it stopped.
//
// Asserted the way it actually fails: the report is surrounded by other
// output, and a reader has to be able to cut it back out.
func TestTheReportIsOneBoundedBlockInAConcurrentLog(t *testing.T) {
	report := Render(Aggregate(ReadAll(ticket(t)), "OR-9"))
	log := "18:52:03 OR-9  ok  something else printed first\n" +
		report +
		"18:52:09 OR-10 ok  and something else after it\n"

	lines := strings.Split(log, "\n")
	var first, last int = -1, -1
	for i, l := range lines {
		if strings.Contains(l, "cost report OR-9") {
			if first < 0 {
				first = i
			}
			last = i
		}
	}
	if first < 0 || last <= first {
		t.Fatalf("the report has no opening and closing boundary:\n%s", log)
	}
	if !strings.Contains(lines[last], "end") {
		t.Errorf("the last boundary does not say it is the end: %q", lines[last])
	}
	// Everything the report promises has to sit INSIDE the two rules; a number
	// outside them belongs to whatever else was printing.
	inside := strings.Join(lines[first:last+1], "\n")
	for _, want := range []string{"$4.50", "wall time", "runs"} {
		if !strings.Contains(inside, want) {
			t.Errorf("%q fell outside the block:\n%s", want, inside)
		}
	}
	for _, other := range []string{"something else printed first", "and something else after it"} {
		if strings.Contains(inside, other) {
			t.Errorf("the block swallowed another line: %q", other)
		}
	}
}

// A ticket nothing is known about still gets both edges. It is the shortest
// report there is, which is exactly when an unbounded one disappears into the
// surrounding log.
func TestEvenTheEmptyReportIsBounded(t *testing.T) {
	out := Render(Aggregate(nil, "OR-9"))
	if strings.Count(out, "cost report OR-9") != 2 {
		t.Errorf("the empty report is not bounded at both ends:\n%s", out)
	}
}

// The renderer's own half of the never-started distinction, over the event log
// rather than through the supervisor: a fault and a failure recorded side by
// side must read as two different things.
func TestTheReportTellsAFaultFromAFailure(t *testing.T) {
	dir := t.TempDir()
	path := events.Path(dir)
	log, err := events.Open(path, events.Event{})
	if err != nil {
		t.Fatal(err)
	}
	log.Emit(events.Event{})
	Record(log, "", events.ActorImplementer, "OR-9", Run{
		Failed: true, Reason: "claude exited 1", Seconds: 5, NeverStarted: true,
	})
	Record(log, "", events.ActorImplementer, "OR-9", Run{
		Failed: true, Reason: "timed out", Seconds: 1800, HaveUsage: true,
		Turns: 40, CostUSD: 0.09,
	})
	log.Close()

	rep := Aggregate(ReadAll(path), "OR-9")
	if rep.Total.NeverStarted != 1 || rep.Total.Failed != 1 {
		t.Fatalf("never started %d / failed %d, want 1 / 1",
			rep.Total.NeverStarted, rep.Total.Failed)
	}

	out := Render(rep)
	if !strings.Contains(out, "never started") {
		t.Errorf("the fault is not named:\n%s", out)
	}
	if !strings.Contains(out, "1 failed") || !strings.Contains(out, "1 never started") {
		t.Errorf("the runs column merges the two counts:\n%s", out)
	}
	if strings.Contains(out, "FLOOR") {
		t.Errorf("the floor warning fired over a run known to have spent nothing:\n%s", out)
	}
}
