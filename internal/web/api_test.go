package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/orion-sdlc/orion/internal/events"
)

// A machine with nothing ever run is a fresh install, not a fault (OR-65's
// rule, exercised at this ticket's boundary). The handler answers 200 with
// an empty body rather than an error page.
func TestSnapshotOnAnEmptyMachineIsEmptyButValid(t *testing.T) {
	snap, err := buildSnapshot(t.TempDir())
	if err != nil {
		t.Fatalf("an empty machine must not be an error: %v", err)
	}
	if len(snap.Cards) != 0 {
		t.Errorf("expected no cards, got %v", snap.Cards)
	}
}

// THE ACCEPTANCE CRITERION, verbatim: two calls straddling an appended event
// return different results. Proves the endpoint re-reads rather than caching
// a snapshot taken once at startup.
func TestSnapshotChangesBetweenTwoCallsAcrossAnAppendedEvent(t *testing.T) {
	home := t.TempDir()
	ws := mkTestWorkspace(t, home, "proj-a")

	first, err := buildSnapshot(home)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Cards) != 0 {
		t.Fatalf("expected no cards before any event, got %v", first.Cards)
	}

	appendEvent(t, ws, events.Event{
		Kind: events.KindRunStart, Key: "OR-1", Actor: "implementer", Run: "r1",
	})

	second, err := buildSnapshot(home)
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Cards) != 1 {
		t.Fatalf("expected one card after the append, got %v", second.Cards)
	}
	if second.Cards[0].Key != "OR-1" {
		t.Errorf("expected the card keyed on OR-1, got %q", second.Cards[0].Key)
	}
}

// A workspace whose log cannot be read must not take the whole snapshot
// down -- an aborted `orion init` or a directory mid-write is not this
// handler's problem to fail on.
func TestAnUnreadableWorkspaceIsSkippedNotFatal(t *testing.T) {
	home := t.TempDir()
	// A directory with no .orion at all: events.Read returns an error, and
	// that must be swallowed per-workspace rather than propagated.
	if err := os.MkdirAll(filepath.Join(home, "projects", "half-init"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := buildSnapshot(home); err != nil {
		t.Fatalf("one unreadable workspace must not fail the whole snapshot: %v", err)
	}
}

// The endpoint itself: content type, status, and that the body is the same
// shape buildSnapshot produces -- not a re-test of buildSnapshot's own
// behaviour, which the tests above already cover.
func TestSnapshotHandlerServesJSON(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/snapshot", nil)
	snapshotHandler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("expected application/json, got %q", ct)
	}
	var snap Snapshot
	if err := json.Unmarshal(rec.Body.Bytes(), &snap); err != nil {
		t.Fatalf("response body did not decode as a Snapshot: %v", err)
	}
}

// --- helpers ---

// mkTestWorkspace creates an unregistered workspace directory, which
// sessions.Scan picks up without needing a registry fixture -- the simpler
// path for a test that only cares about the event log inside it.
func mkTestWorkspace(t *testing.T, home, id string) string {
	t.Helper()
	dir := filepath.Join(home, "projects", id)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	return dir
}

func appendEvent(t *testing.T, wsDir string, e events.Event) {
	t.Helper()
	log, err := events.Open(events.Path(wsDir), events.Event{})
	if err != nil {
		t.Fatal(err)
	}
	log.Emit(e)
	if err := log.Close(); err != nil {
		t.Fatal(err)
	}
}
