package events

import (
	"strings"
	"testing"
	"time"
)

// OR-258. The format used to live in two packages that agreed by coincidence.
// These tests are the agreement: the parser is fed what the WRITER produces,
// never a literal string, so a change to the wording breaks a test rather than
// a dashboard.

func TestANoteSurvivesItsOwnRoundTrip(t *testing.T) {
	for _, n := range []BatchNote{
		{Ref: "orion/batch", Runs: 1, Elapsed: 18 * time.Minute,
			Landed: []string{"OR-1", "OR-2"}, Median: 40 * time.Minute, Samples: 26},
		// The durations Sscanf could not read: under a minute, and over an
		// hour. Both silently emptied the dashboard.
		{Ref: "orion/batch", Runs: 1, Elapsed: 45 * time.Second, Landed: []string{"OR-1"}},
		{Ref: "orion/batch", Runs: 7, Elapsed: 62 * time.Minute, Landed: []string{"OR-1"}},
		// A red batch: nothing landed, and the outcome lists carry the rest.
		{Ref: "orion/batch", Runs: 4, Elapsed: 22 * time.Minute,
			Culprit: []string{"OR-3"}, Deferred: []string{"OR-4", "OR-5"}},
		// No baseline yet, which is a real state and not an error.
		{Ref: "orion/batch", Runs: 1, Elapsed: time.Minute, Landed: []string{"OR-9"}},
	} {
		got, ok := ParseBatchNote(n.String())
		if !ok {
			t.Fatalf("a note this package wrote did not parse: %q", n.String())
		}
		if got.Ref != n.Ref || got.Runs != n.Runs {
			t.Errorf("ref/runs = %s/%d, want %s/%d", got.Ref, got.Runs, n.Ref, n.Runs)
		}
		if got.Elapsed != n.Elapsed.Round(time.Second) && got.Elapsed != n.Elapsed.Round(time.Minute) {
			t.Errorf("elapsed = %s, want %s (from %q)", got.Elapsed, n.Elapsed, n.String())
		}
		if len(got.Members()) != len(n.Members()) {
			t.Errorf("members = %v, want %v", got.Members(), n.Members())
		}
		if n.Samples > 0 && got.Samples != n.Samples {
			t.Errorf("samples = %d, want %d", got.Samples, n.Samples)
		}
	}
}

// The member count must not be keyed off one project's ticket prefix. The old
// parser counted occurrences of "OR-", so every other tracker measured every
// batch as having no members, and the runs-saved figure divided by nothing.
func TestMembersAreCountedWhateverTheProjectKeyLooksLike(t *testing.T) {
	n := BatchNote{Ref: "r", Runs: 1, Elapsed: time.Minute,
		Landed: []string{"FCIA-7", "PROJ-100"}, Ejected: []string{"ABC-1"}}
	got, ok := ParseBatchNote(n.String())
	if !ok {
		t.Fatal("did not parse")
	}
	if len(got.Members()) != 3 {
		t.Errorf("members = %v, want all three regardless of prefix", got.Members())
	}
	if len(got.Landed) != 2 || got.Landed[0] != "FCIA-7" {
		t.Errorf("landed = %v", got.Landed)
	}
}

// A note written before OR-250 added elapsed still names its runs and its
// members. A partial reading of a real batch beats discarding it -- which is
// what happened to this repository's own 2026-08-31 batch.
func TestANoteWithNoElapsedStillParses(t *testing.T) {
	old := "batch on orion/batch: 1 run(s), landed=[OR-226 OR-230] ejected=[] culprit=[] deferred=[]"
	got, ok := ParseBatchNote(old)
	if !ok {
		t.Fatal("a note from before elapsed was recorded was discarded entirely")
	}
	if got.Runs != 1 || len(got.Landed) != 2 {
		t.Errorf("got runs=%d landed=%v", got.Runs, got.Landed)
	}
	if got.Elapsed != 0 {
		t.Errorf("elapsed = %s, want zero rather than a guess", got.Elapsed)
	}
}

func TestSomethingThatIsNotABatchNoteIsNotOne(t *testing.T) {
	for _, msg := range []string{
		"", "claimed", "batching is enabled",
		"the batch cost 1 CI run for 3 branches",
		"batch on orion/batch but nothing else",
	} {
		if _, ok := ParseBatchNote(msg); ok {
			t.Errorf("parsed %q as a batch note", msg)
		}
	}
}

// An unmeasured baseline is not a baseline of zero, and printing "0s" would
// read as one measured and found instant.
func TestAnUnmeasuredDurationIsNamedRatherThanPrintedAsZero(t *testing.T) {
	n := BatchNote{Ref: "r", Runs: 1, Elapsed: time.Minute, Landed: []string{"OR-1"}}
	if !strings.Contains(n.String(), "median unknown") {
		t.Errorf("a note with no baseline says %q; an unmeasured median must not "+
			"print as 0s", n.String())
	}
}
