package work

import (
	"strings"
	"testing"
)

// The Slack message is read on a phone by somebody who was not watching, and
// this is the one failure notification whose remedy is known exactly. The
// title has to name the MACHINE rather than the ticket -- the ticket is fine --
// and the body has to say the fix and that nothing was spent, or the reader
// goes looking at a branch for a problem that never reached one (OR-212).
func TestTheNoAuthMessageNamesTheMachineTheFixAndThatNothingWasSpent(t *testing.T) {
	reason := "claude is not authenticated: Anthropic profile login expired. " +
		"Run: claude, sign in, then restart the watcher."
	title, body := msgNoAuth("OR-168", "do the thing", reason, "https://x/browse/OR-168")

	if title != "Orion stopped: claude is not authenticated" {
		t.Errorf("title = %q; it must name the machine, not the ticket", title)
	}
	for _, want := range []string{
		"Run: claude, sign in", // the fix, verbatim
		"OR-168",               // and which ticket it was working
		"nothing was spent",
		"queued again",
		// Said explicitly rather than left to inference: the reader's whole
		// question is whether this ticket now needs their attention.
		"rather than marked failed",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("body does not say %q:\n%s", want, body)
		}
	}
	if strings.Contains(strings.ToLower(title), "failed") {
		t.Errorf("title = %q reads as a ticket failure; nothing was attempted", title)
	}
}
