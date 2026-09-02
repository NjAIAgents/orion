package decide

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/orion-sdlc/orion/internal/actors"
	"github.com/orion-sdlc/orion/internal/events"
	"github.com/orion-sdlc/orion/internal/slack"
)

// fakeSlack is the merge-approval surface, answering from fixtures.
type fakeSlack struct {
	ts        string
	posted    string
	reactions []slack.Reaction
	replies   []slack.Message
	names     map[string]string
	reacted   []string
}

func (f *fakeSlack) PostTS(channel, text string) (string, error) {
	f.posted = text
	return f.ts, nil
}
func (f *fakeSlack) React(channel, ts, emoji string) { f.reacted = append(f.reacted, emoji) }
func (f *fakeSlack) BotID() string                   { return "UBOT" }
func (f *fakeSlack) Reactions(string, string) ([]slack.Reaction, error) {
	return f.reactions, nil
}
func (f *fakeSlack) Replies(string, string) ([]slack.Message, error) { return f.replies, nil }
func (f *fakeSlack) UserName(id string) string {
	if n, ok := f.names[id]; ok {
		return n
	}
	return id
}

type fakeTracker struct{ comments []string }

func (f *fakeTracker) Comment(key, text string) error {
	f.comments = append(f.comments, text)
	return nil
}

func at(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}

func recommended(t *testing.T, s *fakeSlack, j *fakeTracker) (string, Deps) {
	t.Helper()
	dir := t.TempDir()
	deps := Deps{Slack: s, Jira: j, Now: func() time.Time { return at("2026-09-02T10:00:00Z") }}
	_, err := Recommend(deps, dir, Record{
		Key: "OR-153", Title: "index the ledger by issuer",
		Recommendation: "Partition the ledger by issuer, not by date.",
		Grounding:      "spec.md, the read patterns section",
		By:             events.ActorArchitect,
		Channel:        "C1", Approvers: []string{"U-APPROVER"},
	})
	if err != nil {
		t.Fatalf("Recommend: %v", err)
	}
	return dir, deps
}

func pendingPath(dir string) string   { return filepath.Join(dir, PendingDir, "OR-153.md") }
func confirmedPath(dir string) string { return filepath.Join(dir, ConfirmedDir, "OR-153.md") }

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

// The invariant, from the direction that matters: a recommendation nobody
// has answered is unconfirmed in the file AND absent from the directory
// later stages read. Either alone would be a convention; both is a
// structure.
func TestARecommendationIsUnconfirmedAndOutOfScopeUntilSomebodyAnswers(t *testing.T) {
	dir, _ := recommended(t, &fakeSlack{ts: "111.222"}, &fakeTracker{})

	body := read(t, pendingPath(dir))
	if !strings.Contains(body, statusUnconfirmed) {
		t.Errorf("the record does not say it is unconfirmed:\n%s", body)
	}
	if strings.Contains(body, statusConfirmed) {
		t.Errorf("a brand new recommendation says it is confirmed:\n%s", body)
	}
	if _, err := os.Stat(confirmedPath(dir)); !os.IsNotExist(err) {
		t.Errorf("an unconfirmed recommendation was written into %s, where every "+
			"later stage reads it as agreed", ConfirmedDir)
	}
}

// Anyone in the channel can react. Only the allowlist can decide -- the same
// rule the merge gate runs, and the reason this reuses it rather than
// growing a second one.
func TestAReactionFromOutsideTheAllowlistDoesNotConfirm(t *testing.T) {
	s := &fakeSlack{ts: "111.222",
		reactions: []slack.Reaction{{Name: "white_check_mark", Users: []string{"U-BYSTANDER"}}}}
	dir, deps := recommended(t, s, &fakeTracker{})

	d, ok, err := Confirm(deps, dir, "OR-153")
	if err != nil {
		t.Fatalf("Confirm: %v", err)
	}
	if ok || d.Approved {
		t.Fatalf("a bystander's tick confirmed the recommendation: %+v", d)
	}
	if _, err := os.Stat(confirmedPath(dir)); !os.IsNotExist(err) {
		t.Errorf("it was promoted anyway")
	}
	if _, err := os.Stat(pendingPath(dir)); err != nil {
		t.Errorf("the pending record was lost: %v", err)
	}
}

