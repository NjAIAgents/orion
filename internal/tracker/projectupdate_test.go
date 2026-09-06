package tracker

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The project description is what `orion plan` designs from -- it carries the
// answers given to `orion new`, verbatim -- so correcting it must not require
// recreating a project Jira will not let you delete.
func TestUpdateProjectDescriptionSendsOnlyTheDescription(t *testing.T) {
	var method, path string
	var sent map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method, path = r.Method, r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&sent)
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"key":"CLOUDLEN"}`))
	}))
	defer srv.Close()

	j := &Jira{BaseURL: srv.URL, Email: "e@x", Token: "t", client: srv.Client()}
	if err := j.UpdateProjectDescription("CLOUDLEN", "the corrected text"); err != nil {
		t.Fatalf("UpdateProjectDescription: %v", err)
	}

	if method != "PUT" {
		t.Errorf("method = %s, want PUT", method)
	}
	if !strings.HasSuffix(path, "/project/CLOUDLEN") {
		t.Errorf("path = %s, want it to name the project", path)
	}
	if sent["description"] != "the corrected text" {
		t.Errorf("description = %v", sent["description"])
	}
	// Only the description. A payload carrying name or key would rewrite
	// fields nobody asked to change -- and the key is the project's identity.
	for _, field := range []string{"name", "key", "leadAccountId", "projectTypeKey"} {
		if _, ok := sent[field]; ok {
			t.Errorf("payload also sent %q; it must change only the description", field)
		}
	}
}

// A refused update is ErrNoPermission, not a generic error, so a caller can
// tell "you may not" from "it broke".
func TestUpdateProjectDescriptionSurfacesPermission(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(403)
	}))
	defer srv.Close()

	j := &Jira{BaseURL: srv.URL, Email: "e@x", Token: "t", client: srv.Client()}
	if err := j.UpdateProjectDescription("CLOUDLEN", "x"); err != ErrNoPermission {
		t.Errorf("err = %v, want ErrNoPermission", err)
	}
}

// Any other failure names the project and the status, so the message is
// actionable rather than "request failed".
func TestUpdateProjectDescriptionReportsTheFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(404)
		_, _ = w.Write([]byte(`{"errorMessages":["No project could be found"]}`))
	}))
	defer srv.Close()

	j := &Jira{BaseURL: srv.URL, Email: "e@x", Token: "t", client: srv.Client()}
	err := j.UpdateProjectDescription("NOPE", "x")
	if err == nil {
		t.Fatal("a 404 was reported as success")
	}
	if !strings.Contains(err.Error(), "NOPE") || !strings.Contains(err.Error(), "404") {
		t.Errorf("error does not name the project and status: %v", err)
	}
}
