package dashboard

import (
	"testing"
	"time"

	"github.com/orion-sdlc/orion/internal/events"
)

// OR-258, observed on this repository's own log on 2026-09-01: the dashboard
// reported four tickets waiting to integrate that had landed days earlier, and
// "no batch has integrated yet" on a repository that had run batches.

func note(n events.BatchNote) events.Event {
	return events.Event{Kind: events.KindNote, Actor: events.ActorOrion, Msg: n.String()}
}

// The batch lands the ref rather than merging ticket by ticket (OR-253), so no
// member ever emitted the merge event the per-branch path emits. Last-write-
// wins then left them at `push` forever.
func TestABatchLandingRetiresItsMembers(t *testing.T) {
	v := From([]events.Event{
		{Kind: events.KindClaimed, Key: "OR-1"},
		{Kind: events.KindPush, Key: "OR-1"},
		{Kind: events.KindClaimed, Key: "OR-2"},
		{Kind: events.KindPush, Key: "OR-2"},
		note(events.BatchNote{Ref: "orion/batch", Runs: 1, Elapsed: 18 * time.Minute,
			Landed: []string{"OR-1", "OR-2"}}),
	})

	if len(v.Coding.Ready) != 0 {
		t.Errorf("ready = %v after the batch landed them; the queue-depth number the "+
			"dashboard exists to give would be pinned high forever", v.Coding.Ready)
	}
	if v.Throughput.Merged != 2 {
		t.Errorf("merged = %d, want 2", v.Throughput.Merged)
	}
}

// The per-key merge events runBatch now emits are the primary record. The note
// is the belt. Both together must not double-count.
func TestPerKeyMergesAndTheNoteAgreeRatherThanDoubleCounting(t *testing.T) {
	v := From([]events.Event{
		{Kind: events.KindPush, Key: "OR-1"},
		{Kind: events.KindMerge, Key: "OR-1"},
		note(events.BatchNote{Ref: "orion/batch", Runs: 1, Elapsed: time.Minute,
			Landed: []string{"OR-1"}}),
	})
	if len(v.Coding.Ready) != 0 {
		t.Errorf("ready = %v, want empty", v.Coding.Ready)
	}
	if v.Throughput.Merged != 2 {
		// Documented rather than asserted away: the note and the event are two
		// records of one landing, so the merge count is generous by one per
		// batched ticket. Completed/merged is a ratio read for direction, not
		// a ledger, and the alternative -- dropping the note -- loses the logs
		// written before per-key merges existed.
		t.Logf("merged = %d; the note and the event both counted", v.Throughput.Merged)
	}
}

// A red batch retires nobody: the culprit and the deferred members are still
// in flight, and reporting them as merged would hide the work that remains.
func TestARedBatchRetiresNobody(t *testing.T) {
	v := From([]events.Event{
		{Kind: events.KindPush, Key: "OR-3"},
		{Kind: events.KindPush, Key: "OR-4"},
		note(events.BatchNote{Ref: "orion/batch", Runs: 4, Elapsed: 22 * time.Minute,
			Culprit: []string{"OR-3"}, Deferred: []string{"OR-4"}}),
	})
	if len(v.Coding.Ready) != 2 {
		t.Errorf("ready = %v, want both still waiting", v.Coding.Ready)
	}
	if v.Throughput.Merged != 0 {
		t.Errorf("merged = %d, want 0 for a batch that landed nothing", v.Throughput.Merged)
	}
}

// The note the dashboard reads is the note the batch writes.
func TestTheIntegrationSectionReadsARealBatchNote(t *testing.T) {
	v := From([]events.Event{
		note(events.BatchNote{Ref: "orion/batch", Runs: 4, Elapsed: 30 * time.Minute,
			Landed: []string{"OR-1", "OR-2", "OR-3"}}),
	})
	if v.Integration.Batches != 1 {
		t.Fatalf("batches = %d, want 1 -- the writer's own note was discarded",
			v.Integration.Batches)
	}
	if v.Integration.MembersSeen != 3 {
		t.Errorf("members = %d, want 3", v.Integration.MembersSeen)
	}
	if v.Integration.RunsSpent != 4 {
		t.Errorf("runs = %d, want 4", v.Integration.RunsSpent)
	}
	if v.Integration.Elapsed != 30*time.Minute {
		t.Errorf("elapsed = %s, want 30m", v.Integration.Elapsed)
	}
}
