package tracker

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Jira Cloud returns descriptions as a nested document, not a string. An
// agent handed the raw JSON would be reading markup instead of the
// requirement, so the tree has to be flattened -- and the bullets kept,
// because acceptance criteria arrive as a list of separate conditions.
func TestFlattenADFKeepsListItemsDistinct(t *testing.T) {
	doc := `{"type":"doc","version":1,"content":[
	  {"type":"paragraph","content":[{"type":"text","text":"Expose GET /healthz."}]},
	  {"type":"paragraph","content":[{"type":"text","text":"Acceptance:"}]},
	  {"type":"bulletList","content":[
	    {"type":"listItem","content":[{"type":"paragraph","content":[{"type":"text","text":"200 with a sha"}]}]},
	    {"type":"listItem","content":[{"type":"paragraph","content":[{"type":"text","text":"under 50ms"}]}]},
	    {"type":"listItem","content":[{"type":"paragraph","content":[{"type":"text","text":"covered by a test"}]}]}
	  ]}
	]}`
	got := flattenADF(json.RawMessage(doc))

	for _, want := range []string{"- 200 with a sha", "- under 50ms", "- covered by a test"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	if !strings.HasPrefix(got, "Expose GET /healthz.") {
		t.Errorf("prose lost:\n%s", got)
	}
	if strings.Contains(got, "\"type\"") {
		t.Errorf("raw ADF leaked into the text:\n%s", got)
	}
}

// Some API paths still return a plain string. Treating that as a parse
// failure would silently blank the requirement.
func TestFlattenADFAcceptsAPlainString(t *testing.T) {
	if got := flattenADF(json.RawMessage(`"just text"`)); got != "just text" {
		t.Errorf("got %q", got)
	}
	if got := flattenADF(json.RawMessage(`null`)); got != "" {
		t.Errorf("null description should be empty, got %q", got)
	}
	if got := flattenADF(nil); got != "" {
		t.Errorf("absent description should be empty, got %q", got)
	}
}

func TestADFParagraphsRoundTrips(t *testing.T) {
	doc := adfParagraphs("first line\n\nthird line")
	b, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	back := flattenADF(b)
	if !strings.Contains(back, "first line") || !strings.Contains(back, "third line") {
		t.Errorf("round trip lost content: %q", back)
	}
}

// Labels are sent as add/remove operations, never as a whole list. Writing
// the full set would clobber a label added while Orion was working -- and
// the queue label is how a human cancels a job mid-flight.
func TestSetLabelsSendsOperationsNotAWholeList(t *testing.T) {
	var got map[string]any
	j := fakeJira(t, func(method, path string, body []byte) (int, string) {
		_ = json.Unmarshal(body, &got)
		return 204, `{}`
	})
	if err := j.SetLabels("FCIA-1", []string{"orion-working"}, []string{"ORION"}); err != nil {
		t.Fatal(err)
	}
	upd, ok := got["update"].(map[string]any)
	if !ok {
		t.Fatalf("payload has no update block: %v", got)
	}
	ops, _ := upd["labels"].([]any)
	if len(ops) != 2 {
		t.Fatalf("expected an add and a remove, got %v", ops)
	}
	if _, isSet := got["fields"]; isSet {
		t.Error("a fields block would overwrite the whole label set")
	}
}

// The claim is assigned to whoever the credentials belong to -- the bot when
// there is a bot account, the operator otherwise -- so the account id is
// READ from the tracker rather than configured anywhere (OR-34).
func TestAssignSelfSendsTheAuthenticatedAccount(t *testing.T) {
	var putPath string
	var putBody map[string]any
	j := fakeJira(t, func(method, path string, body []byte) (int, string) {
		if method == "GET" && strings.HasSuffix(path, "/myself") {
			return 200, `{"accountId":"5b10a2","displayName":"Orion Bot"}`
		}
		putPath = path
		_ = json.Unmarshal(body, &putBody)
		return 204, `{}`
	})
	if err := j.AssignSelf("FCIA-1"); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(putPath, "/issue/FCIA-1/assignee") {
		t.Errorf("assigned via %q; the assignee endpoint needs only the assign "+
			"permission, an edit also needs the field on the edit screen", putPath)
	}
	if putBody["accountId"] != "5b10a2" {
		t.Errorf("body = %v, want the account /myself named", putBody)
	}
}

// An account that cannot be resolved must be an error the caller can report,
// not an assignment of the ticket to nobody.
func TestAssignSelfFailsRatherThanAssigningNobody(t *testing.T) {
	assigned := false
	j := fakeJira(t, func(method, path string, body []byte) (int, string) {
		if method == "GET" && strings.HasSuffix(path, "/myself") {
			return 401, `{"message":"unauthorized"}`
		}
		assigned = true
		return 204, `{}`
	})
	if err := j.AssignSelf("FCIA-1"); err == nil {
		t.Error("a rejected /myself was reported as a successful assignment")
	}
	if assigned {
		t.Error("the ticket was written to with no account resolved")
	}
}

// A malformed /myself body -- Jira returning 200 with something that is not
// the expected JSON shape -- must fail descriptively rather than panic or
// silently resolve an empty account id.
func TestSelfMalformedJSONFailsDescriptively(t *testing.T) {
	assigned := false
	j := fakeJira(t, func(method, path string, body []byte) (int, string) {
		if method == "GET" && strings.HasSuffix(path, "/myself") {
			return 200, `not json`
		}
		assigned = true
		return 204, `{}`
	})
	err := j.AssignSelf("FCIA-1")
	if err == nil {
		t.Fatal("a malformed /myself body was reported as a successful assignment")
	}
	if !strings.Contains(err.Error(), "resolving the authenticated account") {
		t.Errorf("error = %q, want it to name what failed", err.Error())
	}
	if assigned {
		t.Error("the ticket was written to with no account resolved")
	}
}

// A tracker that resolves the account fine but refuses the assignment itself
// -- no Assign Issues permission, a deactivated account -- must still fail
// descriptively; the caller (work.one) is the one that turns this into a
// warning rather than a blocked run.
func TestAssignSelfPermissionDeniedFailsDescriptively(t *testing.T) {
	j := fakeJira(t, func(method, path string, body []byte) (int, string) {
		if method == "GET" && strings.HasSuffix(path, "/myself") {
			return 200, `{"accountId":"5b10a2"}`
		}
		return 403, `{"errorMessages":["You do not have permission to assign issues."]}`
	})
	err := j.AssignSelf("FCIA-1")
	if err == nil {
		t.Fatal("a 403 from the assignee endpoint was reported as success")
	}
	if !strings.Contains(err.Error(), "FCIA-1") || !strings.Contains(err.Error(), "403") {
		t.Errorf("error = %q, want the ticket and status code named", err.Error())
	}
}

func TestSetLabelsNoOpsMakesNoRequest(t *testing.T) {
	called := false
	j := fakeJira(t, func(method, path string, body []byte) (int, string) {
		called = true
		return 204, `{}`
	})
	if err := j.SetLabels("FCIA-1", nil, []string{"  "}); err != nil {
		t.Fatal(err)
	}
	if called {
		t.Error("nothing to change should not cost a request")
	}
}

// Workflows name the same move differently ("Start Progress" vs "In
// Progress"). Matching only the transition NAME works on one project and
// silently does nothing on the next, so the destination status counts too.
func TestTransitionToMatchesDestinationStatus(t *testing.T) {
	var posted string
	j := fakeJira(t, func(method, path string, body []byte) (int, string) {
		if method == "GET" {
			return 200, `{"transitions":[
			  {"id":"11","name":"Start Progress","to":{"name":"In Progress"}},
			  {"id":"21","name":"Done","to":{"name":"Done"}}]}`
		}
		posted = string(body)
		return 204, `{}`
	})
	if err := j.TransitionTo("FCIA-1", "in progress"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(posted, `"id":"11"`) {
		t.Errorf("posted %q, want the Start Progress transition", posted)
	}
}

// A failure here must name the options, or the user is guessing at their
// own workflow's vocabulary.
func TestTransitionToListsWhatIsAvailable(t *testing.T) {
	j := fakeJira(t, func(method, path string, body []byte) (int, string) {
		return 200, `{"transitions":[{"id":"11","name":"Start Progress","to":{"name":"In Progress"}}]}`
	})
	err := j.TransitionTo("FCIA-1", "Deployed")
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "Start Progress") || !strings.Contains(err.Error(), "In Progress") {
		t.Errorf("error should list the real options, got: %v", err)
	}
}

func TestSearchSurfacesJiraExplanationForBadJQL(t *testing.T) {
	j := fakeJira(t, func(method, path string, body []byte) (int, string) {
		return 400, `{"errorMessages":["Field 'nope' does not exist"]}`
	})
	_, err := j.Search("nope = 1", 10)
	if err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Errorf("Jira's explanation must reach the user, got: %v", err)
	}
}

