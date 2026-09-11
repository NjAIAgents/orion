package web

// This writer's slice of OR-276's done-when, alongside escaping_test.go's
// structural pair, escaping_extra_test.go's Unicode/source-guard cases and
// escaping_vectors_test.go's payload spread:
//
//   - the Card struct itself, reflected over, carries no escape-hatch type --
//     a check the source guard cannot make, since a field typed template.HTML
//     that happens to hold plain ASCII in every test fixture would still pass
//     every render test above,
//   - Scan's Activity assignment is a straight copy, not a type conversion,
//   - SVG/XML-shaped vectors, which the HTML-tag vectors above do not cover,
//   - and a value rendered into a data-* / custom attribute, escaped the same
//     way Title's attribute is.
//
// Client-side rendering (case 23, textContent vs innerHTML) is OR-289's over
// internal/web/ui, per this package's own VENDOR.md pointer; internal/web/ui
// today holds only vendored preact/htm under ui/vendor, with no board-drawing
// code of this repo's own to hold to that rule yet, so there is nothing here
// to test against.

import (
	"bytes"
	"html"
	"html/template"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/orion-sdlc/orion/internal/events"
)

// Case 21: every string-shaped field on Card, and on the Session it embeds,
// is the plain `string` type -- never template.HTML or one of its siblings,
// which say "already escaped" about a value nothing here has escaped.
// Reflected rather than asserted by usage, so a field added later that is
// merely never fed a payload in a render test still gets caught here.
func TestCardFieldsAreValuePlainStringNotAnEscapeHatchType(t *testing.T) {
	plainString := reflect.TypeOf("")

	assertPlainStringFields := func(v reflect.Value, structName string) {
		typ := v.Type()
		for i := 0; i < typ.NumField(); i++ {
			f := typ.Field(i)
			// Session and other non-string fields (time.Time, int, bool) are out
			// of scope here; this case is about the string-shaped fields only.
			if f.Type.Kind() != reflect.String {
				continue
			}
			if f.Type != plainString {
				t.Errorf("%s.%s is type %s, not plain string -- that type asserts the value is already safe to render, which a tracker or log value never is", structName, f.Name, f.Type)
			}
		}
	}

	assertPlainStringFields(reflect.ValueOf(Card{}), "Card")
	assertPlainStringFields(reflect.ValueOf(Session{}), "Session")
}

// Case 22: Scan (via sessionOf) sets Session.Activity from the event's Msg
// with no conversion in between -- not through html.EscapeString, not through
// template.HTML(...), not through any transform that would make the value
// under test something other than what the log actually wrote. The render
// tests elsewhere prove html/template escapes it at draw time; this proves
// Scan is not pre-processing it on the way in, which would double-escape it
// or (via template.HTML) skip escaping entirely.
func TestScanPreservesActivityAsPlainStringWithoutConversion(t *testing.T) {
	const raw = `<b>plan</b> & "quote" 'apos' <script>x</script>`

	cards := Scan([]events.Event{
		{At: base, Kind: events.KindRunStart, Key: "OR-276", Run: "r1"},
		{At: base.Add(time.Minute), Kind: events.KindSay, Key: "OR-276", Run: "r1", Msg: raw},
	}, nil)
	if got, want := len(cards), 1; got != want {
		t.Fatalf("Scan returned %d cards, want %d", got, want)
	}

	got := cards[0].Session.Activity
	if got != raw {
		t.Fatalf("Scan carried Activity = %q, want the untouched event Msg %q -- Scan is transforming the value instead of passing it through", got, raw)
	}
	if reflect.TypeOf(got) != reflect.TypeOf("") {
		t.Fatalf("Session.Activity is type %T, not string", got)
	}
}

// Case 24: SVG and XML carry their own script-execution vectors -- onload on
// an svg element, a foreignObject smuggling HTML into an XML-parsed document,
// and a CDATA-wrapped script -- distinct from the HTML <img>/<script> vectors
// already exercised above. html/template treats SVG markup as ordinary HTML
// text in this context (the card is drawn as HTML, not embedded as an SVG
// document), so the same contextual escaping has to hold for it too.
func TestSVGAndXMLPayloadsEscapeCorrectly(t *testing.T) {
	vectors := []string{
		`<svg onload="alert(1)"></svg>`,
		`<svg><foreignObject><body onload="alert(2)"></body></foreignObject></svg>`,
		`<svg><script><![CDATA[alert(3)]]></script></svg>`,
		`<xml><script>alert(4)</script></xml>`,
	}

	for _, payload := range vectors {
		t.Run(payload, func(t *testing.T) {
			card := Card{Key: "OR-276", Title: payload, Gate: payload, Session: Session{Activity: payload}}

			var buf bytes.Buffer
			if err := cardTemplate.Execute(&buf, card); err != nil {
				t.Fatalf("render card: %v", err)
			}
			out := buf.String()

			raw := []string{"<svg", "<foreignObject", "<script", "<xml", "onload=\"alert"}
			for _, r := range raw {
				if strings.Contains(out, r) {
					t.Errorf("rendered card contains %q, so the SVG/XML payload reached the browser as markup:\n%s", r, out)
				}
			}
			if got, want := strings.Count(html.UnescapeString(out), payload), 4; got != want {
				t.Errorf("payload appears as literal text %d times, want %d (Title attribute, Title summary, Gate, Activity):\n%s", got, want, out)
			}
		})
	}
}

// Case 25: a template that puts an untrusted value into a data-* or other
// custom attribute has to escape it exactly the way Title's title="" does --
// html/template does not treat data-* specially, but nothing here pinned that
// a future card template using one inherits the same guarantee rather than a
// hand-rolled string-concatenation attribute that skips escaping entirely.
func TestDataAttributeValuesEscapeCorrectly(t *testing.T) {
	const payload = `" data-x="1" onmouseover="alert(1)`

	tmpl := template.Must(template.New("data-attr-card").Parse(
		`<article class="card" data-key="{{.Key}}" data-activity="{{.Session.Activity}}"></article>`))

	card := Card{Key: payload, Session: Session{Activity: payload}}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, card); err != nil {
		t.Fatalf("render card: %v", err)
	}
	out := buf.String()

	if strings.Contains(out, `onmouseover="alert(1)`) {
		t.Errorf("rendered card contains an unescaped onmouseover attribute, so the payload broke out of a data-* attribute:\n%s", out)
	}
	if got, want := strings.Count(html.UnescapeString(out), payload), 2; got != want {
		t.Errorf("payload appears as literal text %d times, want %d (data-key, data-activity):\n%s", got, want, out)
	}
}
