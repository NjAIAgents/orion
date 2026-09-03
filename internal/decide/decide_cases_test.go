package decide

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/orion-sdlc/orion/internal/actors"
	"github.com/orion-sdlc/orion/internal/events"
	"github.com/orion-sdlc/orion/internal/slack"
)

// OR-153 case: no Slack channel/TS on the record at all -- Confirm has
// nothing to read and says so, rather than inventing a confirmation.
func TestConfirmWithNoChannelOrTSReturnsNeverAskedReason(t *testing.T) {
	dir := t.TempDir()
	deps := Deps{Slack: &fakeSlack{ts: "999.999"}, Now: func() time.Time { return at("2026-09-02T10:00:00Z") }}
	if _, err := Recommend(deps, dir, Record{
		Key: "OR-153", Recommendation: "do the thing", By: events.ActorArchitect,
		// Channel deliberately empty: no question was ever asked.
	}); err != nil {
		t.Fatalf("Recommend: %v", err)
	}

	d, ok, err := Confirm(deps, dir, "OR-153")
	if err != nil || ok {
		t.Fatalf("confirmed with no channel/ts on the record: ok=%v err=%v", ok, err)
	}
	if !strings.Contains(d.Why, "never asked") {
		t.Errorf("Why = %q; it should say there was no question to answer", d.Why)
	}
}

// OR-153 case: a tracker outage must not stop the recommendation from being
// recorded, nor stop a later confirmation from promoting it -- Deps.Jira is
// one of the optional seams and its absence degrades in the safe direction.
func TestTrackerUnavailableDoesNotBlockRecommendationOrConfirmation(t *testing.T) {
	s := &fakeSlack{ts: "111.222",
		names:     map[string]string{"U-APPROVER": "ops-lead"},
		reactions: []slack.Reaction{{Name: "white_check_mark", Users: []string{"U-APPROVER"}}}}
	dir := t.TempDir()
	deps := Deps{Slack: s, Now: func() time.Time { return at("2026-09-02T10:00:00Z") }} // no Jira

	if _, err := Recommend(deps, dir, Record{
		Key: "OR-153", Title: "index the ledger by issuer",
		Recommendation: "Partition the ledger by issuer, not by date.",
		By:             events.ActorArchitect,
		Channel:        "C1", Approvers: []string{"U-APPROVER"},
	}); err != nil {
		t.Fatalf("Recommend without a tracker: %v", err)
	}
	if _, err := os.Stat(pendingPath(dir)); err != nil {
		t.Fatalf("the recommendation was not written without a tracker: %v", err)
	}

	d, ok, err := Confirm(deps, dir, "OR-153")
	if err != nil || !ok {
		t.Fatalf("Confirm without a tracker: ok=%v err=%v decision=%+v", ok, err, d)
	}
	if _, err := os.Stat(confirmedPath(dir)); err != nil {
		t.Fatalf("the confirmation was not promoted without a tracker: %v", err)
	}
}

// OR-153 case: the confirmation comment is attributed to Orion -- not to the
// actor that recommended, and not left unattributed -- and names the
// approver by the display name Slack knows them by, not their raw user id.
func TestConfirmationCommentIsAttributedToOrionAndNamesApproverByDisplayName(t *testing.T) {
	s := &fakeSlack{ts: "111.222",
		names:     map[string]string{"U-APPROVER": "ops-lead"},
		reactions: []slack.Reaction{{Name: "white_check_mark", Users: []string{"U-APPROVER"}}}}
	j := &fakeTracker{}
	dir, deps := recommended(t, s, j)

	if _, ok, err := Confirm(deps, dir, "OR-153"); err != nil || !ok {
		t.Fatalf("Confirm: ok=%v err=%v", ok, err)
	}
	if len(j.comments) != 2 {
		t.Fatalf("expected a recommendation comment and a confirmation comment, got %d", len(j.comments))
	}
	comment := j.comments[1]
	if !strings.HasPrefix(comment, actors.Attribution(events.ActorOrion)) {
		t.Errorf("the confirmation comment is not attributed to Orion:\n%s", comment)
	}
	if !strings.Contains(comment, "ops-lead") {
		t.Errorf("the confirmation comment does not name the approver by display name (U-APPROVER instead?):\n%s", comment)
	}
	if strings.Contains(comment, "U-APPROVER") {
		t.Errorf("the confirmation comment leaked the raw Slack user id instead of the display name:\n%s", comment)
	}
}

// OR-153 case: confirming appends a KindDecision event that carries the
// approver, how they confirmed, and the Slack message it happened on --
// everything a reader needs without going back to Slack.
func TestConfirmingRecordsADecisionEventWithActorMethodApproverAndSlackReference(t *testing.T) {
	s := &fakeSlack{ts: "111.222",
		names:     map[string]string{"U-APPROVER": "ops-lead"},
		reactions: []slack.Reaction{{Name: "white_check_mark", Users: []string{"U-APPROVER"}}}}
	dir := t.TempDir()
	logPath := filepath.Join(dir, "events.jsonl")
	log, err := events.Open(logPath, events.Event{})
	if err != nil {
		t.Fatalf("events.Open: %v", err)
	}
	defer log.Close()

	deps := Deps{Slack: s, Jira: &fakeTracker{}, Log: log, Now: func() time.Time { return at("2026-09-02T10:00:00Z") }}
	if _, err := Recommend(deps, dir, Record{
		Key: "OR-153", Title: "index the ledger by issuer",
		Recommendation: "Partition the ledger by issuer, not by date.",
		By:             events.ActorArchitect,
		Channel:        "C1", Approvers: []string{"U-APPROVER"},
	}); err != nil {
		t.Fatalf("Recommend: %v", err)
	}

	if _, ok, err := Confirm(deps, dir, "OR-153"); err != nil || !ok {
		t.Fatalf("Confirm: ok=%v err=%v", ok, err)
	}

	evs, err := events.Read(logPath)
	if err != nil {
		t.Fatalf("events.Read: %v", err)
	}
	var decision *events.Event
	for i := range evs {
		if evs[i].Kind == events.KindDecision {
			decision = &evs[i]
		}
	}
	if decision == nil {
		t.Fatalf("no decision event was recorded; got %+v", evs)
	}
	if decision.Actor != events.ActorHuman {
		t.Errorf("decision actor = %q, want %q -- the approver, not Orion, made the choice", decision.Actor, events.ActorHuman)
	}
	if decision.Key != "OR-153" {
		t.Errorf("decision key = %q", decision.Key)
	}
	if got := decision.Detail["approver"]; got != "ops-lead" {
		t.Errorf("decision detail approver = %v, want ops-lead", got)
	}
	if _, ok := decision.Detail["how"]; !ok {
		t.Errorf("decision detail is missing the confirmation method: %+v", decision.Detail)
	}
	if decision.Detail["slack_channel"] != "C1" || decision.Detail["slack_ts"] != "111.222" {
		t.Errorf("decision detail is missing the Slack reference: %+v", decision.Detail)
	}
}
