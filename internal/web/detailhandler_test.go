package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/orion-sdlc/orion/internal/events"
)

// Given a request with both key and run, When the workspace holding that
// key is found, Then the detail reflects exactly that run's events.
func TestDetailHandlerServesTheNamedRun(t *testing.T) {
	home := t.TempDir()
	dir := mkTestWorkspace(t, home, "proj-a")
	appendEvent(t, dir, evt(t0(1), events.KindTool, "OR-1", "r1", "implementer", "opus", "editing foo.go"))
	appendEvent(t, dir, evt(t0(2), events.KindPR, "OR-1", "r1", "orion", "", "https://github.com/x/y/pull/1"))
	t.Setenv("ORION_HOME", home)

	req := httptest.NewRequest(http.MethodGet, "/api/detail?key=OR-1&run=r1", nil)
	rec := httptest.NewRecorder()
	detailHandler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}
	var d Detail
	if err := json.Unmarshal(rec.Body.Bytes(), &d); err != nil {
		t.Fatal(err)
	}
	if d.Key != "OR-1" || d.Run != "r1" {
		t.Errorf("Key/Run = %q/%q, want OR-1/r1", d.Key, d.Run)
	}
	if len(d.Steps) != 1 {
		t.Errorf("Steps = %d, want 1", len(d.Steps))
	}
	if d.PR == nil || d.PR.URL != "https://github.com/x/y/pull/1" {
		t.Errorf("PR = %+v, want the pr event's URL", d.PR)
	}
}

// Given a request missing key or run, Then it is a usage error (400), not a
// guess at which run was meant.
func TestDetailHandlerRequiresBothKeyAndRun(t *testing.T) {
	home := t.TempDir()
	t.Setenv("ORION_HOME", home)

	for _, q := range []string{"", "?key=OR-1", "?run=r1"} {
		req := httptest.NewRequest(http.MethodGet, "/api/detail"+q, nil)
		rec := httptest.NewRecorder()
		detailHandler(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("query %q: status = %d, want 400", q, rec.Code)
		}
	}
}

// Given a key that no workspace's log has ever recorded, Then the detail is
// a 200 with an empty body, not a 404 or a 500 -- the same "missing source
// is not an error" rule api.go states for the snapshot.
func TestDetailHandlerOnAnUnknownKeyIs200AndEmpty(t *testing.T) {
	home := t.TempDir()
	dir := mkTestWorkspace(t, home, "proj-a")
	appendEvent(t, dir, evt(t0(1), events.KindTool, "OR-1", "r1", "implementer", "opus", "editing foo.go"))
	t.Setenv("ORION_HOME", home)

	req := httptest.NewRequest(http.MethodGet, "/api/detail?key=OR-999&run=none", nil)
	rec := httptest.NewRecorder()
	detailHandler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 on an unknown key", rec.Code)
	}
	var d Detail
	if err := json.Unmarshal(rec.Body.Bytes(), &d); err != nil {
		t.Fatal(err)
	}
	if d.Steps != nil || d.Asks != nil || d.Decisions != nil || d.PR != nil {
		t.Errorf("Detail for an unknown key should be entirely empty, got %+v", d)
	}
}

// A fresh install -- nothing ever run, no projects directory at all -- must
// serve 200, the same rule OR-65 established for the snapshot endpoint and
// restated here.
func TestDetailHandlerOnAFreshInstallIs200(t *testing.T) {
	home := t.TempDir() // no projects/ directory at all
	t.Setenv("ORION_HOME", home)

	req := httptest.NewRequest(http.MethodGet, "/api/detail?key=OR-1&run=r1", nil)
	rec := httptest.NewRecorder()
	detailHandler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 on a fresh install", rec.Code)
	}
}
