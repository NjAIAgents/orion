package web

// The log panel (OR-69): tests over the served artifact, the same pattern
// runview_test.go established for OR-67. No JS runtime is exercised --
// OR-71's own criterion (a working binary on a machine with no Node
// installed) rules out this suite quietly acquiring one. What is verified:
// app.js connects to the real stream endpoint, reads the fields the server
// actually sends, and implements the four behaviours the ticket's done-when
// names by their real mechanism rather than by a comment claiming they
// exist.

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func appJS(t *testing.T) string {
	t.Helper()
	rec := httptest.NewRecorder()
	Assets().ServeHTTP(rec, httptest.NewRequest("GET", "/js/app.js", nil))
	if rec.Code != 200 {
		t.Fatalf("GET /js/app.js = %d", rec.Code)
	}
	return rec.Body.String()
}

// DONE-WHEN, CLAUSE ONE (streamed lines): the panel connects to the real
// stream endpoint OR-63 serves, not a hardcoded or placeholder URL.
func TestLogPanelConnectsToTheStreamEndpoint(t *testing.T) {
	src := appJS(t)
	if !strings.Contains(src, `new EventSource("/api/stream")`) {
		t.Fatal("app.js does not open an EventSource on /api/stream")
	}
}

// The panel reads the same detail keys ui/stage.go writes into a stage
// event (internal/ui/stage.go: detailFrom="from", detailTo="to",
// detailBy="by", detailNext="next"). A rename on either side and this
// fails, because the two are not otherwise connected by the compiler.
func TestLogPanelReadsTheRealStageDetailKeys(t *testing.T) {
	src := appJS(t)
	for _, key := range []string{"detail.from", "detail.to", "detail.by", "detail.next"} {
		if !strings.Contains(src, key) {
			t.Errorf("app.js does not read e.%s from a stage event", key)
		}
	}
}

// DONE-WHEN, CLAUSE TWO (filters): the five real verbs, and no others, are
// the toggleable set -- internal/ui/event.go's own closed vocabulary,
// copied rather than reinvented for the reason app.js's own verbIcon
// comment already gives for the icons.
func TestLogPanelFiltersAreTheRealFiveVerbs(t *testing.T) {
	src := appJS(t)
	for _, v := range []string{"ok", "working", "waiting", "warning", "failed"} {
		if !strings.Contains(src, `"`+v+`"`) {
			t.Errorf("app.js does not name the verb %q among its filters", v)
		}
	}
}

// verbFor must be a complete port of internal/ui's VerbFor (event.go), or
// the browser and the terminal disagree about what an event's verb is --
// the exact defect internal/ui's own package comment says it exists to
// prevent. Checked kind by kind against the real switch, not just that a
// function named verbFor exists.
func TestVerbForMatchesEveryKindInternalUIMaps(t *testing.T) {
	src := appJS(t)
	want := map[string]string{
		"failed": "failed", "blocked": "failed",
		"escalate": "warning", "refuse": "warning", "budget": "warning", "attribution": "warning",
		"ci":      "waiting",
		"claimed": "working", "branch": "working", "run-start": "working", "ask": "working",
		"tool": "working", "say": "working",
		"note": "ok",
	}
	for kind, verb := range want {
		idx := strings.Index(src, `case "`+kind+`"`)
		if idx == -1 {
			t.Errorf("verbFor has no case for kind %q", kind)
			continue
		}
		// The return for this case is the next "return " after its case
		// label, within the switch. Good enough for a single-line case body,
		// which is every branch in verbFor.
		rest := src[idx:]
		retIdx := strings.Index(rest, "return ")
		if retIdx == -1 {
			t.Errorf("no return found after case %q", kind)
			continue
		}
		line := rest[retIdx : retIdx+40]
		if !strings.Contains(line, `"`+verb+`"`) {
			t.Errorf("kind %q: want verb %q, source near it: %q", kind, verb, line)
		}
	}
}

// DONE-WHEN, CLAUSE THREE (auto-scroll): a new line scrolls the panel to
// its bottom UNLESS paused -- checked as the real conditional, not just
// that a scrollTop assignment exists somewhere.
func TestAutoScrollIsGatedOnThePauseFlag(t *testing.T) {
	src := appJS(t)
	if !strings.Contains(src, "if (this.state.paused || !this.linesRef) return;") {
		t.Fatal("maybeScroll does not gate on state.paused; a paused reader would be " +
			"yanked back to the bottom by every new line")
	}
	if !strings.Contains(src, "scrollTop = this.linesRef.scrollHeight") {
		t.Fatal("no scrollTop assignment found; the panel never actually auto-scrolls")
	}
}

// DONE-WHEN, CLAUSE FOUR (pause on scroll-up): scrolling away from the
// bottom sets paused, and the footer names both directions -- the
// mockup's own "pause on scroll-up" label, and its opposite once paused.
func TestScrollingUpPausesAndTheControlOffersToResume(t *testing.T) {
	src := appJS(t)
	if !strings.Contains(src, "atBottom") || !strings.Contains(src, "paused: true") {
		t.Fatal("no scroll-position check found that sets paused true")
	}
	if !strings.Contains(src, "pause on scroll-up") {
		t.Error(`app.js does not use the mockup's own label "pause on scroll-up"`)
	}
	if !strings.Contains(src, "resume") {
		t.Error("the paused state offers no way back to following")
	}
}

// A malformed SSE frame must not stop the tail. The server (OR-63) writes
// json.Marshal output exclusively, so this is defence against a future
// change on either side, not a case expected from today's server.
func TestAMalformedFrameDoesNotStopTheTail(t *testing.T) {
	src := appJS(t)
	if !strings.Contains(src, "catch") {
		t.Fatal("app.js has no catch around parsing a stream frame; one bad frame " +
			"would throw inside onmessage and the browser would simply stop delivering more")
	}
}

// Memory must be bounded. A watch left open for hours must not grow the
// tab's memory without limit -- checked as a real cap enforced on every
// append, not merely declared and ignored.
func TestTheLineBufferIsBounded(t *testing.T) {
	src := appJS(t)
	if !strings.Contains(src, "maxLines") {
		t.Fatal("no maxLines cap found")
	}
	if !strings.Contains(src, "lines.length >= maxLines") {
		t.Error("maxLines is declared but nothing checks the buffer length against it " +
			"before appending")
	}
}

// A stage boundary must never be silently dropped by a verb or trace
// filter -- OR-69's own reason for the different layout is that a handoff
// has to be FINDABLE, which a filtered-out handoff is not.
func TestStageEventsAreNeverFilteredOut(t *testing.T) {
	src := appJS(t)
	i := strings.Index(src, "visible()")
	if i == -1 {
		t.Fatal("no visible() filter method found")
	}
	body := src[i : i+400]
	if !strings.Contains(body, "if (l.stage) return true") {
		t.Error("visible() does not unconditionally keep stage rows ahead of the " +
			"verb/trace checks; a handoff could be filtered out like an ordinary line")
	}
}
