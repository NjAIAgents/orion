package tracker

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestIssueTypesReadsWhatTheProjectOffers(t *testing.T) {
	j := fakeJira(t, func(method, path string, _ []byte) (int, string) {
		if method != "GET" || !strings.Contains(path, "/createmeta/CAT/issuetypes") {
			t.Errorf("unexpected request: %s %s", method, path)
		}
		return 200, `{"issueTypes":[
		  {"id":"10000","name":"Epic","subtask":false,"hierarchyLevel":1},
		  {"id":"10003","name":"Subtask","subtask":true,"hierarchyLevel":-1}]}`
	})

	got, err := j.IssueTypes("CAT")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].ID != "10000" || got[0].HierarchyLevel != 1 {
		t.Fatalf("got %+v", got)
	}
	if !got[1].Subtask {
		t.Error("the subtask flag decides whether an issue can take a Story as its parent")
	}
}

func TestIssueTypesSaysWhichProjectIsMissing(t *testing.T) {
	j := fakeJira(t, func(string, string, []byte) (int, string) { return 404, `{"errorMessages":["no"]}` })
	_, err := j.IssueTypes("NOPE")
	if err == nil || !strings.Contains(err.Error(), "NOPE") {
		t.Errorf("a 404 must name the project rather than read as a generic failure: %v", err)
	}
}

func TestCreateIssueSendsTheHierarchyAndReturnsTheKey(t *testing.T) {
	var sent map[string]any
	j := fakeJira(t, func(method, path string, body []byte) (int, string) {
		if method != "POST" || path != "/rest/api/3/issue" {
			t.Errorf("unexpected request: %s %s", method, path)
		}
		var env struct{ Fields map[string]any }
		if err := json.Unmarshal(body, &env); err != nil {
			t.Fatal(err)
		}
		sent = env.Fields
		return 201, `{"id":"1","key":"CAT-7"}`
	})

	key, err := j.CreateIssue(NewIssue{
		Project: "CAT", TypeID: "10003", Summary: "T004 Implement listing",
		Description: "Files: internal/catalogue/list.go",
		Labels:      []string{"orion-spec-product-catalogue", "documentation"},
		ParentKey:   "CAT-3",
	})
	if err != nil {
		t.Fatal(err)
	}
	if key != "CAT-7" {
		t.Errorf("key = %q, want the one Jira returned", key)
	}
	if got := sent["parent"].(map[string]any)["key"]; got != "CAT-3" {
		t.Errorf("parent = %v: a child that does not carry its parent is not in the tree", got)
	}
	if got := sent["issuetype"].(map[string]any)["id"]; got != "10003" {
		t.Errorf("issuetype = %v, want the id the mapping resolved", got)
	}
	// The description must arrive as a document, not as a raw string: Jira
	// Cloud rejects a string, and the file paths live in here.
	doc, ok := sent["description"].(map[string]any)
	if !ok || doc["type"] != "doc" {
		t.Fatalf("description = %#v, want Atlassian Document Format", sent["description"])
	}
	if !strings.Contains(string(mustJSON(t, doc)), "internal/catalogue/list.go") {
		t.Error("the exact file paths must survive into the description")
	}
}

// Jira rejects a label containing whitespace with a message about the field
// rather than about the value, which reads as "creation failed" for no
// visible reason.
func TestCreateIssueMakesLabelsAcceptable(t *testing.T) {
	var sent map[string]any
	j := fakeJira(t, func(_, _ string, body []byte) (int, string) {
		var env struct{ Fields map[string]any }
		_ = json.Unmarshal(body, &env)
		sent = env.Fields
		return 201, `{"key":"CAT-1"}`
	})
	if _, err := j.CreateIssue(NewIssue{
		Project: "CAT", TypeID: "1", Summary: "s", Labels: []string{"needs design"}}); err != nil {
		t.Fatal(err)
	}
	if got := sent["labels"].([]any)[0]; got != "needs-design" {
		t.Errorf("label = %v, want the space closed up", got)
	}
}

// A summary over Jira's 255-character cap is refused outright. Losing the
// whole tree to one long task line would be absurd when the full text is in
// the description either way.
func TestCreateIssueCapsTheSummary(t *testing.T) {
	var sent map[string]any
	j := fakeJira(t, func(_, _ string, body []byte) (int, string) {
		var env struct{ Fields map[string]any }
		_ = json.Unmarshal(body, &env)
		sent = env.Fields
		return 201, `{"key":"CAT-1"}`
	})
	long := "T001 " + strings.Repeat("x", 400)
	if _, err := j.CreateIssue(NewIssue{Project: "CAT", TypeID: "1", Summary: long}); err != nil {
		t.Fatal(err)
	}
	got := sent["summary"].(string)
	if n := len([]rune(got)); n != 255 {
		t.Errorf("summary is %d characters, want it capped at 255", n)
	}
	if !strings.HasPrefix(got, "T001 ") {
		t.Error("the id leads the summary and must survive the cap")
	}
}

func TestCreateIssuePassesJirasRefusalThrough(t *testing.T) {
	j := fakeJira(t, func(string, string, []byte) (int, string) {
		return 400, `{"errors":{"parent":"Issue type is a sub-task but parent issue is not set"}}`
	})
	_, err := j.CreateIssue(NewIssue{Project: "CAT", TypeID: "1", Summary: "s"})
	if err == nil || !strings.Contains(err.Error(), "parent") {
		t.Errorf("Jira names the field it objected to, and that is the difference between\n"+
			"a fixable error and \"creation failed\": %v", err)
	}
}

func TestCreateIssueRefusesAnIncompleteRequest(t *testing.T) {
	j := fakeJira(t, func(string, string, []byte) (int, string) {
		t.Error("a request missing a project or a type must not reach Jira")
		return 201, `{"key":"X-1"}`
	})
	for _, in := range []NewIssue{
		{TypeID: "1", Summary: "s"},
		{Project: "CAT", Summary: "s"},
		{Project: "CAT", TypeID: "1"},
	} {
		if _, err := j.CreateIssue(in); err == nil {
			t.Errorf("want an error for %+v", in)
		}
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
