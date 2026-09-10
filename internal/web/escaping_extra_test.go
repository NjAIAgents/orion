package web

// Remaining OR-276 done-when cases not covered by escaping_test.go's
// structural pair or escaping_vectors_test.go's payload spread: newline and
// Unicode payloads, a long payload with several nested tags, and the source
// guard split into its three named prohibitions (text/template import,
// template.HTML conversion, template.JS conversion) rather than the combined
// scan.

import (
	"bytes"
	"html"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/orion-sdlc/orion/internal/events"
)

// A summary is not necessarily one line of ASCII: a tracker takes newlines in
// a description field, and an agent's activity line can carry any Unicode a
// log can hold. Escaping has to survive both -- a newline must not let a
// payload break out of the attribute it sits in on its own line, and a
// Unicode character must round-trip rather than being mangled or dropped.
func TestNewlineAndUnicodePayloadsEscapeCorrectly(t *testing.T) {
	const payload = "line one\n<script>alert(1)</script>\nline three —日本語 emoji 🎉"

	cards := Scan([]events.Event{
		{At: base, Kind: events.KindRunStart, Key: "OR-276", Run: "r1"},
		{At: base.Add(time.Minute), Kind: events.KindSay, Key: "OR-276", Run: "r1", Msg: payload},
	})
	if got, want := len(cards), 1; got != want {
		t.Fatalf("Scan returned %d cards, want %d", got, want)
	}
	card := cards[0]
	card.Title = payload
	card.Gate = payload
	if got := card.Session.Activity; got != payload {
		t.Fatalf("fixture is not exercising the rule: Scan carried Activity = %q, want %q", got, payload)
	}

	var buf bytes.Buffer
	if err := cardTemplate.Execute(&buf, card); err != nil {
		t.Fatalf("render card: %v", err)
	}
	out := buf.String()

	if strings.Contains(out, "<script>alert(1)</script>") {
		t.Errorf("rendered card contains an unescaped script tag from a multi-line payload:\n%s", out)
	}
	if !strings.Contains(out, "日本語") || !strings.Contains(out, "🎉") {
		t.Errorf("rendered card lost Unicode content from the payload:\n%s", out)
	}
	if got, want := strings.Count(html.UnescapeString(out), payload), 4; got != want {
		t.Errorf("payload appears as literal text %d times, want %d (Title attribute, Title summary, Gate, Activity):\n%s", got, want, out)
	}
}

// One field is not limited to one attack: a real summary can accumulate
// several tags as a ticket gets edited. A long payload nesting multiple tag
// types has to escape completely -- not just the first tag encountered --
// or a template that happens to neutralize one shape still lets the rest
// through.
func TestVeryLongNestedTagPayloadEscapesCompletely(t *testing.T) {
	const repeats = 200
	one := `<div><script>alert(1)</script><img src=x onerror="alert(2)"><svg onload="alert(3)"><a href="javascript:alert(4)">x</a></svg></div>`
	payload := strings.Repeat(one, repeats)

	card := Card{Key: "OR-276", Title: payload, Gate: payload, Session: Session{Activity: payload}}

	var buf bytes.Buffer
	if err := cardTemplate.Execute(&buf, card); err != nil {
		t.Fatalf("render card: %v", err)
	}
	out := buf.String()

	raw := []string{"<div>", "<script>", "<img src=x", "<svg", `href="javascript:`}
	for _, r := range raw {
		if strings.Contains(out, r) {
			t.Errorf("rendered card contains %q, so a nested tag in the long payload reached the browser as markup:\n%s", r, out)
		}
	}
	if got, want := strings.Count(html.UnescapeString(out), payload), 4; got != want {
		t.Errorf("long payload appears as literal text %d times, want %d (Title attribute, Title summary, Gate, Activity):\n%s", got, want, out)
	}
}

// The combined scan in escaping_test.go proves no banned pattern survives
// anywhere in the package; this pins the text/template import specifically,
// so a regression here reports as "text/template imported" rather than
// folding into a generic banned-pattern failure.
func TestNoWebSourceImportsTextTemplate(t *testing.T) {
	assertNoBannedPatternInWebSources(t, `"text/template"`,
		"text/template escapes nothing; the board's server-rendered paths use html/template")
}

// Pins the template.HTML conversion specifically: a value asserted to already
// be safe markup skips html/template's escaping entirely, no matter how the
// template that renders it is written.
func TestNoWebSourceConvertsToTemplateHTML(t *testing.T) {
	assertNoBannedPatternInWebSources(t, "template.HTML",
		"template.HTML asserts a value is already-safe markup, which no tracker or log value is")
}

// Pins the template.JS conversion specifically: a value asserted to already
// be safe script is emitted verbatim into a script context.
func TestNoWebSourceConvertsToTemplateJS(t *testing.T) {
	assertNoBannedPatternInWebSources(t, "template.JS",
		"template.JS asserts a value is already-safe script, which no tracker or log value is")
}

// assertNoBannedPatternInWebSources walks the same non-test .go files as
// escaping_test.go's combined guard, checking a single pattern so each
// case above fails with its own name rather than a shared one.
func assertNoBannedPatternInWebSources(t *testing.T, pattern, why string) {
	t.Helper()

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
		if strings.Contains(code, pattern) {
			t.Errorf("%s uses %s. %s. Render the value with html/template and let it escape (OR-276).", path, pattern, why)
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
