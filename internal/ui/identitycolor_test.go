package ui

import (
	"bytes"
	"strings"
	"testing"

	"github.com/orion-sdlc/orion/internal/actors"
	"time"
)

// OR-346, reported from a real watch: four actors on screen, one of them
// coloured and three plain, so nothing said who was speaking.
//
// The cause was not missing colour. actorColor hashed into a six-entry
// palette, and at the size of the actual roster that collides hard --
// measured, seven actors produced FOUR colours, with orion sharing blue
// with dba and qa sharing bright cyan with ci. A colour two actors share is
// worse than none, because it groups lines that are not related.
func TestEveryActorOnTheRosterGetsItsOwnColour(t *testing.T) {
	roster := []string{"orion", "implementer", "qa", "reviewer", "devops", "dba", "ci"}

	seen := map[string]string{}
	for _, a := range roster {
		c := actorColor(a)
		if c == "" {
			t.Errorf("%q has no colour at all", a)
			continue
		}
		if other, clash := seen[c]; clash {
			t.Errorf("%q and %q are the same colour, so a reader cannot tell "+
				"their lines apart", a, other)
		}
		seen[c] = a
	}
}

// A colour has to answer ONE question. Sharing a palette between the ticket
// column and the actor column meant a colour could mean either, and the eye
// had nothing to group by.
func TestActorsAndTicketsNeverShareAColour(t *testing.T) {
	tickets := map[string]bool{}
	for _, c := range ticketPalette {
		tickets[c] = true
	}
	for _, a := range []string{"orion", "implementer", "qa", "reviewer", "devops", "dba", "ci"} {
		if tickets[actorColor(a)] {
			t.Errorf("actor %q wears a colour from the ticket palette", a)
		}
	}
}

// The same actor keeps its colour for the life of the process. A colour that
// changed between ticks would say "different actor", which is worse than no
// colour at all -- the same contract ticketColor has.
func TestAnActorKeepsItsColour(t *testing.T) {
	first := actorColor("qa")
	for i := 0; i < 5; i++ {
		if got := actorColor("qa"); got != first {
			t.Fatalf("qa changed colour on call %d: %q then %q", i, first, got)
		}
	}
}

// OR-346's second half. Identity suppression assumes the reader still has
// the previous line in view. That holds for a burst and stops holding for a
// heartbeat thirty seconds later (OR-338): a long agent run rendered as
// fifteen consecutive lines with an empty actor column.
func TestAGapReStatesWhoTheLineBelongsTo(t *testing.T) {
	now := time.Now()
	restore := clock
	clock = func() time.Time { return now }
	t.Cleanup(func() { clock = restore; ConsoleReset() })
	ConsoleReset()

	var buf bytes.Buffer
	beat := func(detail string) {
		Say(&buf, "OR-346", "qa", VerbWorking, "%s", detail)
	}
	// What the reader actually sees in the actor column: the roster renders
	// an id as a person, so asserting on the raw id would pass against a
	// blank column.
	who := actors.Display("qa")
	if strings.TrimSpace(who) == "" {
		t.Fatal("the roster gave this actor no display name; the assertions below would be vacuous")
	}

	beat("ran go test")
	// Immediately after: a burst, and the identity is rightly suppressed.
	beat("ran go vet")
	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("want 2 lines, got %d:\n%s", len(lines), buf.String())
	}
	if strings.Contains(lines[1], who) {
		t.Errorf("a line in the same burst repeated its actor; suppression is "+
			"not working at all:\n%s", lines[1])
	}

	// A heartbeat later. The reader has scrolled; the line has to say whose
	// it is.
	now = now.Add(identityRefresh)
	beat("ran go build")
	lines = strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	if !strings.Contains(lines[len(lines)-1], who) {
		t.Errorf("after a %s gap the line still hides its actor, so a run of "+
			"heartbeats has no visible owner (OR-346):\n%s",
			identityRefresh, lines[len(lines)-1])
	}
}

// ConsoleReset is the boundary BETWEEN runs: nothing about the previous one
// may reach the next. That has to include the identity-refresh clock added
// for OR-346, or one run's timing decides whether the next run's first line
// states who it belongs to.
func TestConsoleResetClearsTheIdentityClockToo(t *testing.T) {
	now := time.Now()
	restore := clock
	clock = func() time.Time { return now }
	t.Cleanup(func() { clock = restore; ConsoleReset() })

	var first bytes.Buffer
	ConsoleReset()
	Say(&first, "OR-154", "qa", VerbWorking, "ran go test")

	ConsoleReset()
	if !console.at.IsZero() {
		t.Errorf("ConsoleReset left the identity clock at %v; a later run's "+
			"first line then depends on when an earlier, unrelated run "+
			"printed (OR-154)", console.at)
	}
}
