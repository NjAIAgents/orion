package web

// The output-escaping contract for the board (OR-276).
//
// NOTHING A CARD CARRIES IS TRUSTED INPUT. Key and Title come from a tracker
// anyone with a Jira licence can type into; Activity and Gate come off an
// event log that agents and other tooling write; a branch name is whatever
// somebody pushed. The board shows all of it, and once the write endpoints
// exist the board is the control plane -- so a summary reading
// `<img src=x onerror=...>` that executes is script running with the operator's
// own origin, not a cosmetic bug.
//
// THE RULE: every server-rendered path uses html/template's contextual
// escaping, and no scanned value is converted to template.HTML, template.JS,
// template.URL, template.CSS or template.Srcset. Those types are not a
// stronger string; they are the words "I have already escaped this", and the
// one thing nobody can say about a tracker summary is that. text/template is
// banned outright for the same reason: it escapes nothing, and its templates
// look identical to html/template's on the page that reviews them.
//
// The client-rendered half of the same rule -- values set as vdom children,
// never innerHTML -- is OR-289's, enforced over internal/web/ui; stderr is
// OR-285's. This file is the server half, and it is two tests because the rule
// has two ways to break: the escaping stops happening (the render test), or
// someone reintroduces the escape hatch (the source guard).

import (
	"bytes"
	"go/format"
	"go/parser"
	"go/token"
	"html"
	"html/template"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/orion-sdlc/orion/internal/events"
)

// The summary a tracker will accept without complaint. It carries both halves
// of the problem: a tag that executes on load, and a quote that closes an
// attribute so a value interpolated into one can break out of it.
const escapePayload = `<img src=x onerror="alert(1)">`

// The shape a card is drawn in: the untrusted fields in element text, and
// Title again inside an attribute, because the two are different escaping
// contexts and a template that is safe in one is not automatically safe in the
// other. Declared here rather than in a renderer because there is no
// server-rendered board yet -- this pins the contract the first one inherits.
var cardTemplate = template.Must(template.New("card").Parse(
	`<article class="card" title="{{.Title}}">` +
		`<h2>{{.Key}}</h2>` +
		`<p class="summary">{{.Title}}</p>` +
		`<p class="gate">{{.Gate}}</p>` +
		`<p class="activity">{{.Session.Activity}}</p>` +
		`</article>`))

// OR-276's done-when: a ticket summary containing markup appears as literal
// text.
//
// Activity is taken from a real Scan rather than assigned, so the value under
// test has travelled the path a live one does -- log line to event to card --
// and a Card field retyped to template.HTML anywhere along it fails here
// rather than passing as "already safe".
func TestATicketSummaryContainingMarkupRendersAsLiteralText(t *testing.T) {
	cards := Scan([]events.Event{
		{At: base, Kind: events.KindRunStart, Key: "OR-276", Run: "r1"},
		{At: base.Add(time.Minute), Kind: events.KindSay, Key: "OR-276", Run: "r1", Msg: escapePayload},
	}, nil)
	if got, want := len(cards), 1; got != want {
		t.Fatalf("Scan returned %d cards, want %d", got, want)
	}

	card := cards[0]
	if got := card.Session.Activity; got != escapePayload {
		t.Fatalf("fixture is not exercising the rule: Scan carried Activity = %q, want the payload %q", got, escapePayload)
	}
	// Title and Gate are the tracker's words and the supervisor's, neither of
	// which Scan fills; they are as untrusted as the activity beside them.
	card.Title = escapePayload
	card.Gate = escapePayload

	var buf bytes.Buffer
	if err := cardTemplate.Execute(&buf, card); err != nil {
		t.Fatalf("render card: %v", err)
	}
	out := buf.String()

	// The tag must not survive into the markup in any form a browser would
	// parse as a tag -- in the attribute or in the text, opened by the payload
	// or by a quote it closed.
	for _, raw := range []string{"<img", `onerror="alert(1)"`, "<script"} {
		if strings.Contains(out, raw) {
			t.Errorf("rendered card contains %q, so the payload reached the browser as markup:\n%s", raw, out)
		}
	}

	// Present as text, and present in every field -- Title twice (attribute
	// and element), Gate and Activity once each. Absence would pass the check
	// above while silently dropping the summary, which is a different bug
	// wearing this test's green.
	if got, want := strings.Count(html.UnescapeString(out), escapePayload), 4; got != want {
		t.Errorf("payload appears as literal text %d times, want %d:\n%s", got, want, out)
	}
}

// The render test above proves the rule for Title, Gate and Activity, but
// leaves Key untested with the payload -- Scan fills it from the tracker key
// ("OR-276"), so a card built from a live event never carries the payload
// there. Key is exactly as untrusted as the rest (model.go says so directly),
// and it sits in its own element, "<h2>{{.Key}}</h2>", so a template safe for
// Title says nothing about whether Key is escaped too.
func TestATicketKeyContainingMarkupRendersAsLiteralText(t *testing.T) {
	card := Card{Key: escapePayload}

	var buf bytes.Buffer
	if err := cardTemplate.Execute(&buf, card); err != nil {
		t.Fatalf("render card: %v", err)
	}
	out := buf.String()

	for _, raw := range []string{"<img", `onerror="alert(1)"`, "<script"} {
		if strings.Contains(out, raw) {
			t.Errorf("rendered card contains %q, so the Key payload reached the browser as markup:\n%s", raw, out)
		}
	}
	if !strings.Contains(html.UnescapeString(out), escapePayload) {
		t.Errorf("Key payload does not appear as literal text; rendered card:\n%s", out)
	}
}

// The escape hatch, closed. The render test proves html/template escapes; it
// cannot prove the board still asks it to, because a value converted to
// template.HTML before it is rendered is emitted verbatim by exactly the same
// template.
//
// Source-scanned rather than type-checked so it covers a conversion, a struct
// field and a function signature alike, and so it reports the file to open
// rather than a failure somewhere downstream. Comments are stripped before the
// scan -- the rule has to be writable down in the prose that explains it, and a
// guard that fails on its own documentation gets deleted rather than obeyed.
func TestNoWebSourceMakesAScannedValueRawMarkup(t *testing.T) {
	banned := []struct{ pattern, why string }{
		{`"text/template"`, "text/template escapes nothing; the board's server-rendered paths use html/template"},
		{"template.HTML", "template.HTML asserts a value is already-safe markup, which no tracker or log value is"},
		{"template.JS", "template.JS asserts a value is already-safe script, which no tracker or log value is"},
		{"template.URL", "template.URL skips URL escaping, so a javascript: summary becomes a live link"},
		{"template.CSS", "template.CSS asserts a value is already-safe style"},
		{"template.Srcset", "template.Srcset skips srcset escaping"},
	}

	scanned := 0
	err := filepath.WalkDir(".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		code, cerr := codeWithoutComments(path)
		if cerr != nil {
			return cerr
		}
		scanned++
		for _, ban := range banned {
			if strings.Contains(code, ban.pattern) {
				t.Errorf("%s uses %s. %s. Render the value with html/template and let it escape (OR-276).",
					path, ban.pattern, ban.why)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk web sources: %v", err)
	}
	if scanned == 0 {
		t.Fatal("no non-test Go file was scanned; this guard would pass on an empty package")
	}
}

// codeWithoutComments is one Go file printed back out with its comments
// dropped: parsed without ParseComments, so what returns is what the compiler
// sees and nothing a person wrote about it.
func codeWithoutComments(path string) (string, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		return "", err
	}
	var buf bytes.Buffer
	if err := format.Node(&buf, fset, f); err != nil {
		return "", err
	}
	return buf.String(), nil
}
