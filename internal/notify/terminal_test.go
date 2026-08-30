package notify

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/orion-sdlc/orion/internal/actors"
	"github.com/orion-sdlc/orion/internal/events"
	"github.com/orion-sdlc/orion/internal/ui"
)

func captureOut(t *testing.T) *bytes.Buffer {
	t.Helper()
	var b bytes.Buffer
	prev := Out
	Out = &b
	t.Cleanup(func() { Out = prev })
	return &b
}

// The bug this exists for, captured from a real run:
//
//	[orion:info] FCIA-8 is ready for review
//	*Build golden evaluation suite*
//	• pull request  <https://github.com/navjyotnishant/fcia/pull/3|open it>
//
// That last line is Slack's <url|label> syntax printed verbatim into a
// terminal, because one body was composed for Slack and echoed here
// unchanged. The terminal gets a summary; Slack carries the formatted
// version.
func TestNoSlackMarkupReachesTheTerminal(t *testing.T) {
	isolate(t)
	out := captureOut(t)

	Send(Event{
		Level: Info,
		Title: "FCIA-8 is ready for review",
		Body: "*Build golden evaluation suite*\n\n" +
			"• pull request  <https://github.com/x/y/pull/3|open it>\n" +
			"• branch        `orion/fcia-8`",
	})

	got := out.String()
	for _, markup := range []string{"<https://", "|open it>", "•", "*Build"} {
		if strings.Contains(got, markup) {
			t.Errorf("Slack markup %q reached the terminal:\n%s", markup, got)
		}
	}
	if !strings.Contains(got, "FCIA-8 is ready for review") {
		t.Errorf("the one-line summary is missing:\n%s", got)
	}
	if n := strings.Count(strings.TrimSpace(got), "\n"); n != 0 {
		t.Errorf("the terminal form must be one line, got %d:\n%s", n+1, got)
	}
}

// The second half of OR-189. The echo used to be written straight to Out as
//
//	[orion:info] OR-183 is ready for review
//
// dropped into the middle of a run whose every other line is columnar -- no
// timestamp, no key column, no icon, no verb, no actor. Title-only is
// defended and stays; the missing columns were never defended at all.
func TestTheTerminalEchoOccupiesTheSameColumnsAsEveryOtherLine(t *testing.T) {
	isolate(t)
	out := captureOut(t)

	at := time.Date(2026, 8, 29, 13, 40, 53, 0, time.Local)
	Send(Event{At: at, Key: "OR-183", Level: Info, Actor: events.ActorOrion,
		Title: "OR-183 is ready for review", Body: "irrelevant"})

	got := strings.TrimRight(out.String(), "\n")
	// Time, then the ticket thread, exactly as internal/ui puts them.
	if !strings.HasPrefix(got, "13:40:53 OR-183") {
		t.Errorf("the echo does not lead with the time and key: %q", got)
	}
	if !strings.Contains(got, "OR-183 is ready for review") {
		t.Errorf("the echo lost its title: %q", got)
	}
	// Rendered by ui, not by this package: the reference line for the same
	// facts must be identical up to the message.
	want := ui.Render(out, ui.Line{At: at, Key: "OR-183", Actor: events.ActorOrion,
		Verb: ui.VerbOK, Msg: "notified: OR-183 is ready for review"})
	if got != want {
		t.Errorf("the echo does not render through ui\n got: %q\nwant: %q", got, want)
	}
	if strings.Contains(got, "[orion:") {
		t.Errorf("the echo still carries its own ad-hoc marker: %q", got)
	}
}

// The level is not dropped on the way through: it lands in the status
// column, which is the axis this renderer already has for "do I have to do
// something".
func TestTheTerminalEchoCarriesItsLevel(t *testing.T) {
	isolate(t)
	for level, want := range map[Level]string{
		Info: ui.VerbOK, Warning: ui.VerbWarn, Blocked: ui.VerbFail,
	} {
		out := captureOut(t)
		Send(Event{Key: "OR-183", Level: level, Title: "something happened"})
		if got := out.String(); !strings.Contains(got, want) {
			t.Errorf("level %q did not reach the status column as %q:\n%s",
				level, want, got)
		}
	}
}

// Defensive: a title composed for Slack later must still print as text.
func TestPlainRendersMrkdwnAsText(t *testing.T) {
	got := Plain("*done* — <https://x/y|open it>")
	for _, markup := range []string{"*", "|", "<", ">"} {
		if strings.Contains(got, markup) {
			t.Errorf("Plain left %q in %q", markup, got)
		}
	}
	if !strings.Contains(got, "https://x/y") {
		t.Errorf("Plain dropped the URL: %q", got)
	}
}

// A Slack message is read with no surrounding context, often on a phone, by
// somebody who did not watch the run. The terminal's actor column does not
// exist there, so the name and job title have to travel inside the message.
func TestASlackMessageNamesTheActingRole(t *testing.T) {
	isolate(t)
	captureOut(t)
	var sent string
	prev := SetSlackSender(func(_, text string, _ Level) error { sent = text; return nil })
	t.Cleanup(func() { SetSlackSender(prev) })

	Send(Event{Channel: "C1", Level: Info, Actor: events.ActorImplementer,
		Title: "FCIA-8 started", Body: "*Build it*"})

	if !strings.Contains(sent, actors.Attribution(events.ActorImplementer)) {
		t.Errorf("the acting role is missing from the Slack message:\n%s", sent)
	}
}

// An event with no actor is Orion's own, and must still say so rather than
// arriving unattributed.
func TestAnUnattributedMessageIsOrionsOwn(t *testing.T) {
	isolate(t)
	captureOut(t)
	var sent string
	prev := SetSlackSender(func(_, text string, _ Level) error { sent = text; return nil })
	t.Cleanup(func() { SetSlackSender(prev) })

	Send(Event{Channel: "C1", Level: Info, Title: "FCIA-8 merged", Body: "x"})

	if !strings.Contains(sent, actors.Attribution(events.ActorOrion)) {
		t.Errorf("an unattributed message must be attributed to Orion:\n%s", sent)
	}
}
