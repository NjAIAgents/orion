package main

// The database architect in planning (OR-154). What these pin down is the
// ORDER -- recommend, confirm, then design -- and the refusal of a choice
// that argues nothing. Both are properties a later change can quietly lose:
// designing the schema in the same run reads as helpful, and recording a
// bare "Postgres" reads as a recommendation right up until somebody asks why.

import (
	"fmt"
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

// Reasoning with no choice is the mirror case: an argument for nothing named
// is not something anybody can confirm either, so it is REFUSED the same way.
func TestReasoningWithNoChoiceIsRefusedAndNothingIsRecorded(t *testing.T) {
	h := newPlanHarness(t, "DBA BECAUSE the ledger is relational and needs transactions.")

	err := h.run(t)
	if err == nil {
		t.Fatal("reasoning with no named choice was accepted as a recommendation")
	}
	if !strings.Contains(err.Error(), supervisor.DBARecommends) {
		t.Errorf("the refusal does not name what was missing: %v", err)
	}
	if _, statErr := os.Stat(h.pending(planDBATopicDatabase)); !os.IsNotExist(statErr) {
		t.Error("reasoning with no named choice was recorded anyway")
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

// A model decorates a marker in whatever way it renders emphasis, and the
// parser tolerates that the way dbaVerdict does: an arrow-bulleted RECOMMENDS
// and a bolded-both-sides BECAUSE are still the markers, decoration and all.
func TestProposalToleratesDecorationAroundTheMarkers(t *testing.T) {
	what, why, err := planDBAProposal(
		"-> DBA RECOMMENDS\nMySQL 8, single primary\n**DBA BECAUSE**\nthe write volume is modest")
	if err != nil {
		t.Fatalf("planDBAProposal: %v", err)
	}
	if what != "MySQL 8, single primary" {
		t.Errorf("what = %q", what)
	}
	if why != "the write volume is modest" {
		t.Errorf("why = %q", why)
	}
}

// planDBAArtifacts must name only committed artifacts that actually exist:
// a prompt naming a path that is not there invites the agent to go looking
// for it, or to invent what it would have said. Some of the candidate paths
// exist, some do not, and only the ones present in the repository come back.
func TestPlanDBAArtifactsNamesOnlyFilesThatActuallyExist(t *testing.T) {
	dir := t.TempDir()
	ws := &workspace.Workspace{ID: "orion-pay", Dir: dir}
	ws.Task.Slug = "orion-pay"
	if err := os.MkdirAll(filepath.Join(dir, "repo"), 0o755); err != nil {
		t.Fatal(err)
	}

	// Nothing committed yet: the list must come back empty rather than
	// naming paths that are not there.
	if got := planDBAArtifacts(ws); len(got) != 0 {
		t.Fatalf("planDBAArtifacts with nothing committed = %v, want none", got)
	}

	// Write only the spec and the decisions directory; leave the intent
	// capture and the plan absent.
	specDir := filepath.Join(ws.RepoDir(), "specs")
	if err := os.MkdirAll(specDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(specDir, "orion-pay.spec.md"), []byte("spec"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(ws.RepoDir(), "docs", "decisions"), 0o755); err != nil {
		t.Fatal(err)
	}

	got := planDBAArtifacts(ws)
	want := map[string]bool{"specs/orion-pay.spec.md": true, "docs/decisions": true}
	if len(got) != len(want) {
		t.Fatalf("planDBAArtifacts = %v, want exactly %v", got, want)
	}
	for _, g := range got {
		if !want[g] {
			t.Errorf("planDBAArtifacts named %q, which was never written to the repository", g)
		}
	}
	for _, missing := range []string{"docs/intent/orion-pay.md", "plans/orion-pay.plan.md"} {
		for _, g := range got {
			if g == missing {
				t.Errorf("planDBAArtifacts named %q, which does not exist", missing)
			}
		}
	}
}

// Both records confirmed is a normal steady state, reachable any time this
// command is re-run after everything has already been agreed. It must not
// dispatch the architect again, must not touch the pending or confirmed
// directories, and must say plainly that both are already settled.
func TestWhenBothAreConfirmedRunningWritesNothingAndReportsCompletion(t *testing.T) {
	h := newPlanHarness(t)
	if err := os.MkdirAll(filepath.Dir(h.confirmed(planDBATopicDatabase)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(h.confirmed(planDBATopicDatabase), []byte("PostgreSQL 16"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(h.confirmed(planDBATopicSchema), []byte("CREATE TABLE ledger_entry (...)"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := h.run(t); err != nil {
		t.Fatalf("planDBA with both already confirmed: %v", err)
	}
	if len(h.sent) != 0 {
		t.Errorf("the architect was dispatched %d times when both records were already "+
			"confirmed; there is nothing left to recommend", len(h.sent))
	}
	if len(h.slack.posted) != 0 {
		t.Error("Slack was asked a question that was already settled")
	}
	if !strings.Contains(h.out.String(), "both confirmed") &&
		!strings.Contains(h.out.String(), "are both confirmed") {
		t.Errorf("the report does not say the database and schema are already confirmed:\n%s",
			h.out.String())
	}
}

// The database and the schema are two topics filed under the same ticket
// key, and without the topic in the filename the schema would land on top
// of the database's own record. They must resolve to separate files, in
// both docs/recommendations/pending and docs/recommendations/confirmed.
func TestDatabaseAndSchemaOnTheSameKeyKeepSeparateFiles(t *testing.T) {
	h := newPlanHarness(t, choiceReport, schemaReport)

	// First run: the database recommendation is pending.
	if err := h.run(t); err != nil {
		t.Fatalf("planDBA: %v", err)
	}
	if h.pending(planDBATopicDatabase) == h.pending(planDBATopicSchema) {
		t.Fatal("the database and schema pending records resolve to the same file")
	}
	if _, err := os.Stat(h.pending(planDBATopicDatabase)); err != nil {
		t.Fatalf("the database's pending record is missing: %v", err)
	}
	if _, err := os.Stat(h.pending(planDBATopicSchema)); !os.IsNotExist(err) {
		t.Fatal("the schema was recorded before the database was even confirmed")
	}

	// Confirm the database and let the schema become its own pending record.
	h.slack.reactions = []slack.Reaction{
		{Name: "white_check_mark", Users: []string{"U-APPROVER"}},
	}
	if err := h.run(t); err != nil {
		t.Fatalf("planDBA, after confirming the database: %v", err)
	}
	if h.confirmed(planDBATopicDatabase) == h.confirmed(planDBATopicSchema) {
		t.Fatal("the database and schema confirmed records resolve to the same file")
	}
	dbBody, err := os.ReadFile(h.confirmed(planDBATopicDatabase))
	if err != nil {
		t.Fatalf("the database's confirmed record is missing: %v", err)
	}
	if _, err := os.Stat(h.confirmed(planDBATopicSchema)); !os.IsNotExist(err) {
		t.Fatal("the schema confirmed itself alongside the database")
	}
	schemaBody, err := os.ReadFile(h.pending(planDBATopicSchema))
	if err != nil {
		t.Fatalf("the schema's own pending record is missing: %v", err)
	}
	if strings.Contains(string(dbBody), "ledger_entry") {
		t.Error("the schema's content leaked into the database's confirmed record")
	}
	if !strings.Contains(string(schemaBody), "ledger_entry") {
		t.Error("the schema's own pending record does not carry the schema that was proposed")
	}
}

// Nobody approves between two runs, and a re-run must not ask the architect
// again or design anything against a choice nobody has confirmed: it reads
// the same pending record and stops, in exactly the same way, every time.
func TestRunningTwiceWithNoApprovalReadsThePendingRecordAndStopsBothTimes(t *testing.T) {
	h := newPlanHarness(t, choiceReport)

	if err := h.run(t); err != nil {
		t.Fatalf("planDBA, first run: %v", err)
	}
	if len(h.sent) != 1 {
		t.Fatalf("the architect was dispatched %d times on the first run", len(h.sent))
	}
	before, err := os.ReadFile(h.pending(planDBATopicDatabase))
	if err != nil {
		t.Fatalf("the database choice was not recorded as pending: %v", err)
	}

	if err := h.run(t); err != nil {
		t.Fatalf("planDBA, second run with nobody having confirmed: %v", err)
	}
	if len(h.sent) != 1 {
		t.Fatalf("the architect was dispatched again (%d times) though nobody confirmed anything",
			len(h.sent))
	}
	after, err := os.ReadFile(h.pending(planDBATopicDatabase))
	if err != nil {
		t.Fatalf("the pending record disappeared between runs: %v", err)
	}
	if string(before) != string(after) {
		t.Errorf("the pending record changed with nobody having confirmed it:\nbefore:\n%s\nafter:\n%s",
			before, after)
	}
	if !strings.Contains(h.out.String(), "is still a recommendation") {
		t.Errorf("the second run does not say the choice is still a recommendation:\n%s", h.out.String())
	}

	// A third run tells the same story again: still nothing to read, still
	// no dispatch, still the same file.
	if err := h.run(t); err != nil {
		t.Fatalf("planDBA, third run: %v", err)
	}
	if len(h.sent) != 1 {
		t.Fatalf("a third run with nobody having confirmed anything dispatched the architect "+
			"again (%d times)", len(h.sent))
	}
}

// brokenSlack answers every post with an error, the shape planDBA itself has
// to handle when Slack is unreachable -- distinct from deps.Decide.Slack
// being nil outright (runPlanDBA's path when slack.FromEnv fails), which
// decide.Recommend does not even attempt to post through.
type brokenSlack struct{ planSlack }

func (b *brokenSlack) PostTS(channel, text string) (string, error) {
	return "", fmt.Errorf("slack unavailable")
}

// Slack being unreachable does not stop the record from being written --
// unconfirmed is the safe state to be stuck in -- but the run has to say
// plainly that nobody was actually asked, since the record on its own reads
// exactly like one where the question went out and is only awaiting a
// reaction.
func TestWithSlackUnavailableTheRecordIsWrittenAndTheMissingQuestionIsReported(t *testing.T) {
	h := newPlanHarness(t, choiceReport)
	h.deps.Decide.Slack = &brokenSlack{}

	if err := h.run(t); err != nil {
		t.Fatalf("planDBA with Slack unavailable: %v", err)
	}
	if _, err := os.Stat(h.pending(planDBATopicDatabase)); err != nil {
		t.Fatalf("the recommendation was not recorded even though unconfirmed is the safe "+
			"state to be stuck in: %v", err)
	}
	if !strings.Contains(h.out.String(), "could not be posted") {
		t.Errorf("the run does not report that the Slack question could not be posted:\n%s",
			h.out.String())
	}
	if strings.Contains(h.out.String(), "asked in Slack") {
		t.Error("the run claims the question was asked in Slack when it was not")
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