func TestSearchParsesIssuesInOrder(t *testing.T) {
	j := fakeJira(t, func(method, path string, body []byte) (int, string) {
		if !strings.Contains(path, "maxResults=100") {
			t.Errorf("maxResults not capped in %s", path)
		}
		return 200, `{"issues":[
		  {"key":"FCIA-9","fields":{"summary":"first","labels":["ORION"],"status":{"name":"To Do"},
		    "description":{"type":"doc","content":[{"type":"paragraph","content":[{"type":"text","text":"do a thing"}]}]}}},
		  {"key":"FCIA-3","fields":{"summary":"second","labels":[],"status":{"name":"To Do"}}}]}`
	})
	is, err := j.Search("labels = ORION ORDER BY Rank ASC", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(is) != 2 || is[0].Key != "FCIA-9" || is[1].Key != "FCIA-3" {
		t.Fatalf("order not preserved: %+v", is)
	}
	if is[0].Description != "do a thing" {
		t.Errorf("description = %q", is[0].Description)
	}
	if !strings.HasSuffix(is[0].URL, "/browse/FCIA-9") {
		t.Errorf("URL = %q, want a browse link", is[0].URL)
	}
}

// OR-171's router reads Issue.IssueType and Issue.Components -- route_test.go
// proves the router itself, but only by constructing tracker.Issue directly,
// which never exercises the JSON parsing that has to populate those fields
// from a real Jira response. A wrong field tag here would leave every ticket
// routing to the implementer in production while every existing test stays
// green, since nothing else feeds a real payload through this parser.
func TestGetIssueParsesIssueTypeAndComponents(t *testing.T) {
	j := fakeJira(t, func(method, path string, body []byte) (int, string) {
		return 200, `{"key":"FCIA-9","fields":{"summary":"write the docs","labels":["documentation"],
		  "status":{"name":"To Do"},
		  "issuetype":{"name":"Documentation"},
		  "components":[{"name":"docs-site"},{"name":"frontend"}]}}`
	})
	i, err := j.GetIssue("FCIA-9")
	if err != nil {
		t.Fatal(err)
	}
	if i.IssueType != "Documentation" {
		t.Errorf("IssueType = %q, want %q", i.IssueType, "Documentation")
	}
	if len(i.Components) != 2 || i.Components[0] != "docs-site" || i.Components[1] != "frontend" {
		t.Errorf("Components = %v, want [docs-site frontend]", i.Components)
	}
}

// GetIssue must not report a component or issue type that Jira did not send:
// an empty issuetype/components block must parse to zero values, not a stale
// or zero-valued placeholder that then matches a route by accident.
func TestGetIssueLeavesIssueTypeAndComponentsEmptyWhenAbsent(t *testing.T) {
	j := fakeJira(t, func(method, path string, body []byte) (int, string) {
		return 200, `{"key":"FCIA-1","fields":{"summary":"fix the rounding bug","status":{"name":"To Do"}}}`
	})
	i, err := j.GetIssue("FCIA-1")
	if err != nil {
		t.Fatal(err)
	}
	if i.IssueType != "" {
		t.Errorf("IssueType = %q, want empty", i.IssueType)
	}
	if len(i.Components) != 0 {
		t.Errorf("Components = %v, want none", i.Components)
	}
}

// Search feeds the same router when a ticket is picked up from the queue
// rather than fetched by key, and it hits its own struct for the fields
// block -- so the same parsing has to be proven here separately from GetIssue.
func TestSearchParsesIssueTypeAndComponents(t *testing.T) {
	j := fakeJira(t, func(method, path string, body []byte) (int, string) {
		if !strings.Contains(path, "fields=") || !strings.Contains(path, "issuetype") ||
			!strings.Contains(path, "components") {
			t.Errorf("the fields query did not ask Jira for issuetype/components: %s", path)
		}
		return 200, `{"issues":[
		  {"key":"FCIA-4","fields":{"summary":"restyle the button","labels":["ui"],"status":{"name":"To Do"},
		    "issuetype":{"name":"Story"},
		    "components":[{"name":"frontend"}]}}]}`
	})
	is, err := j.Search("labels = ORION", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(is) != 1 {
		t.Fatalf("got %d issues", len(is))
	}
	if is[0].IssueType != "Story" {
		t.Errorf("IssueType = %q, want %q", is[0].IssueType, "Story")
	}
	if len(is[0].Components) != 1 || is[0].Components[0] != "frontend" {
		t.Errorf("Components = %v, want [frontend]", is[0].Components)
	}
}

// fakeJira points a client at a test server. handler receives the method,
// the request path with query, and the body, and returns a status and body.
func fakeJira(t *testing.T, handler func(method, path string, body []byte) (int, string)) *Jira {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		path := r.URL.Path
		if r.URL.RawQuery != "" {
			path += "?" + r.URL.RawQuery
		}
		code, body := handler(r.Method, path, b)
		w.WriteHeader(code)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return &Jira{BaseURL: srv.URL, Email: "e", Token: "t", client: srv.Client()}
}

// A ticket can briefly carry two labels if a write was interrupted between
// the remove and the add. State must report the more urgent one: failed
// outranks everything, and an agent still running outranks a pull request
// that may already be stale.
func TestStatePrecedenceUnderPartialWrites(t *testing.T) {
	for _, tc := range []struct {
		labels []string
		want   string
	}{
		{[]string{"ORION"}, "queued"},
		{[]string{LabelWorking}, "working"},
		{[]string{LabelCIWait}, "ci-wait"},
		{[]string{LabelFailed}, "failed"},
		{[]string{LabelWorking, LabelCIWait}, "working"},
		{[]string{LabelCIWait, "ORION"}, "ci-wait"},
		{[]string{LabelFailed, LabelWorking, LabelCIWait, "ORION"}, "failed"},
		{[]string{"unrelated"}, ""},
		{nil, ""},
		// Jira lowercases nothing, but people type labels by hand.
		{[]string{"orion"}, "queued"},
	} {
		if got := State(tc.labels, "ORION"); got != tc.want {
			t.Errorf("State(%v) = %q, want %q", tc.labels, got, tc.want)
		}
	}
}

// Managed must cover every label Orion writes. A label missing here would
// drop off the queue view and, worse, survive a requeue -- leaving a ticket
// that looks queued but still carries a terminal state.
func TestManagedCoversEveryOrionLabel(t *testing.T) {
	m := Managed("ORION")
	for _, want := range []string{"ORION", LabelWorking, LabelCIWait, LabelReady, LabelFailed} {
		found := false
		for _, g := range m {
			if g == want {
				found = true
			}
		}
		if !found {
			t.Errorf("Managed() is missing %q", want)
		}
	}
	// Five since OR-253 added orion-ready: the integration queue's inbox is a
	// label Orion owns and must clear when a ticket finishes or is requeued,
	// exactly like the other four. The count is asserted because an entry
	// added carelessly widens the queue query, and one forgotten leaves a
	// label nothing ever clears.
	if len(m) != 5 {
		t.Errorf("Managed() = %v; an extra entry would widen the queue query", m)
	}
}

// A finished ticket still holding the claim lock stops the whole queue and
// looks exactly like a job that is genuinely running. `orion queue` has to
// name that rather than print a "working" line whose status reads Done and
// leave the reader to reconcile the two (OR-125).
func TestStaleLocksNamesFinishedTicketsThatStillHoldTheClaim(t *testing.T) {
	got := StaleLocks([]Issue{
		{Key: "OR-124", StatusCategory: "Done", Labels: []string{LabelWorking}},
		{Key: "OR-130", StatusCategory: "indeterminate", Labels: []string{LabelWorking}},
		{Key: "OR-131", StatusCategory: "Done", Labels: []string{LabelCIWait}},
		{Key: "OR-132", StatusCategory: "Done"},
	})
	if len(got) != 1 || got[0] != "OR-124" {
		t.Errorf("StaleLocks = %v, want only the Done ticket holding %s", got, LabelWorking)
	}
}

// Labels are free text and their case is not guaranteed. A mismatch on case
// alone would report a wedged queue as healthy.
func TestStaleLocksIgnoresLabelCase(t *testing.T) {
	if got := StaleLocks([]Issue{
		{Key: "OR-124", StatusCategory: "done", Labels: []string{"Orion-Working"}},
	}); len(got) != 1 {
		t.Errorf("StaleLocks = %v; the lock was missed on case alone", got)
	}
}