// Orion adds the tick to its own question as an affordance. Counting it
// would make every recommendation self-confirming, which is the obvious way
// to build the affordance and the obvious way to lose the gate.
func TestOrionsOwnReactionDoesNotConfirmItsOwnRecommendation(t *testing.T) {
	s := &fakeSlack{ts: "111.222",
		reactions: []slack.Reaction{{Name: "white_check_mark", Users: []string{"UBOT"}}}}
	dir, deps := recommended(t, s, &fakeTracker{})
	if want := []string{confirmEmoji}; len(s.reacted) != 1 || s.reacted[0] != want[0] {
		t.Fatalf("expected the affordance to be added, got %v", s.reacted)
	}

	if _, ok, err := Confirm(deps, dir, "OR-153"); err != nil || ok {
		t.Fatalf("the bot confirmed its own recommendation (ok=%v, err=%v)", ok, err)
	}
}

// An objection beats an approval, whichever arrived first, and it leaves the
// record where it is rather than inventing a third state.
func TestARejectionLeavesItUnconfirmed(t *testing.T) {
	s := &fakeSlack{ts: "111.222",
		names: map[string]string{"U-APPROVER": "ops-lead"},
		reactions: []slack.Reaction{
			{Name: "white_check_mark", Users: []string{"U-APPROVER"}},
			{Name: "x", Users: []string{"U-APPROVER"}},
		}}
	dir, deps := recommended(t, s, &fakeTracker{})

	d, ok, err := Confirm(deps, dir, "OR-153")
	if err != nil {
		t.Fatalf("Confirm: %v", err)
	}
	if ok || !d.Rejected {
		t.Fatalf("an objection did not stop the confirmation: %+v", d)
	}
	if _, err := os.Stat(confirmedPath(dir)); !os.IsNotExist(err) {
		t.Errorf("it was promoted over an objection")
	}
}

// The whole point of confirming: the record moves into scope, says so, and
// carries the audit record -- who confirmed it, when, and the Slack message
// it was said on, which the record points at rather than reproduces.
func TestConfirmingPromotesTheRecordAndNamesTheApprover(t *testing.T) {
	s := &fakeSlack{ts: "111.222",
		names:     map[string]string{"U-APPROVER": "ops-lead"},
		reactions: []slack.Reaction{{Name: "white_check_mark", Users: []string{"U-APPROVER"}}}}
	j := &fakeTracker{}
	dir, deps := recommended(t, s, j)

	d, ok, err := Confirm(deps, dir, "OR-153")
	if err != nil || !ok {
		t.Fatalf("Confirm: ok=%v err=%v decision=%+v", ok, err, d)
	}

	body := read(t, confirmedPath(dir))
	if !strings.Contains(body, statusConfirmed) || strings.Contains(body, statusUnconfirmed) {
		t.Errorf("the promoted record does not say it is confirmed:\n%s", body)
	}
	for _, want := range []string{
		"Confirmed by: ops-lead",             // who
		"Confirmed at: 2026-09-02T10:00:00Z", // when
		":white_check_mark:",                 // how
		"C1/111.222",                         // the Slack message, referenced not replaced
		"Partition the ledger by issuer",     // what they actually confirmed, unedited
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the audit record is missing %q:\n%s", want, body)
		}
	}
	if _, err := os.Stat(pendingPath(dir)); !os.IsNotExist(err) {
		t.Errorf("the record is in both directories at once, so it reads as " +
			"pending and confirmed simultaneously")
	}
}

// A tracker comment months later has no actor column, so the actor travels
// inside the text -- the recommendation attributed to whoever made it, the
// confirmation naming the person who gave it.
func TestTrackerCommentsAreAttributedAndSayWhichStateItIsIn(t *testing.T) {
	s := &fakeSlack{ts: "111.222",
		names:     map[string]string{"U-APPROVER": "ops-lead"},
		reactions: []slack.Reaction{{Name: "white_check_mark", Users: []string{"U-APPROVER"}}}}
	j := &fakeTracker{}
	dir, deps := recommended(t, s, j)

	if len(j.comments) != 1 {
		t.Fatalf("expected the recommendation to be commented once, got %d", len(j.comments))
	}
	first := j.comments[0]
	// Compared against the renderer rather than against a literal: a name
	// written into a test is a name that survives the operator renaming the
	// agent (see actors.TestNoDefaultNameAppearsOutsideTheRegistry).
	if !strings.HasPrefix(first, actors.Attribution(events.ActorArchitect)) {
		t.Errorf("the recommendation is not attributed to the actor that made it:\n%s", first)
	}
	if !strings.Contains(first, "RECOMMENDATION, not a decision") {
		t.Errorf("the comment does not say it is unconfirmed:\n%s", first)
	}

	if _, ok, err := Confirm(deps, dir, "OR-153"); err != nil || !ok {
		t.Fatalf("Confirm: ok=%v err=%v", ok, err)
	}
	if len(j.comments) != 2 {
		t.Fatalf("expected the confirmation to be commented, got %d", len(j.comments))
	}
	last := j.comments[1]
	for _, want := range []string{"ops-lead", "C1/111.222", ConfirmedDir} {
		if !strings.Contains(last, want) {
			t.Errorf("the confirmation comment is missing %q:\n%s", want, last)
		}
	}
}

