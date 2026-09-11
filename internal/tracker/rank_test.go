package tracker

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The queue's order is "priority DESC, Rank ASC", so a reorder that does not
// reach Jira's ranking endpoint with the neighbour it is being placed behind
// changes nothing at all -- and the command reporting it would still say it
// worked.
func TestRankAfterAsksJiraToPlaceTheIssueBehindItsNeighbour(t *testing.T) {
	var gotPath, gotMethod string
	var body struct {
		Issues         []string `json:"issues"`
		RankAfterIssue string   `json:"rankAfterIssue"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotMethod = r.URL.Path, r.Method
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &body)
		w.WriteHeader(204)
	}))
	defer srv.Close()

	j := &Jira{BaseURL: srv.URL, Email: "e", Token: "t"}
	if err := j.RankAfter("OR-2", "OR-1"); err != nil {
		t.Fatalf("RankAfter: %v", err)
	}
	if gotMethod != "PUT" || gotPath != "/rest/agile/1.0/issue/rank" {
		t.Errorf("called %s %s, want PUT /rest/agile/1.0/issue/rank", gotMethod, gotPath)
	}
	if len(body.Issues) != 1 || body.Issues[0] != "OR-2" || body.RankAfterIssue != "OR-1" {
		t.Errorf("payload was %+v, want OR-2 ranked after OR-1", body)
	}
}

// 207 is a 2xx that means "accepted, and this issue was NOT ranked". Read as
// success -- which any `code >= 400` check would do -- the operator is told
// the queue was reordered when it was not.
func TestRankAfterTreatsMultiStatusAsAFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(207)
		_, _ = w.Write([]byte(`{"entries":[{"issueId":1,"status":403,"errors":["no permission"]}]}`))
	}))
	defer srv.Close()

	j := &Jira{BaseURL: srv.URL, Email: "e", Token: "t"}
	err := j.RankAfter("OR-2", "OR-1")
	if err == nil {
		t.Fatal("a 207 multi-status reported success, so a refused rank reads as a reorder")
	}
	if !strings.Contains(err.Error(), "OR-2") || !strings.Contains(err.Error(), "no permission") {
		t.Errorf("the error names neither the ticket nor Jira's reason: %v", err)
	}
}

// A 404 here has two causes and only one of them is a wrong key: a site
// without a Jira Software backlog has no ranking endpoint at all. Reporting
// only "not found" sends the operator to check ticket keys that are fine.
func TestRankAfterExplainsAMissingAgileAPI(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(404)
	}))
	defer srv.Close()

	j := &Jira{BaseURL: srv.URL, Email: "e", Token: "t"}
	err := j.RankAfter("OR-2", "OR-1")
	if err == nil {
		t.Fatal("a 404 was reported as a successful rank")
	}
	if !strings.Contains(err.Error(), "Jira Software") {
		t.Errorf("the error does not mention the other cause of a 404: %v", err)
	}
}

// The queue orders by priority before rank, so `orion prioritise` has to know
// each ticket's priority before it writes anything. GetIssue not asking for
// the field would leave that check reading "" for every ticket -- all equal,
// no refusal, and an ordering written that the queue will not show.
func TestGetIssueReadsPriority(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.RawQuery, "priority") {
			t.Errorf("GetIssue did not ask Jira for the priority field: %s", r.URL.RawQuery)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"key": "OR-1",
			"fields": map[string]any{
				"summary":  "x",
				"status":   map[string]any{"name": "To Do"},
				"priority": map[string]any{"name": "High"},
			},
		})
	}))
	defer srv.Close()

	j := &Jira{BaseURL: srv.URL, Email: "e", Token: "t"}
	is, err := j.GetIssue("OR-1")
	if err != nil {
		t.Fatalf("GetIssue: %v", err)
	}
	if is.Priority != "High" {
		t.Errorf("priority is %q, want High", is.Priority)
	}
}
