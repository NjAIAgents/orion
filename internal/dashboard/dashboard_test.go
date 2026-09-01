package dashboard

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/orion-sdlc/orion/internal/events"
)

func at(min int) time.Time {
	return time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC).Add(time.Duration(min) * time.Minute)
}

// The three coding states are the last thing that happened to each ticket.
func TestCodingStateIsTheLastTransitionPerTicket(t *testing.T) {
	v := From([]events.Event{
		{At: at(0), Kind: events.KindClaimed, Key: "OR-1"},
		{At: at(1), Kind: events.KindClaimed, Key: "OR-2"},
		{At: at(2), Kind: events.KindPush, Key: "OR-2"}, // finished, now ready
		{At: at(3), Kind: events.KindClaimed, Key: "OR-3"},
		{At: at(4), Kind: events.KindPush, Key: "OR-3"},
		{At: at(5), Kind: events.KindMerge, Key: "OR-3"}, // landed, gone
		{At: at(6), Kind: events.KindFailed, Key: "OR-4"},
	})

	if got := v.Coding.Active; len(got) != 1 || got[0] != "OR-1" {
		t.Errorf("active = %v, want [OR-1]", got)
	}
	if got := v.Coding.Ready; len(got) != 1 || got[0] != "OR-2" {
		t.Errorf("ready = %v, want [OR-2]: pushed and nothing running", got)
	}
	if got := v.Coding.Fixing; len(got) != 1 || got[0] != "OR-4" {
		t.Errorf("fixing = %v, want [OR-4]", got)
	}
	// A merged ticket is in none of the three: it is done, not waiting.
	for _, list := range [][]string{v.Coding.Active, v.Coding.Ready, v.Coding.Fixing} {
		for _, k := range list {
			if k == "OR-3" {
				t.Errorf("OR-3 merged but is still shown as in flight")
			}
		}
	}
}

// The gap between finished and landed is the bottleneck moving to
// integration, and it is the whole reason both numbers are reported.
func TestCompletedAndMergedAreCountedSeparately(t *testing.T) {
	v := From([]events.Event{
		{At: at(0), Kind: events.KindPush, Key: "OR-1"},
		{At: at(1), Kind: events.KindPush, Key: "OR-2"},
		{At: at(2), Kind: events.KindPush, Key: "OR-3"},
		{At: at(3), Kind: events.KindMerge, Key: "OR-1"},
	})
	if v.Throughput.Completed != 3 || v.Throughput.Merged != 1 {
		t.Errorf("completed=%d merged=%d, want 3 and 1: the gap IS the signal",
			v.Throughput.Completed, v.Throughput.Merged)
	}
}

// A batch that cost more than the path it replaced must be reported as such.
// A metric that can only show a saving is advertising.
func TestRunsAvoidedIsSignedAndReportsALoss(t *testing.T) {
	// Two members, three runs: the batch went red and bisected.
	lost := Integration{Batches: 1, RunsSpent: 3, MembersSeen: 2}
	if got := lost.RunsAvoided(); got != -1 {
		t.Errorf("RunsAvoided = %d, want -1", got)
	}

	var w bytes.Buffer
	Render(&w, View{Integration: lost})
	if !strings.Contains(w.String(), "COST") {
		t.Errorf("a batch that cost more did not say so:\n%s", w.String())
	}

	won := Integration{Batches: 1, RunsSpent: 1, MembersSeen: 4}
	if got := won.RunsAvoided(); got != 3 {
		t.Errorf("RunsAvoided = %d, want 3", got)
	}
}

// Nothing integrated yet is SAID, not shown as zero. "0 min" is a
// measurement; "nothing has integrated" is the truth, and they lead to
// different actions.
func TestNoBatchesYetIsStatedRatherThanShownAsZero(t *testing.T) {
	var w bytes.Buffer
	Render(&w, From([]events.Event{{At: at(0), Kind: events.KindPush, Key: "OR-1"}}))
	out := w.String()

	if !strings.Contains(out, "no batch has integrated yet") {
		t.Errorf("want the absence stated:\n%s", out)
	}
	if strings.Contains(out, "avg integration 0s") {
		t.Errorf("reported a zero average as though it were measured:\n%s", out)
	}
}

// Queue depth is the leading indicator, so it must be visible even when
// nothing has integrated and every other integration number is unknown.
func TestQueueDepthIsShownEvenWithNoIntegrationHistory(t *testing.T) {
	var w bytes.Buffer
	Render(&w, From([]events.Event{
		{At: at(0), Kind: events.KindPush, Key: "OR-1"},
		{At: at(1), Kind: events.KindPush, Key: "OR-2"},
	}))
	if !strings.Contains(w.String(), "queue depth     2") {
		t.Errorf("queue depth missing:\n%s", w.String())
	}
}

// The batch record is read out of the note runBatch writes.
func TestABatchRecordIsReadFromTheLog(t *testing.T) {
	v := From([]events.Event{
		{At: at(0), Kind: events.KindNote,
			Msg: "batch on orion/batch: 1 run(s) in 18m, landed=[OR-1 OR-2 OR-3] " +
				"ejected=[] culprit=[] deferred=[] (per-branch median 0s over 0 landing(s))"},
	})
	if v.Integration.Batches != 1 {
		t.Fatalf("batches = %d, want 1", v.Integration.Batches)
	}
	if v.Integration.RunsSpent != 1 {
		t.Errorf("runs = %d, want 1", v.Integration.RunsSpent)
	}
	if v.Integration.MembersSeen != 3 {
		t.Errorf("members = %d, want 3", v.Integration.MembersSeen)
	}
	if v.Integration.Avg() != 18*time.Minute {
		t.Errorf("avg = %s, want 18m", v.Integration.Avg())
	}
	if got := v.Integration.RunsAvoided(); got != 2 {
		t.Errorf("runs avoided = %d, want 2: three members, one run", got)
	}
}

// A note that is not a batch record must not be parsed as one.
func TestAnUnrelatedNoteIsIgnored(t *testing.T) {
	v := From([]events.Event{
		{At: at(0), Kind: events.KindNote, Msg: "asked for merge approval in Slack"},
		{At: at(1), Kind: events.KindNote, Msg: "batch on orion/batch: not the shape"},
	})
	if v.Integration.Batches != 0 {
		t.Errorf("batches = %d, want 0: an unparseable note is not a batch", v.Integration.Batches)
	}
}
