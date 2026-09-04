package ui

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

// Every ticket in the queue keeps a row; only its stage changes (OR-325).
// Observed: the watch listed four tickets, then the rows vanished and only
// the batch block remained, because a row was an agent in this process.
func TestQueuedTicketsKeepARowThatDoesNotSpinOrDrawABar(t *testing.T) {
	LiveReset()
	t.Cleanup(LiveReset)
	now := time.Now()
	LiveQueue([]QueueRow{
		{Key: "OR-295", Stage: "ready", Title: "Rename the package"},
		{Key: "OR-301", Stage: "queued", Title: "Record the decision"},
	})
	var buf bytes.Buffer
	rows := renderRegionAt(&buf, liveSnapshot(), now, 200, false)
	got := plain(strings.Join(rows, "\n"))
	for _, want := range []string{"OR-295", "ready", "OR-301", "queued", "Rename the package"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q:\n%s", want, got)
		}
	}
	for _, line := range rows {
		p := plain(line)
		if !strings.Contains(p, "OR-") {
			continue
		}
		if strings.ContainsAny(p, spinnerGlyphs) || strings.Contains(p, "✓") {
			t.Errorf("a queued row must neither spin nor tick: %q", p)
		}
		if strings.ContainsAny(p, barFullGlyph+barHeadGlyph+barEmptyGlyph) {
			t.Errorf("a queued row draws no bar: %q", p)
		}
	}
	if !strings.Contains(got, "2 queued") {
		t.Errorf("the header must count the queue:\n%s", got)
	}
}

// A row an agent owns is never replaced by a tracker read, and a queued row
// the tracker no longer lists is gone.
func TestAQueueReconcileLeavesRunningRowsAloneAndDropsTheDeparted(t *testing.T) {
	LiveReset()
	t.Cleanup(LiveReset)
	now := time.Now()
	liveStart("OR-297", now.Add(-10*time.Minute))
	liveStage("OR-297", "qa", "brandon")
	LiveQueue([]QueueRow{{Key: "OR-297", Stage: "working"}, {Key: "OR-300", Stage: "ready"}})
	LiveQueue([]QueueRow{{Key: "OR-297", Stage: "working"}})

	st := liveSnapshot()
	if len(st.rows) != 1 || st.rows[0].key != "OR-297" {
		t.Fatalf("rows = %+v, want only the running OR-297", st.rows)
	}
	if st.rows[0].queued || st.rows[0].stage != "qa" || st.rows[0].actor != "brandon" {
		t.Errorf("the running row was replaced by a queue row: %+v", st.rows[0])
	}
}

// A member's row says what the batch made of it.
func TestABatchMembersRowTakesItsStageFromTheBatch(t *testing.T) {
	LiveReset()
	t.Cleanup(LiveReset)
	now := time.Now()
	LiveQueue([]QueueRow{{Key: "OR-297", Stage: "ready"}, {Key: "OR-300", Stage: "ready"}, {Key: "OR-295", Stage: "ready"}})
	LiveBatchStart("orion/batch", "develop", []string{"OR-297", "OR-300", "OR-295"})
	LiveBatchMember("OR-297", MemberCulprit)
	LiveBatchMember("OR-300", MemberLanded)
	LiveBatchMember("OR-295", MemberEjected)

	var buf bytes.Buffer
	got := plain(strings.Join(renderRegionAt(&buf, liveSnapshot(), now, 200, false), "\n"))
	for key, stage := range map[string]string{"OR-297": "culprit", "OR-300": "landed", "OR-295": "ejected"} {
		found := false
		for _, line := range strings.Split(got, "\n") {
			if strings.Contains(line, key) && strings.Contains(line, stage) && !strings.Contains(line, "┝") {
				found = true
			}
		}
		if !found {
			t.Errorf("%s's row does not say %q:\n%s", key, stage, got)
		}
	}
}

// Off a terminal a queued row is its key and its stage, nothing measured.
func TestOffTerminalAQueuedRowPrintsNoElapsedOrNotes(t *testing.T) {
	LiveReset()
	t.Cleanup(LiveReset)
	LiveQueue([]QueueRow{{Key: "OR-295", Stage: "ready"}})
	lines := renderPlain(liveSnapshot(), time.Now())
	got := strings.Join(lines, "\n")
	if !strings.Contains(got, "OR-295") || !strings.Contains(got, "ready") {
		t.Fatalf("the queued row is missing:\n%s", got)
	}
	if strings.Contains(got, "h") && strings.Contains(got, "calls") || strings.Contains(got, "baseline") {
		t.Errorf("a queued row must print no elapsed, calls or notes:\n%s", got)
	}
}
