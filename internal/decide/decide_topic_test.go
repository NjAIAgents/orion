package decide

// One ticket, two decisions in sequence (OR-154). The record is a file named
// after the ticket, so without a topic the second recommendation overwrites
// the first -- and it overwrites it in ConfirmedDir, which means what is lost
// is a decision somebody actually made.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/orion-sdlc/orion/internal/events"
	"github.com/orion-sdlc/orion/internal/slack"
)

func TestTwoTopicsOnOneTicketKeepTheirOwnRecords(t *testing.T) {
	dir := t.TempDir()
	s := &fakeSlack{ts: "111.222", reactions: []slack.Reaction{
		{Name: "white_check_mark", Users: []string{"U-APPROVER"}},
	}}
	deps := Deps{Slack: s, Now: func() time.Time { return at("2026-09-04T10:00:00Z") }}

	for _, topic := range []string{"database", "schema"} {
		r, err := Recommend(deps, dir, Record{
			Key: "OR-154", Topic: topic, Title: topic,
			Recommendation: "the " + topic + " body",
			By:             events.ActorDBA,
			Channel:        "C1", Approvers: []string{"U-APPROVER"},
		})
		if err != nil {
			t.Fatalf("Recommend %s: %v", topic, err)
		}
		if r.Name() != "OR-154-"+topic {
			t.Fatalf("record name = %q, want OR-154-%s", r.Name(), topic)
		}
		if _, ok, cErr := Confirm(deps, dir, r.Name()); cErr != nil || !ok {
			t.Fatalf("Confirm %s: ok=%v err=%v", topic, ok, cErr)
		}
	}

	for _, topic := range []string{"database", "schema"} {
		body, err := os.ReadFile(filepath.Join(dir, ConfirmedDir, "OR-154-"+topic+".md"))
		if err != nil {
			t.Fatalf("the confirmed %s record is gone: %v", topic, err)
		}
		if !strings.Contains(string(body), "the "+topic+" body") {
			t.Errorf("the confirmed %s record holds somebody else's decision:\n%s", topic, body)
		}
	}
}
