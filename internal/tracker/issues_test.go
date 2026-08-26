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
