package main

// The database architect in planning (OR-154). What these pin down is the
// ORDER -- recommend, confirm, then design -- and the refusal of a choice
// that argues nothing. Both are properties a later change can quietly lose:
// designing the schema in the same run reads as helpful, and recording a
// bare "Postgres" reads as a recommendation right up until somebody asks why.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/orion-sdlc/orion/internal/decide"
	"github.com/orion-sdlc/orion/internal/events"
	"github.com/orion-sdlc/orion/internal/slack"
	"github.com/orion-sdlc/orion/internal/supervisor"
	"github.com/orion-sdlc/orion/internal/workspace"
)

// planSlack is the confirmation surface, answering from fixtures.
type planSlack struct {
	posted    []string
	reactions []slack.Reaction
}

func (s *planSlack) PostTS(channel, text string) (string, error) {
	s.posted = append(s.posted, text)
	return "111.222", nil
}
func (s *planSlack) React(channel, ts, emoji string) {}
func (s *planSlack) BotID() string                   { return "UBOT" }
func (s *planSlack) Reactions(string, string) ([]slack.Reaction, error) {
	return s.reactions, nil
}
func (s *planSlack) Replies(string, string) ([]slack.Message, error) { return nil, nil }
func (s *planSlack) UserName(id string) string                       { return id }

// planHarness is one workspace, one fake architect, one fake Slack.
type planHarness struct {
	ws    *workspace.Workspace
	slack *planSlack
	deps  planDBADeps
	out   *strings.Builder
	says  []string // what the fake architect reports, one run at a time
	sent  []supervisor.Options
}

func newPlanHarness(t *testing.T, says ...string) *planHarness {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "repo"), 0o755); err != nil {
		t.Fatal(err)
	}
	h := &planHarness{
		ws:    &workspace.Workspace{ID: "orion-pay", Dir: dir},
		slack: &planSlack{},
		out:   &strings.Builder{},
		says:  says,
	}
	// The allowlist as it stands when the question is asked is the one the
	// record carries, so it has to be in place before the first run.
	writeApprovers(t, h.ws.RepoDir(), "U-APPROVER")
	h.ws.Task.Slug = "orion-pay"
	h.ws.Task.Idea = "a payments ledger with a database behind it"
	h.ws.Task.Slack = &workspace.SlackChannel{ID: "C1"}
	h.deps = planDBADeps{
		Out: h.out,
		Dispatch: func(_ *workspace.Workspace, o supervisor.Options) (*supervisor.Result, error) {
			h.sent = append(h.sent, o)
			if len(h.says) == 0 {
				// A dispatch the test did not expect. Returning nothing makes
				// the run fail loudly rather than silently reusing the last
				// report.
				return nil, nil
			}
			final := h.says[0]
			h.says = h.says[1:]
			return &supervisor.Result{ExitCode: 0, Final: final}, nil
		},
		Decide: decide.Deps{
			Slack: h.slack,
			Now:   func() time.Time { return time.Unix(1756800000, 0).UTC() },
		},
	}
	return h
}

func (h *planHarness) run(t *testing.T) error {
	t.Helper()
	return planDBA(h.ws, h.deps)
}

func (h *planHarness) pending(topic string) string {
	return pendingRecord(h.ws.RepoDir(), "orion-pay-"+topic)
}

func (h *planHarness) confirmed(topic string) string {
	return confirmedRecord(h.ws.RepoDir(), "orion-pay-"+topic)
}

const choiceReport = `I read the spec.

DBA RECOMMENDS PostgreSQL 16
DBA BECAUSE the ledger is relational and the balance invariant needs a
transaction across two tables. I rejected DynamoDB: it would push that
invariant into application code.`

const schemaReport = `DBA RECOMMENDS
CREATE TABLE ledger_entry (id bigserial PRIMARY KEY, issuer text NOT NULL);
DBA BECAUSE one table per entry keeps the balance derivable, and issuer is
NOT NULL because an entry with no issuer is not a thing the domain has.`

// THE ORDER, which is the whole ticket. One run recommends the database and
// stops. It does not design a schema against an engine nobody has agreed to
// -- that work is thrown away if the choice changes, and worse, a person
// confirming the database would be confirming a schema they were never asked
// about.
func TestTheDatabaseIsRecommendedAndNothingIsDesignedUntilItIsConfirmed(t *testing.T) {
	h := newPlanHarness(t, choiceReport, schemaReport)

	if err := h.run(t); err != nil {
		t.Fatalf("planDBA: %v", err)
	}
	if len(h.sent) != 1 {
		t.Fatalf("the architect was dispatched %d times on the first run; the schema must "+
			"wait for the choice to be confirmed", len(h.sent))
	}
	body, err := os.ReadFile(h.pending(planDBATopicDatabase))
	if err != nil {
		t.Fatalf("the database choice was not recorded as pending: %v", err)
	}
	if !strings.Contains(string(body), "PostgreSQL 16") {
		t.Errorf("the record does not carry what was recommended:\n%s", body)
	}
	if !strings.Contains(string(body), "rejected DynamoDB") {
		t.Errorf("the record does not carry the REASONING, which is the half somebody "+
			"revisiting this in a year comes for:\n%s", body)
	}
	if h.sent[0].Actor != events.ActorDBA {
		t.Errorf("the planning run is attributed to %q, not to the database architect",
			h.sent[0].Actor)
	}
	if _, err := os.Stat(h.confirmed(planDBATopicDatabase)); !os.IsNotExist(err) {
		t.Error("an unconfirmed choice was written where later stages read it as agreed")
	}

	// Nobody has reacted. A second run reads the answer and finds none: it
	// must still not design anything.
	if err := h.run(t); err != nil {
		t.Fatalf("planDBA, second run: %v", err)
	}
	if len(h.sent) != 1 {
		t.Fatalf("the schema was designed while the database was still unconfirmed "+
			"(%d dispatches)", len(h.sent))
	}
	if _, err := os.Stat(h.pending(planDBATopicSchema)); !os.IsNotExist(err) {
		t.Error("a schema was recorded against a database nobody has confirmed")
	}
}