// Without Slack there is nothing to read, and the answer to that is the
// unconfirmed state -- not a confirmation nobody gave.
func TestWithNoSlackQuestionItStaysUnconfirmed(t *testing.T) {
	dir := t.TempDir()
	deps := Deps{Now: func() time.Time { return at("2026-09-02T10:00:00Z") }}
	if _, err := Recommend(deps, dir, Record{
		Key: "OR-153", Recommendation: "do the thing", By: events.ActorArchitect,
	}); err != nil {
		t.Fatalf("Recommend: %v", err)
	}
	d, ok, err := Confirm(deps, dir, "OR-153")
	if err != nil || ok {
		t.Fatalf("confirmed with nobody to ask: ok=%v err=%v", ok, err)
	}
	if !strings.Contains(d.Why, "never asked") {
		t.Errorf("Why = %q; it should say there was no question to answer", d.Why)
	}
}

// The header is the only part read back, so it has to survive the round
// trip: an approver list that did not would silently become an empty
// allowlist, which ReadDecision treats as "nobody may approve".
func TestTheHeaderRoundTrips(t *testing.T) {
	r := Record{
		Key: "OR-153", Title: "t", Recommendation: "r", By: events.ActorPM,
		At: at("2026-09-02T10:00:00Z"), Channel: "C1", TS: "111.222",
		Approvers: []string{"U1", "@someone"},
	}
	got := parseHeader(r.markdown())
	if got.Key != r.Key || got.By != r.By || !got.At.Equal(r.At) {
		t.Errorf("header round trip lost a field: %+v", got)
	}
	if got.Channel != "C1" || got.TS != "111.222" {
		t.Errorf("Slack handle = %q/%q", got.Channel, got.TS)
	}
	if strings.Join(got.Approvers, ",") != "U1,@someone" {
		t.Errorf("approvers = %v", got.Approvers)
	}
}

// Confirming something that does not say it is pending would be inventing a
// confirmation, so it is refused rather than written.
func TestConfirmRefusesARecordThatIsNotPending(t *testing.T) {
	dir := t.TempDir()
	if err := write(pendingPath(dir), "# OR-153\n\n- Status: something else\n"); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := Confirm(Deps{}, dir, "OR-153"); err == nil || ok {
		t.Fatalf("promoted a record with no pending status (ok=%v, err=%v)", ok, err)
	}
}

// The question itself carries the affordance: a phone user taps the emoji
// rather than typing a reply, so the posted text has to name it.
func TestSlackQuestionIsPostedWithTheConfirmationEmojiAffordance(t *testing.T) {
	s := &fakeSlack{ts: "111.222"}
	recommended(t, s, &fakeTracker{})

	if !strings.Contains(s.posted, ":"+confirmEmoji+":") {
		t.Errorf("the posted question does not carry the confirmation affordance:\n%s", s.posted)
	}
	if len(s.reacted) != 1 || s.reacted[0] != confirmEmoji {
		t.Errorf("Orion did not react with its own affordance: %v", s.reacted)
	}
}

// An allowlisted approver's reaction is what actually promotes the record --
// the positive case the bystander and self-reaction tests are the negative
// of.
func TestAnAllowlistedReactionConfirms(t *testing.T) {
	s := &fakeSlack{ts: "111.222",
		names:     map[string]string{"U-APPROVER": "ops-lead"},
		reactions: []slack.Reaction{{Name: "white_check_mark", Users: []string{"U-APPROVER"}}}}
	dir, deps := recommended(t, s, &fakeTracker{})

	d, ok, err := Confirm(deps, dir, "OR-153")
	if err != nil || !ok || !d.Approved {
		t.Fatalf("an allowlisted reaction did not confirm: ok=%v err=%v decision=%+v", ok, err, d)
	}
}

