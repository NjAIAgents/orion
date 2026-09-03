package decide

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/orion-sdlc/orion/internal/events"
	"github.com/orion-sdlc/orion/internal/slack"
)

// OR-153 case: Slack was asked and nobody has reacted at all yet -- as
// opposed to a bystander reacting (covered elsewhere) or Slack never being
// configured (covered elsewhere). collect.ReadDecision reports this as "not
// approved" with its own Why, and Confirm must pass that through rather than
// inventing a different reason.
func TestConfirmationWithNoAnswerReturnsNotApprovedWithCorrectWhy(t *testing.T) {
	s := &fakeSlack{ts: "111.222"} // no reactions at all
	dir, deps := recommended(t, s, &fakeTracker{})

	d, ok, err := Confirm(deps, dir, "OR-153")
	if err != nil {
		t.Fatalf("Confirm: %v", err)
	}
	if ok || d.Approved {
		t.Fatalf("an unanswered question confirmed itself: %+v", d)
	}
	if !strings.Contains(d.Why, "nobody has approved") {
		t.Errorf("Why = %q; want it to say nobody has approved it yet", d.Why)
	}
	if _, err := os.Stat(confirmedPath(dir)); !os.IsNotExist(err) {
		t.Errorf("an unanswered recommendation was promoted anyway")
	}
}

// OR-153 case: confirming twice must be safe. The first call promotes the
// record and removes the pending copy; the second call has no pending file
// left to read, so it errors rather than silently re-confirming or
// corrupting the already-confirmed record. Either "no-op" or "error" is an
// acceptable implementation choice -- what is not acceptable is a second,
// different confirmed record, or losing the first one.
func TestConfirmingTwiceFromTheSameApproverIsIdempotentOrErrors(t *testing.T) {
	s := &fakeSlack{ts: "111.222",
		names:     map[string]string{"U-APPROVER": "ops-lead"},
		reactions: []slack.Reaction{{Name: "white_check_mark", Users: []string{"U-APPROVER"}}}}
	dir, deps := recommended(t, s, &fakeTracker{})

	d1, ok1, err1 := Confirm(deps, dir, "OR-153")
	if err1 != nil || !ok1 || !d1.Approved {
		t.Fatalf("first Confirm: ok=%v err=%v decision=%+v", ok1, err1, d1)
	}
	before := read(t, confirmedPath(dir))

	d2, ok2, err2 := Confirm(deps, dir, "OR-153")
	if err2 == nil && ok2 {
		t.Fatalf("a second confirmation reported success without a pending "+
			"file to read: decision=%+v", d2)
	}

	after := read(t, confirmedPath(dir))
	if before != after {
		t.Errorf("the confirmed record changed on a second confirmation:\nbefore:\n%s\nafter:\n%s",
			before, after)
	}
}

// OR-153 case: a recommendation with no ticket key is meaningless -- there is
// nowhere to file it and nothing later can look it up -- so Recommend refuses
// it and writes nothing to either directory.
func TestRecommendWithEmptyKeyReturnsErrorAndWritesNothing(t *testing.T) {
	dir := t.TempDir()
	deps := Deps{}
	if _, err := Recommend(deps, dir, Record{
		Key: "", Recommendation: "do the thing", By: events.ActorArchitect,
	}); err == nil {
		t.Fatal("Recommend accepted a record with no ticket key")
	}
	assertNothingWritten(t, dir)
}

// OR-153 case: a recommendation with nothing recommended is nothing to
// confirm, so Recommend refuses it and writes nothing.
func TestRecommendWithEmptyRecommendationReturnsErrorAndWritesNothing(t *testing.T) {
	dir := t.TempDir()
	deps := Deps{}
	if _, err := Recommend(deps, dir, Record{
		Key: "OR-153", Recommendation: "   ", By: events.ActorArchitect,
	}); err == nil {
		t.Fatal("Recommend accepted a record with no recommendation")
	}
	assertNothingWritten(t, dir)
}

func assertNothingWritten(t *testing.T, dir string) {
	t.Helper()
	for _, d := range []string{PendingDir, ConfirmedDir} {
		entries, err := os.ReadDir(filepath.Join(dir, d))
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			t.Fatalf("reading %s: %v", d, err)
		}
		if len(entries) != 0 {
			t.Errorf("%s is not empty after a rejected Recommend: %v", d, entries)
		}
	}
}

// OR-153 case: a recommendation with no Slack channel has no question to
// ask, but that is graceful degradation, not a hard error -- the record is
// still written to PendingDir, unconfirmed, so a human can confirm it some
// other way later.
func TestRecommendWithEmptyChannelStillWritesToPending(t *testing.T) {
	dir := t.TempDir()
	deps := Deps{Slack: &fakeSlack{ts: "111.222"}}
	if _, err := Recommend(deps, dir, Record{
		Key: "OR-153", Recommendation: "do the thing", By: events.ActorArchitect,
		// Channel deliberately empty.
	}); err != nil {
		t.Fatalf("Recommend with no channel: %v", err)
	}
	body := read(t, pendingPath(dir))
	if !strings.Contains(body, statusUnconfirmed) {
		t.Errorf("the record written with no channel does not say unconfirmed:\n%s", body)
	}
}

// ConfirmedDir being used consistently by advise.Artifacts, and PendingDir
// never appearing there, are covered in internal/advise
// (TestConfirmedRecommendationsAreInScopeAndPendingOnesAreNot) rather than
// here: advise imports decide, so a test importing advise from this package
// would be an import cycle.
