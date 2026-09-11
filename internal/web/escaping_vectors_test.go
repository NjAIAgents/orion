package web

// More of OR-276's done-when, alongside escaping_test.go's two structural
// tests: this file exercises the render path against a spread of concrete
// payloads and edge-case field values rather than the single canonical
// payload above -- multiple XSS vectors in one card, a payload built to break
// out of the attribute Title renders into twice, and the empty/nil/
// already-escaped values a real log will eventually produce.

import (
	"bytes"
	"html"
	"strings"
	"testing"
	"time"

	"github.com/orion-sdlc/orion/internal/events"
)

// A card whose summary, gate and activity each carry a different XSS vector.
// One canonical payload (escapePayload, above) proves the contract holds for
// one shape of attack; a tracker summary is not limited to one shape, and
// html/template's contextual escaping has to hold for all of them or the
// contract is really "escapes img tags."
func TestMultipleXSSVectorsAllEscape(t *testing.T) {
	vectors := []string{
		`<img src=x onerror="alert(1)">`,
		`<div onclick="alert(2)">click</div>`,
		`<script>alert(3)</script>`,
		`javascript:alert(4)`,
	}

	cards := Scan([]events.Event{
		{At: base, Kind: events.KindRunStart, Key: "OR-276", Run: "r1"},
		{At: base.Add(time.Minute), Kind: events.KindSay, Key: "OR-276", Run: "r1", Msg: vectors[2]},
	}, nil)
	if got, want := len(cards), 1; got != want {
		t.Fatalf("Scan returned %d cards, want %d", got, want)
	}
	card := cards[0]
	card.Title = vectors[0]
	card.Gate = vectors[1]
	// Activity already carries vectors[2] from the Scan above.
	if got := card.Session.Activity; got != vectors[2] {
		t.Fatalf("fixture is not exercising the rule: Scan carried Activity = %q, want %q", got, vectors[2])
	}

	var buf bytes.Buffer
	if err := cardTemplate.Execute(&buf, card); err != nil {
		t.Fatalf("render card: %v", err)
	}
	out := buf.String()

	raw := []string{"<img", "<div onclick", "<script>", `href="javascript:`}
	for _, r := range raw {
		if strings.Contains(out, r) {
			t.Errorf("rendered card contains %q, so a vector reached the browser as markup:\n%s", r, out)
		}
	}

	unescaped := html.UnescapeString(out)
	for _, v := range vectors[:3] {
		if !strings.Contains(unescaped, v) {
			t.Errorf("payload %q did not reach the markup as literal text at all:\n%s", v, out)
		}
	}
}

// A payload built to close the attribute Title is rendered into, not to
// inject a tag: {{.Title}} inside title="{{.Title}}" has to escape the quote
// itself, or the attribute boundary breaks and everything after it -- up to
// the next quote -- becomes attacker-controlled attribute soup.
func TestQuoteBreakingAttributePayloadDoesNotEscapeTheBoundary(t *testing.T) {
	const payload = `" onfocus="alert(1)" autofocus="`

	card := Card{Key: "OR-276", Title: payload, Gate: payload}

	var buf bytes.Buffer
	if err := cardTemplate.Execute(&buf, card); err != nil {
		t.Fatalf("render card: %v", err)
	}
	out := buf.String()

	if strings.Contains(out, `onfocus="alert(1)"`) {
		t.Errorf("rendered card contains an unescaped onfocus attribute, so the payload closed the title attribute:\n%s", out)
	}
	if !strings.Contains(out, `title="`) {
		t.Fatalf("rendered card lost the title attribute entirely:\n%s", out)
	}
	// The attribute has to still be a single, well-formed title="..." run --
	// the payload's quotes escaped, not left to open and close their own.
	if !strings.Contains(out, `title="&#34; onfocus=&#34;alert(1)&#34; autofocus=&#34;"`) {
		t.Errorf("title attribute was not the payload's quotes escaped in place:\n%s", out)
	}
}

// Empty strings are the common case, not an edge case -- Gate is empty on
// every card with an agent running, and Activity is empty on any card whose
// agent hasn't spoken yet. The template must not error or panic on them.
func TestEmptyStringFieldsRenderWithoutError(t *testing.T) {
	card := Card{Key: "OR-276", Title: "", Gate: "", Session: Session{Activity: ""}}

	var buf bytes.Buffer
	if err := cardTemplate.Execute(&buf, card); err != nil {
		t.Fatalf("render card with empty fields: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, `title=""`) {
		t.Errorf("rendered card did not carry an empty title attribute:\n%s", out)
	}
}

// Card is a value type -- Session and its strings are always the zero value
// at worst, never a nil pointer -- so this pins that the zero Card renders
// cleanly rather than panicking, the nil/null case a value type can still
// have: an un-scanned Card{} is exactly what a ticket with no session looks
// like today.
func TestNilFieldsRenderWithoutPanic(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("rendering the zero Card panicked: %v", r)
		}
	}()

	var card Card // zero value: every string field "", Session the zero Session.

	var buf bytes.Buffer
	if err := cardTemplate.Execute(&buf, card); err != nil {
		t.Fatalf("render zero-value card: %v", err)
	}
}

// A summary that already contains an HTML entity -- because whoever wrote it
// typed "&lt;" on purpose, or because it round-tripped through a system that
// already escaped it once -- must come out escaped exactly once. Double-
// escaping ("&amp;lt;") is a different bug from not escaping at all, but it
// is still wrong: the card would show the reader "&lt;" instead of "<" or
// "&lt;", neither of which is what was typed.
func TestFieldsWithExistingHTMLEntitiesEscapeWithoutDoubleEscaping(t *testing.T) {
	const withEntity = `price &lt; 10`

	card := Card{Key: "OR-276", Title: withEntity, Gate: withEntity}

	var buf bytes.Buffer
	if err := cardTemplate.Execute(&buf, card); err != nil {
		t.Fatalf("render card: %v", err)
	}
	out := buf.String()

	// The input is the four literal characters "&lt;", not an actual "<".
	// Escaping it once turns only the "&" into "&amp;", giving "&amp;lt;" --
	// that is correct single-escaping, not a bug. Double-escaping would
	// instead escape that already-produced "&amp;" a second time, into
	// "&amp;amp;lt;".
	if strings.Contains(out, "&amp;amp;lt;") {
		t.Errorf("rendered card double-escaped the entity in the input:\n%s", out)
	}
	if !strings.Contains(out, "price &amp;lt; 10") {
		t.Errorf("rendered card did not escape the literal ampersand in the input:\n%s", out)
	}
}
