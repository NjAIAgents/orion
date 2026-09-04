package decompose

// Tests for the parts of OR-302 not already covered elsewhere in this
// package: a Jira 404 surfacing as a named-project error rather than a
// generic one, the marker coming from work.Rules() itself for every
// published rule (not a hardcoded couple of keywords), and a project with no
// Epic-level type naming what it does offer.

import (
	"net/http"
	"net/http/httptest"
	"path"
	"strings"
	"testing"

	"github.com/orion-sdlc/orion/internal/tracker"
	"github.com/orion-sdlc/orion/internal/work"
)

// A project that does not exist (or the account cannot see) 404s on the
// createmeta lookup Create makes first. That must reach the caller naming
// the project, not as "reading the issue types of X: 404 ...".
func TestJiraProjectNotFoundIsReportedNotGeneric(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"errorMessages":["No project could be found"]}`))
	}))
	defer srv.Close()

	j := &tracker.Jira{BaseURL: srv.URL, Email: "e", Token: "t"}
	b := NewJiraBackend(j)

	_, err := b.Create(CreateRequest{Project: "GONE", Kind: KindEpic, Summary: "e"})
	if err == nil {
		t.Fatal("want an error for a project Jira 404s on")
	}
	if !strings.Contains(err.Error(), "GONE") {
		t.Errorf("a 404 must name the project rather than read as a generic failure: %v", err)
	}
	if strings.Contains(err.Error(), "404") {
		t.Errorf("the operator-facing message should say what is wrong, not carry the raw\n"+
			"status code as if it were the explanation: %v", err)
	}
}

// marker() reads work.Rules() directly rather than a copy: every rule in the
// live table must route through it, not just the two or three a fixture
// happens to exercise.
func TestMarkerRoutesEveryPublishedRuleNotAFixedCopy(t *testing.T) {
	for _, r := range work.Rules() {
		kw := r.Keywords[0]
		if tokenSplit.MatchString(kw) {
			// A multi-word keyword (e.g. "front-end") cannot appear as a
			// single token in a directory name; skip it, it is still
			// reachable through one of its single-word siblings.
			continue
		}
		it := &Item{Paths: []string{path.Join(kw, "file.txt")}}
		if got := marker(signals(it)...); got != kw {
			t.Errorf("rule %q: marker(%q) = %q, want %q -- marker must answer from\n"+
				"work.Rules() itself, not a private copy of its vocabulary", r.Actor, kw, got, kw)
		}
	}
}

// A project offering no Epic-level type has no root to hang the tree off,
// and the error must name what the project DOES offer so the operator can
// act on it rather than guess.
func TestJiraNoEpicTypeListsWhatTheProjectOffers(t *testing.T) {
	noEpic := []tracker.IssueType{
		{ID: "1", Name: "Story", HierarchyLevel: 0},
		{ID: "2", Name: "Task", HierarchyLevel: 0},
	}
	f := &fakeJira{types: noEpic}
	b := NewJiraBackend(f)

	_, err := b.Create(CreateRequest{Project: "FLAT", Kind: KindEpic, Summary: "e"})
	if err == nil {
		t.Fatal("a project with no epic-level type has no root for the tree; want an error")
	}
	for _, want := range []string{"Story", "Task"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error must list what the project offers (%q) so the operator can act on\n"+
				"it rather than guess: %v", want, err)
		}
	}
}