// A Slack call that errors out (as opposed to Slack never being configured,
// covered above) still leaves the record written and unconfirmed -- the safe
// state to be stuck in -- but the caller is told, not left to assume success.
func TestASlackPostFailureStillWritesTheRecordUnconfirmedButReturnsTheError(t *testing.T) {
	dir := t.TempDir()
	deps := Deps{Slack: &failingSlack{}, Now: func() time.Time { return at("2026-09-02T10:00:00Z") }}

	_, err := Recommend(deps, dir, Record{
		Key: "OR-153", Title: "t", Recommendation: "do the thing",
		By: events.ActorArchitect, Channel: "C1",
	})
	if err == nil {
		t.Fatal("Recommend swallowed the Slack error")
	}

	body := read(t, pendingPath(dir))
	if !strings.Contains(body, statusUnconfirmed) {
		t.Errorf("the record was not written unconfirmed after the Slack failure:\n%s", body)
	}
}

// failingSlack always fails to post, as if the connection were down.
type failingSlack struct{}

func (failingSlack) PostTS(string, string) (string, error) { return "", errors.New("connection refused") }
func (failingSlack) React(string, string, string)          {}
func (failingSlack) BotID() string                         { return "UBOT" }
func (failingSlack) Reactions(string, string) ([]slack.Reaction, error) {
	return nil, nil
}
func (failingSlack) Replies(string, string) ([]slack.Message, error) { return nil, nil }
func (failingSlack) UserName(id string) string                       { return id }

// Confirming a key that was never recommended has no pending file to read,
// so it errors rather than silently doing nothing.
func TestConfirmWithNoPendingFileReturnsAnError(t *testing.T) {
	dir := t.TempDir()
	if _, ok, err := Confirm(Deps{}, dir, "OR-153"); err == nil || ok {
		t.Fatalf("confirmed a recommendation that was never written (ok=%v, err=%v)", ok, err)
	}
}

// The allowlist is a comma-separated header field written by hand as often
// as by Recommend, so stray spaces around a name or a comma must not survive
// into the parsed list -- a leading space would silently mismatch the ID
// ReadDecision compares against.
func TestApproversWithCommasAndSpacesParseCorrectly(t *testing.T) {
	body := "# OR-153: t\n\n" + statusUnconfirmed + "\n" +
		"- Ticket: OR-153\n" +
		"- Approvers: U1,  U2 ,@someone ,  U3\n\n" +
		"## Recommendation\n\nr\n"
	got := parseHeader(body)
	want := []string{"U1", "U2", "@someone", "U3"}
	if strings.Join(got.Approvers, ",") != strings.Join(want, ",") {
		t.Errorf("approvers = %v, want %v", got.Approvers, want)
	}
}

// The header is parsed field by field, but the prose is never touched --
// markdown() writes it out trimmed, and nothing downstream is allowed to
// reflow or reword what an advisor or a person actually wrote.
func TestProseInRecommendationAndGroundingSurvivesRoundTripUnchanged(t *testing.T) {
	r := Record{
		Key: "OR-153", Title: "t", By: events.ActorPM,
		At:             at("2026-09-02T10:00:00Z"),
		Recommendation: "Partition the ledger by issuer.\n\nNot by date -- reads dominate.",
		Grounding:      "spec.md, section 4 -- read patterns, not write patterns.",
	}
	body := r.markdown()
	if !strings.Contains(body, r.Recommendation) {
		t.Errorf("recommendation prose did not survive rendering unchanged:\n%s", body)
	}
	if !strings.Contains(body, r.Grounding) {
		t.Errorf("grounding prose did not survive rendering unchanged:\n%s", body)
	}
}

// parseHeader stops at the first "## " line and never looks at the prose
// beneath it, so a person editing the recommendation or grounding text by
// hand -- fixing a typo, adding a caveat -- does not touch anything Confirm
// reads and cannot block or corrupt the confirmation.
func TestManuallyEditedRecordProseDoesNotPreventConfirmation(t *testing.T) {
	s := &fakeSlack{ts: "111.222",
		names:     map[string]string{"U-APPROVER": "ops-lead"},
		reactions: []slack.Reaction{{Name: "white_check_mark", Users: []string{"U-APPROVER"}}}}
	dir, deps := recommended(t, s, &fakeTracker{})

	body := read(t, pendingPath(dir))
	edited := strings.Replace(body,
		"Partition the ledger by issuer, not by date.",
		"Partition the ledger by issuer, not by date. (amended by a human reviewer)", 1)
	if edited == body {
		t.Fatal("test setup did not actually change the prose")
	}
	if err := os.WriteFile(pendingPath(dir), []byte(edited), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, ok, err := Confirm(deps, dir, "OR-153"); err != nil || !ok {
		t.Fatalf("a hand-edited prose section blocked confirmation: ok=%v err=%v", ok, err)
	}
}
