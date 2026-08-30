package tracker

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// createSrv captures the body Jira would have received, so what actually goes
// over the wire can be asserted rather than inferred from the caller.
func createSrv(t *testing.T) (*Jira, *map[string]any) {
	t.Helper()
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &got)
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "10010", "key": "CSP"})
	}))
	t.Cleanup(srv.Close)
	return &Jira{BaseURL: srv.URL, Email: "e@x", Token: "t", client: srv.Client()}, &got
}

// The elaborated description is the reason `orion new` is interactive at all.
// If it does not reach the create call, the exchange happened and left nothing
// behind -- and the command still reports success.
func TestCreateProjectSendsTheDescription(t *testing.T) {
	j, got := createSrv(t)
	const desc = "## Who it is for\n\nClaims handlers in the contact centre."

	if _, err := j.CreateProject("CSP", "Claim status portal", "acct-1", desc); err != nil {
		t.Fatal(err)
	}
	if (*got)["description"] != desc {
		t.Fatalf("description sent = %q, want the elaborated one", (*got)["description"])
	}
	if (*got)["name"] != "Claim status portal" {
		t.Errorf("name sent = %q", (*got)["name"])
	}
}

// A caller with nothing to say still gets a project that is identifiable as
// Orion's, rather than one with a blank description.
func TestCreateProjectFallsBackWhenDescriptionIsBlank(t *testing.T) {
	j, got := createSrv(t)
	if _, err := j.CreateProject("CSP", "Claim status portal", "acct-1", "   "); err != nil {
		t.Fatal(err)
	}
	if d, _ := (*got)["description"].(string); !strings.Contains(d, "Orion") {
		t.Fatalf("blank description sent as %q, want the Orion marker", d)
	}
}
