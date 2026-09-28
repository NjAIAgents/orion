package work

import (
	"strings"
	"testing"

	"github.com/orion-sdlc/orion/internal/actors"
	"github.com/orion-sdlc/orion/internal/events"
)

// OR-571: a person's comment reaches the prompts; Orion's own do not.
func TestAPersonsCommentReachesTheDescriptionAndOrionsDoNot(t *testing.T) {
	comments := []string{
		actors.Comment(events.ActorOrion, "claimed: ORION -> orion-working"),
		actors.Comment(events.ActorCI, "CI failed on https://pr/1."),
		actors.Comment(events.ActorImplementer, "committed 3 files"),
		"Why CI failed: the tests run git archive dfc8213, a hard-coded commit.",
	}
	got := withPersonNotes("Run the review.", comments)
	if !strings.Contains(got, notesHeading) || !strings.Contains(got, "git archive dfc8213") {
		t.Fatalf("the person's note is missing:\n%s", got)
	}
	for _, orion := range []string{"claimed: ORION", "CI failed on", "committed 3 files"} {
		if strings.Contains(got, orion) {
			t.Errorf("Orion's own comment %q reached the prompt:\n%s", orion, got)
		}
	}
	if withPersonNotes("Run the review.", comments[:3]) != "Run the review." {
		t.Error("with only Orion's comments, the description should be unchanged")
	}
}
