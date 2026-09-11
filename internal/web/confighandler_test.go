package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// Given the config page, Then it lists each role with the model it uses,
// sourced from actors.Roster -- not duplicated here. Proven by asserting the
// response actually names configurable roles, which only happens if Roster
// (OR-80) was really called.
func TestConfigHandlerServesTheRoster(t *testing.T) {
	home := t.TempDir() // no agents.json: every actor is a shipped default
	t.Setenv("ORION_HOME", home)

	req := httptest.NewRequest(http.MethodGet, "/api/config", nil)
	rec := httptest.NewRecorder()
	configHandler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}
	var v ConfigView
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	if len(v.Roster) == 0 {
		t.Fatal("Roster is empty, want the shipped default roster")
	}
	// Not every configurable actor runs on a named model (the orchestrator
	// itself is one) -- this only asserts the roster came through real, not
	// that every field is non-empty.
	found := false
	for _, e := range v.Roster {
		if e.ID != "" {
			found = true
		}
	}
	if !found {
		t.Error("no roster entry carried an ID -- Roster likely did not resolve")
	}
}

// A missing agents.json is not an error -- every actor reads as a shipped
// default, the same posture Roster's own doc comment states.
func TestConfigHandlerOnAFreshInstallIs200(t *testing.T) {
	home := t.TempDir()
	t.Setenv("ORION_HOME", home)

	req := httptest.NewRequest(http.MethodGet, "/api/config", nil)
	rec := httptest.NewRecorder()
	configHandler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 on a fresh install", rec.Code)
	}
}
