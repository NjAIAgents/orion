package tracker

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// `release add` has to tell "this ticket is on no milestone" from "this ticket
// is on a DIFFERENT one" -- an add and a move are different sentences -- and
// neither is answerable unless GetIssue actually parses fixVersions. A missing
// field tag here would report every ticket as carrying no milestone, so every
// move would silently look like an add (OR-222).
func TestGetIssueParsesFixVersions(t *testing.T) {
	var asked string
	j := fakeJira(t, func(method, path string, body []byte) (int, string) {
		asked = path
		return 200, `{"key":"OR-105","fields":{"summary":"x","status":{"name":"To Do"},
		  "fixVersions":[{"name":"v0.8.2"},{"name":"v0.8.3"}]}}`
	})

	i, err := j.GetIssue("OR-105")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(i.FixVersions, ",") != "v0.8.2,v0.8.3" {
		t.Errorf("FixVersions = %v, want [v0.8.2 v0.8.3]", i.FixVersions)
	}
	// Jira only returns fields that were asked for, so the request itself has
	// to name fixVersions or the parsing above never sees anything.
	if !strings.Contains(asked, "fixVersions") {
		t.Errorf("GetIssue did not request fixVersions: %s", asked)
	}
}

// A ticket on no milestone must parse to an EMPTY list, not to a zero-valued
// placeholder that then looks like a milestone named "".
func TestGetIssueLeavesFixVersionsEmptyWhenAbsent(t *testing.T) {
	j := fakeJira(t, func(method, path string, body []byte) (int, string) {
		return 200, `{"key":"OR-100","fields":{"summary":"x","status":{"name":"To Do"}}}`
	})

	i, err := j.GetIssue("OR-100")
	if err != nil {
		t.Fatal(err)
	}
	if len(i.FixVersions) != 0 {
		t.Errorf("FixVersions = %v, want none", i.FixVersions)
	}
}

// Search feeds the same plan when tickets arrive from a query rather than by
// key, and it has its own struct for the fields block -- so the same parsing
// has to be proven separately.
func TestSearchParsesFixVersions(t *testing.T) {
	var asked string
	j := fakeJira(t, func(method, path string, body []byte) (int, string) {
		asked = path
		return 200, `{"issues":[{"key":"OR-105","fields":{"summary":"x",
		  "status":{"name":"To Do","statusCategory":{"key":"new"}},
		  "fixVersions":[{"name":"v0.8.2"}]}}]}`
	})

	is, err := j.Search(JQLEq("project", "OR"), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(is) != 1 || strings.Join(is[0].FixVersions, ",") != "v0.8.2" {
		t.Errorf("FixVersions = %v, want [v0.8.2]", is[0].FixVersions)
	}
	if !strings.Contains(asked, "fixVersions") {
		t.Errorf("Search did not request fixVersions: %s", asked)
	}
}

// "That ticket does not exist" and "the tracker is unreachable" are different
// answers: a range names tickets nobody looked at, and reporting an outage as
// a missing ticket would let `release add` quietly skip real work.
func TestGetIssueReportsAMissingIssueDistinctly(t *testing.T) {
	j := fakeJira(t, func(method, path string, body []byte) (int, string) {
		return 404, `{"errorMessages":["Issue does not exist"]}`
	})

	_, err := j.GetIssue("OR-999")
	if !errors.Is(err, ErrIssueNotFound) {
		t.Fatalf("a 404 did not come back as ErrIssueNotFound: %v", err)
	}
	if !strings.Contains(err.Error(), "OR-999") {
		t.Errorf("the error does not name the key that was missing: %v", err)
	}

	// A server error must NOT read as a missing ticket.
	j = fakeJira(t, func(method, path string, body []byte) (int, string) {
		return 500, `{"error":"boom"}`
	})
	if _, err := j.GetIssue("OR-1"); errors.Is(err, ErrIssueNotFound) {
		t.Errorf("a 500 was reported as the issue not existing: %v", err)
	}
}

// SetFixVersion REPLACES: a ticket joining v0.8.3 leaves v0.8.2, which is what
// `release add` reports as a move. Appending instead would leave it counted in
// both, and `release status` would then reconcile one changelog fragment
// against two milestones.
func TestSetFixVersionReplacesByVersionID(t *testing.T) {
	var gotMethod, gotPath string
	var gotBody []byte
	j := fakeJira(t, func(method, path string, body []byte) (int, string) {
		gotMethod, gotPath, gotBody = method, path, body
		return 204, ""
	})

	if err := j.SetFixVersion("OR-105", "500"); err != nil {
		t.Fatal(err)
	}
	if gotMethod != "PUT" || !strings.HasSuffix(gotPath, "/rest/api/3/issue/OR-105") {
		t.Errorf("wrote with %s %s, want a PUT to the issue", gotMethod, gotPath)
	}

	var payload struct {
		Fields struct {
			FixVersions []map[string]string `json:"fixVersions"`
		} `json:"fields"`
		Update map[string]any `json:"update"`
	}
	if err := json.Unmarshal(gotBody, &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Fields.FixVersions) != 1 || payload.Fields.FixVersions[0]["id"] != "500" {
		t.Errorf("payload sets %v, want exactly one entry keyed by id 500",
			payload.Fields.FixVersions)
	}
	// An `update` block with an add operation would APPEND rather than replace,
	// which is the bug this test exists to catch.
	if len(payload.Update) != 0 {
		t.Errorf("payload uses an update operation (%v), which appends rather "+
			"than replaces", payload.Update)
	}
}

func TestSetFixVersionSurfacesAFailure(t *testing.T) {
	j := fakeJira(t, func(method, path string, body []byte) (int, string) {
		return 500, `{"error":"boom"}`
	})

	err := j.SetFixVersion("OR-105", "500")
	if err == nil {
		t.Fatal("a 500 came back as success, so a ticket would be reported attached " +
			"when it was not")
	}
	if !strings.Contains(err.Error(), "OR-105") {
		t.Errorf("the error does not name the ticket: %v", err)
	}
}

// A refused write must be distinguishable from a broken one, the same way
// version creation already distinguishes it.
func TestSetFixVersionReportsAPermissionRefusal(t *testing.T) {
	j := fakeJira(t, func(method, path string, body []byte) (int, string) {
		return 403, `{"errorMessages":["forbidden"]}`
	})

	if err := j.SetFixVersion("OR-105", "500"); !errors.Is(err, ErrNoPermission) {
		t.Errorf("a 403 did not come back as ErrNoPermission: %v", err)
	}
}