// Once a person confirms the choice, the schema is designed AGAINST THE
// CONFIRMED RECORD -- the text they agreed to, quoted into the prompt, not
// Orion's paraphrase of it -- and lands as its own unconfirmed
// recommendation.
func TestOnceConfirmedTheSchemaIsDesignedAgainstTheConfirmedRecord(t *testing.T) {
	h := newPlanHarness(t, choiceReport, schemaReport)
	if err := h.run(t); err != nil {
		t.Fatalf("planDBA: %v", err)
	}

	h.slack.reactions = []slack.Reaction{
		{Name: "white_check_mark", Users: []string{"U-APPROVER"}},
	}
	if err := h.run(t); err != nil {
		t.Fatalf("planDBA, after confirmation: %v", err)
	}
	if len(h.sent) != 2 {
		t.Fatalf("the schema was not designed after the choice was confirmed "+
			"(%d dispatches)", len(h.sent))
	}
	if p := h.sent[1].Prompt; !strings.Contains(p, "PostgreSQL 16") ||
		!strings.Contains(p, "THE CONFIRMED DATABASE") {
		t.Errorf("the schema prompt is not grounded in the confirmed choice:\n%s", p)
	}
	if _, err := os.Stat(h.pending(planDBATopicSchema)); err != nil {
		t.Errorf("the schema was not recorded as an unconfirmed recommendation: %v", err)
	}
	if _, err := os.Stat(h.confirmed(planDBATopicSchema)); !os.IsNotExist(err) {
		t.Error("the schema confirmed itself; only a person confirms a recommendation")
	}

	// The decision that was already made survives the second one. Both
	// records are filed under one project key, and without their topics the
	// schema's confirmation would land on top of the database's.
	if err := h.run(t); err != nil {
		t.Fatalf("planDBA, confirming the schema: %v", err)
	}
	for _, topic := range []string{planDBATopicDatabase, planDBATopicSchema} {
		if _, err := os.Stat(h.confirmed(topic)); err != nil {
			t.Errorf("the confirmed %s record is gone: %v", topic, err)
		}
	}
}

// "Postgres" with no argument is not a recommendation anybody can evaluate
// when they confirm it, or revisit later. It is REFUSED, and nothing is
// written -- recording the half that arrived would leave a record that
// answers "what" to every reader who came for "why".
func TestAChoiceWithNoReasoningIsRefusedAndNothingIsRecorded(t *testing.T) {
	h := newPlanHarness(t, "DBA RECOMMENDS PostgreSQL 16\n\nThat is my answer.")

	err := h.run(t)
	if err == nil {
		t.Fatal("a recommendation with no reasoning was accepted")
	}
	if !strings.Contains(err.Error(), supervisor.DBABecause) {
		t.Errorf("the refusal does not name what was missing: %v", err)
	}
	if _, statErr := os.Stat(h.pending(planDBATopicDatabase)); !os.IsNotExist(statErr) {
		t.Error("a recommendation with no reasoning was recorded anyway")
	}
	if len(h.slack.posted) != 0 {
		t.Error("Slack was asked to confirm a recommendation that was refused")
	}
}

// A report that names neither marker recommended nothing. That is a normal
// outcome -- the prompt tells the architect to say so when the committed
// artifacts do not settle the choice -- and it must not be recorded as a
// recommendation with the prose stuffed into it.
func TestAReportWithNoMarkersRecordsNothing(t *testing.T) {
	h := newPlanHarness(t, "The spec does not say what this system stores, so I cannot choose.")

	if err := h.run(t); err == nil {
		t.Fatal("a report that recommended nothing was accepted as a recommendation")
	}
	if _, err := os.Stat(h.pending(planDBATopicDatabase)); !os.IsNotExist(err) {
		t.Error("a record was written for a report that recommended nothing")
	}
}

// The parser reads the two halves apart, and tolerates the decoration a model
// puts around a marker -- the same tolerance dbaVerdict has, for the same
// reason: a bolded marker is still the marker.
func TestProposalParsesWhatAndWhyApart(t *testing.T) {
	what, why, err := planDBAProposal(
		"**DBA RECOMMENDS:** SQLite\nfile-backed\n## DBA BECAUSE\nit is one process")
	if err != nil {
		t.Fatalf("planDBAProposal: %v", err)
	}
	if what != "SQLite\nfile-backed" {
		t.Errorf("what = %q", what)
	}
	if why != "it is one process" {
		t.Errorf("why = %q", why)
	}
}

// writeApprovers puts the allowlist where config.Load reads it. The
// confirmation reuses the merge gate's allowlist rather than growing a second
// one, so this is the same setting a merge approval answers to.
func writeApprovers(t *testing.T, repo, user string) {
	t.Helper()
	body := `{"slack":{"merge_approvers":["` + user + `"]}}`
	if err := os.WriteFile(filepath.Join(repo, "orion.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}
