package web

// The run view (OR-67): tests over the served ARTIFACTS, not the rendered
// page.
//
// No JS runtime is exercised here. Testing what app.js actually draws would
// need a browser or a headless Node, and OR-71's own acceptance criterion --
// "given a machine with no Node installed, go build ./... produces a working
// binary" -- makes Node a dependency this test suite should not quietly
// acquire. What IS verifiable from Go, and what a regression here would
// actually catch: the page references the script that exists, the script
// requests the JSON shape the server actually returns, and the vendored
// runtime app.js imports is reachable at the path it names.

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

// index.html must reference a script that the embedded tree actually
// serves. A typo in the src attribute is a page that loads and does
// nothing -- no error anywhere a Go test would catch it except this one.
func TestIndexReferencesAScriptThatExists(t *testing.T) {
	rec := httptest.NewRecorder()
	Assets().ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	body := rec.Body.String()

	const want = `src="js/app.js"`
	if !strings.Contains(body, want) {
		t.Fatalf("index.html does not reference %s; got:\n%s", want, body)
	}

	rec2 := httptest.NewRecorder()
	Assets().ServeHTTP(rec2, httptest.NewRequest("GET", "/js/app.js", nil))
	if rec2.Code != 200 {
		t.Fatalf("index.html references js/app.js, which does not exist: %d", rec2.Code)
	}
}

// app.js imports the vendored runtime from /vendor/ -- that route has to
// actually serve both files OR-66 committed, or the page's very first
// import throws and nothing renders.
func TestAppJSImportsAreServed(t *testing.T) {
	rec := httptest.NewRecorder()
	Assets().ServeHTTP(rec, httptest.NewRequest("GET", "/js/app.js", nil))
	src := rec.Body.String()

	for _, want := range []string{`/vendor/preact.module.js`, `/vendor/htm.module.js`} {
		if !strings.Contains(src, want) {
			t.Errorf("app.js does not import %s", want)
			continue
		}
		vrec := httptest.NewRecorder()
		Vendored().ServeHTTP(vrec, httptest.NewRequest("GET", want, nil))
		if vrec.Code != 200 {
			t.Errorf("app.js imports %s, but /vendor/ returns %d for it", want, vrec.Code)
		}
	}
}

// app.js must not reach for the one escape hatch VENDOR.md bans (OR-289):
// dangerouslySetInnerHTML. Every value it draws is untrusted -- a ticket
// summary or an activity string from a tracker other people can write to --
// and htm/preact escape by construction only as long as nothing routes
// around that.
func TestAppJSNeverUsesDangerouslySetInnerHTML(t *testing.T) {
	rec := httptest.NewRecorder()
	Assets().ServeHTTP(rec, httptest.NewRequest("GET", "/js/app.js", nil))
	src := rec.Body.String()
	// The property USE, not the bare word: this file's own doc comment
	// names the banned API to explain why it is absent, which a substring
	// match on the word alone would misread as a violation.
	for _, line := range strings.Split(src, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "//") {
			continue
		}
		if strings.Contains(line, "dangerouslySetInnerHTML=") ||
			strings.Contains(line, "dangerouslySetInnerHTML:") {
			t.Errorf("app.js uses dangerouslySetInnerHTML -- OR-289 bans this escape "+
				"hatch across internal/web/ui/**, and the same reasoning applies here: "+
				"a ticket summary or activity line is text from a tracker other people "+
				"write to. Line: %s", trimmed)
		}
	}
}

// THE ACCEPTANCE CRITERION'S OTHER HALF: app.js requests /api/snapshot, and
// what it asks for is a shape Snapshot/Card/Session actually serialise to --
// this test fails if a Go field is ever renamed without app.js's property
// access being updated alongside it, since the two are not otherwise
// connected by the compiler.
func TestAppJSRequestsTheRealSnapshotShape(t *testing.T) {
	rec := httptest.NewRecorder()
	Assets().ServeHTTP(rec, httptest.NewRequest("GET", "/js/app.js", nil))
	src := rec.Body.String()

	if !strings.Contains(src, `fetch("/api/snapshot")`) {
		t.Fatal("app.js does not fetch /api/snapshot")
	}

	snap := Snapshot{Cards: []Card{{
		Key: "OR-1", Title: "t", Verb: "ok",
		Session: Session{Actor: "a", Role: "r", Model: "m", Steps: 3, Done: true},
	}}}
	b, err := json.Marshal(snap)
	if err != nil {
		t.Fatal(err)
	}
	// Every property app.js reads off a Card or a Session must be a real
	// field name in the JSON this produces -- PascalCase, no json tags on
	// the model types, so what app.js names has to match exactly.
	for _, field := range []string{
		`"Key"`, `"Title"`, `"Verb"`, `"Gate"`, `"Session"`,
		`"Actor"`, `"Role"`, `"Model"`, `"Steps"`, `"Activity"`,
		`"Started"`, `"Last"`, `"Done"`, `"Cards"`,
	} {
		if !strings.Contains(string(b), field) {
			t.Fatalf("Snapshot no longer serialises %s; app.js reads this field and will "+
				"silently render undefined if it is renamed", field)
		}
	}
}

// A completed card keeps its status word rather than disappearing -- the
// mockup's own rule ("completed cards keep a tick... the run's shape is the
// useful part") and OR-67's own done-when. Checked at the source-text level:
// app.js must render Verb (which carries "ok" for a finished card) rather
// than filtering finished cards out of the list before drawing it.
func TestCompletedCardsAreNotFilteredOut(t *testing.T) {
	rec := httptest.NewRecorder()
	Assets().ServeHTTP(rec, httptest.NewRequest("GET", "/js/app.js", nil))
	src := rec.Body.String()

	// The thing OR-67's done-when actually forbids: whatever list is handed
	// to the per-card render call (".map(") must be the plain card list, not
	// a filtered one. A filter is legitimate elsewhere in this file (the
	// done/active COUNTS in the header) -- it is only wrong on the list that
	// becomes cards on screen.
	i := strings.Index(src, ".map(")
	if i == -1 {
		t.Fatal("no .map( call found; nothing renders the card list at all")
	}
	// The receiver expression before .map( -- back to the start of the
	// enclosing statement (a newline), so a chained call like
	// "cards.filter(...).map(...)" is caught and not just the identifier
	// immediately adjacent to ".map(".
	lineStart := strings.LastIndexByte(src[:i], '\n')
	receiver := src[lineStart+1 : i]
	if strings.Contains(receiver, "filter") {
		t.Errorf("the card grid maps over %q, which looks filtered; completed cards "+
			"must keep their tick, not disappear (OR-67's done-when)", receiver)
	}

	if !strings.Contains(src, "verbIcon") || !strings.Contains(src, "ok:") {
		t.Error("no icon mapping for the ok verb found; a completed card has nothing to " +
			"draw its tick with")
	}
}
