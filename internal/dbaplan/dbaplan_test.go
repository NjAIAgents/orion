package dbaplan

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/orion-sdlc/orion/internal/config"
	"github.com/orion-sdlc/orion/internal/decide"
	"github.com/orion-sdlc/orion/internal/slack"
	"github.com/orion-sdlc/orion/internal/supervisor"
	"github.com/orion-sdlc/orion/internal/ui"
	"github.com/orion-sdlc/orion/internal/workspace"
)

// fakeSlack is the merge-approval surface, answering from fixtures -- the
// same shape internal/decide's own tests use, because this stage confirms
// through that package and nothing else.
type fakeSlack struct {
	ts        string
	posted    []string
	channels  []string
	reactions []slack.Reaction
}

func (f *fakeSlack) PostTS(channel, text string) (string, error) {
	f.posted = append(f.posted, text)
	f.channels = append(f.channels, channel)
	return f.ts, nil
}
func (f *fakeSlack) React(channel, ts, emoji string)                 {}
func (f *fakeSlack) BotID() string                                   { return "UBOT" }
func (f *fakeSlack) Replies(string, string) ([]slack.Message, error) { return nil, nil }
func (f *fakeSlack) UserName(id string) string                       { return id }
func (f *fakeSlack) Reactions(string, string) ([]slack.Reaction, error) {
	return f.reactions, nil
}

// runner records what the architect was asked and answers with a fixture.
type runner struct {
	prompts []string
	final   []string
	exit    int
}

func (r *runner) run(ws *workspace.Workspace, o supervisor.Options) (*supervisor.Result, error) {
	r.prompts = append(r.prompts, o.Prompt)
	final := ""
	if n := len(r.prompts) - 1; n < len(r.final) {
		final = r.final[n]
	}
	return &supervisor.Result{ExitCode: r.exit, Final: final}, nil
}

const (
	choiceAnswer = supervisor.DBARecommendation + "\nPostgres 16.\n" +
		"Rejected DynamoDB: the ledger query in the spec joins four entities.\n"
	schemaAnswer = supervisor.DBARecommendation + "\nCREATE TABLE ledger (id bigint primary key);\n"
)

func fixture(t *testing.T, r *runner, s decide.SlackAPI) (*workspace.Workspace, config.Config, Deps, *bytes.Buffer) {
	t.Helper()
	repo := t.TempDir()
	ws := &workspace.Workspace{ID: "payments", RepoPath: repo, Task: workspace.Task{
		Slug: "payments", Idea: "Take card payments, with a database behind it.",
		Slack: &workspace.SlackChannel{ID: "C1"},
	}}
	cfg := config.Config{}
	cfg.Paths.Intent, cfg.Paths.Specs = "docs/intent", "specs"
	cfg.Slack.MergeApprovers = []string{"U-APPROVER"}
	var buf bytes.Buffer
	return ws, cfg, Deps{
		Supervise: r.run, Slack: s,
		Now: func() time.Time { return time.Date(2026, 9, 5, 10, 0, 0, 0, time.UTC) },
	}, &buf
}

func run(t *testing.T, ws *workspace.Workspace, cfg config.Config, deps Deps, buf *bytes.Buffer) string {
	t.Helper()
	buf.Reset()
	// Real usage is one `orion run` process per invocation, so ui's
	// package-level console state starts fresh every time; simulating
	// several runs in one test process needs the same reset, or a line
	// identical to the previous simulated run's gets silently collapsed
	// as a same-process repeat (OR-154).
	ui.ConsoleReset()
	if err := Run(ws, cfg, deps, Options{Out: buf}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	return buf.String()
}

func pendingBody(t *testing.T, repo, key string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(repo, decide.PendingDir, key+".md"))
	if err != nil {
		t.Fatalf("read the pending record: %v", err)
	}
	return string(b)
}

func exists(repo, dir, key string) bool {
	_, err := os.Stat(filepath.Join(repo, dir, key+".md"))
	return err == nil
}

// The first step: the choice, with its reasoning, recorded where nothing
// downstream reads it.
func TestTheFirstRunRecommendsADatabaseUnconfirmed(t *testing.T) {
	r := &runner{final: []string{choiceAnswer}}
	ws, cfg, deps, buf := fixture(t, r, &fakeSlack{ts: "111.222"})

	out := run(t, ws, cfg, deps, buf)

	if len(r.prompts) != 1 {
		t.Fatalf("the architect ran %d times, want once", len(r.prompts))
	}
	if !strings.Contains(r.prompts[0], "docs/intent/payments.md") ||
		!strings.Contains(r.prompts[0], "specs/payments.spec.md") {
		t.Errorf("the choice prompt does not point at what it gathers from:\n%s", r.prompts[0])
	}
	body := pendingBody(t, ws.RepoDir(), "payments-database")
	// The reasoning, not just the name: the whole point of recording it.
	if !strings.Contains(body, "Postgres 16") || !strings.Contains(body, "Rejected DynamoDB") {
		t.Errorf("the record does not carry the recommendation and its reasoning:\n%s", body)
	}
	if !strings.Contains(body, "- Status: unconfirmed") {
		t.Errorf("the record does not say it is unconfirmed:\n%s", body)
	}
	if exists(ws.RepoDir(), decide.ConfirmedDir, "payments-database") {
		t.Errorf("an unconfirmed choice was written into %s, where later stages read it as agreed",
			decide.ConfirmedDir)
	}
	if !strings.Contains(out, "unconfirmed") {
		t.Errorf("the run does not say the choice is unconfirmed:\n%s", out)
	}
}

// The invariant, from the direction that matters: an unconfirmed database is
// not designed on. A second run while nobody has answered must not buy the
// schema -- it would be thrown away the moment somebody says no.
func TestNoSchemaIsDesignedWhileTheDatabaseIsUnconfirmed(t *testing.T) {
	r := &runner{final: []string{choiceAnswer, schemaAnswer}}
	ws, cfg, deps, buf := fixture(t, r, &fakeSlack{ts: "111.222"})
	run(t, ws, cfg, deps, buf)

	out := run(t, ws, cfg, deps, buf)

	if len(r.prompts) != 1 {
		t.Fatalf("the architect ran %d times; the schema was designed on a database "+
			"nobody has agreed to", len(r.prompts))
	}
	if exists(ws.RepoDir(), decide.PendingDir, "payments-schema") ||
		exists(ws.RepoDir(), decide.ConfirmedDir, "payments-schema") {
		t.Errorf("a schema record exists while the database is still a recommendation")
	}
	if !strings.Contains(out, "no schema was designed") {
		t.Errorf("the run does not say why it stopped:\n%s", out)
	}
}

// Once a person confirms, and only then, the schema is designed -- on the
// decision, quoted to the run that draws it.
func TestTheSchemaIsDesignedOnceTheDatabaseIsConfirmed(t *testing.T) {
	r := &runner{final: []string{choiceAnswer, schemaAnswer}}
	s := &fakeSlack{ts: "111.222"}
	ws, cfg, deps, buf := fixture(t, r, s)
	run(t, ws, cfg, deps, buf)

	s.reactions = []slack.Reaction{{Name: "white_check_mark", Users: []string{"U-APPROVER"}}}
	out := run(t, ws, cfg, deps, buf)

	if !exists(ws.RepoDir(), decide.ConfirmedDir, "payments-database") {
		t.Fatalf("the confirmed choice was not promoted:\n%s", out)
	}
	if len(r.prompts) != 2 {
		t.Fatalf("the architect ran %d times; the schema was not designed after "+
			"the confirmation", len(r.prompts))
	}
	if !strings.Contains(r.prompts[1], "Postgres 16") {
		t.Errorf("the schema prompt does not carry the confirmed decision:\n%s", r.prompts[1])
	}
	body := pendingBody(t, ws.RepoDir(), "payments-schema")
	if !strings.Contains(body, "CREATE TABLE ledger") {
		t.Errorf("the schema record does not carry the schema:\n%s", body)
	}
	// The schema is a recommendation too. Confirming the database confirmed
	// the database.
	if !strings.Contains(body, "- Status: unconfirmed") {
		t.Errorf("the schema was recorded as already agreed:\n%s", body)
	}
	if exists(ws.RepoDir(), decide.ConfirmedDir, "payments-schema") {
		t.Errorf("the schema went straight into %s", decide.ConfirmedDir)
	}
}

// Two records, not one: the choice's reasoning survives the schema's
// confirmation, which is the thing anybody asking "why this database" in
// eighteen months has to be able to read.
func TestConfirmingTheSchemaLeavesTheChoiceRecordIntact(t *testing.T) {
	r := &runner{final: []string{choiceAnswer, schemaAnswer}}
	s := &fakeSlack{ts: "111.222"}
	ws, cfg, deps, buf := fixture(t, r, s)
	run(t, ws, cfg, deps, buf)
	s.reactions = []slack.Reaction{{Name: "white_check_mark", Users: []string{"U-APPROVER"}}}
	run(t, ws, cfg, deps, buf) // designs the schema

	out := run(t, ws, cfg, deps, buf) // the schema is confirmed by the same reaction

	choice, err := os.ReadFile(filepath.Join(ws.RepoDir(), decide.ConfirmedDir, "payments-database.md"))
	if err != nil {
		t.Fatalf("the confirmed choice is gone: %v", err)
	}
	if !strings.Contains(string(choice), "Rejected DynamoDB") {
		t.Errorf("the schema's confirmation overwrote the choice's reasoning:\n%s", choice)
	}
	if !exists(ws.RepoDir(), decide.ConfirmedDir, "payments-schema") {
		t.Fatalf("the schema was not confirmed:\n%s", out)
	}
	if len(r.prompts) != 2 {
		t.Errorf("the architect ran %d times; a settled flow bought another run", len(r.prompts))
	}
}

// A run that marked no recommendation has proposed nothing. Recording its
// closing message instead would put an unmarked blob in front of somebody to
// confirm, and a confirmation is worth only what the confirmer read.
func TestNothingIsRecordedWithoutTheSentinel(t *testing.T) {
	r := &runner{final: []string{"I read the spec and I think Postgres is probably fine."}}
	ws, cfg, deps, buf := fixture(t, r, &fakeSlack{ts: "111.222"})

	err := Run(ws, cfg, deps, Options{Out: buf})

	if exists(ws.RepoDir(), decide.PendingDir, "payments-database") {
		t.Errorf("prose with no %s was recorded as a recommendation", supervisor.DBARecommendation)
	}
	// A step that recorded nothing has not advanced the flow, and saying so
	// with a zero exit tells whoever is driving it that it has.
	if err == nil {
		t.Fatalf("a run that recorded nothing reported success:\n%s", buf.String())
	}
	if !strings.Contains(err.Error(), supervisor.DBARecommendation) {
		t.Errorf("the error does not say what was missing: %v", err)
	}
}

// --dry-run says what it would do and spends nothing. A dry run that bought
// the agent turn and then declined to record it would be the expensive half
// of the step with none of the result.
func TestADryRunSpendsNothingAndRecordsNothing(t *testing.T) {
	r := &runner{final: []string{choiceAnswer}}
	ws, cfg, deps, buf := fixture(t, r, &fakeSlack{ts: "111.222"})

	if err := Run(ws, cfg, deps, Options{Out: buf, DryRun: true}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(r.prompts) != 0 {
		t.Errorf("--dry-run ran the architect %d times", len(r.prompts))
	}
	if exists(ws.RepoDir(), decide.PendingDir, "payments-database") {
		t.Error("--dry-run wrote a record")
	}
}

// The record is not just written -- it is what Slack was actually asked
// about, reasoning and all. A confirmation is only worth what the confirmer
// read, so the question posted must carry the same recommendation the
// pending file does, not a summary of it.
func TestTheRecommendationPostedToSlackCarriesTheReasoning(t *testing.T) {
	r := &runner{final: []string{choiceAnswer}}
	s := &fakeSlack{ts: "111.222"}
	ws, cfg, deps, buf := fixture(t, r, s)

	run(t, ws, cfg, deps, buf)

	if len(s.posted) != 1 {
		t.Fatalf("Slack was asked %d times, want once", len(s.posted))
	}
	if !strings.Contains(s.posted[0], "Postgres 16") || !strings.Contains(s.posted[0], "Rejected DynamoDB") {
		t.Errorf("what Slack was asked does not carry the recommendation's reasoning:\n%s", s.posted[0])
	}
}

// A run that exited non-zero produced nothing anybody can trust: the final
// message may be a half-written stream cut off mid-thought. Recording it
// anyway would put a broken proposal in front of somebody to confirm.
func TestArchitectRunWithNonZeroExitReturnsErrorAndRecordsNothing(t *testing.T) {
	r := &runner{final: []string{choiceAnswer}, exit: 1}
	ws, cfg, deps, buf := fixture(t, r, &fakeSlack{ts: "111.222"})

	err := Run(ws, cfg, deps, Options{Out: buf})

	if err == nil {
		t.Fatalf("a run that exited non-zero reported success:\n%s", buf.String())
	}
	if exists(ws.RepoDir(), decide.PendingDir, "payments-database") {
		t.Errorf("a failed run still recorded a recommendation")
	}
}

// A record is named after the workspace's slug. A workspace with no slug has
// nothing to name it after, so this must fail loudly rather than write a
// record under an empty or garbled key nothing downstream can find again.
func TestWorkspaceWithEmptySlugReturnsError(t *testing.T) {
	r := &runner{final: []string{choiceAnswer}}
	ws, cfg, deps, buf := fixture(t, r, &fakeSlack{ts: "111.222"})
	ws.Task.Slug = ""

	err := Run(ws, cfg, deps, Options{Out: buf})

	if err == nil {
		t.Fatalf("a workspace with no slug reported success:\n%s", buf.String())
	}
	if len(r.prompts) != 0 {
		t.Errorf("the architect ran %d times for a workspace with no slug", len(r.prompts))
	}
}

// erroringSlack answers Confirm reads normally but fails to post -- the
// shape a real outage takes: Slack is unreachable for writes, not silently
// wrong for reads.
type erroringSlack struct {
	fakeSlack
	postErr error
}

func (e *erroringSlack) PostTS(channel, text string) (string, error) {
	e.posted = append(e.posted, text)
	e.channels = append(e.channels, channel)
	return "", e.postErr
}

// Later stages read the two confirmed records directly off disk -- that is
// the whole interface (OR-154's "read off the disk, for free"). Once both
// are confirmed, a pending record must not exist to be misread as one.
func TestLaterStagesReadConfirmedNotPendingRecords(t *testing.T) {
	r := &runner{final: []string{choiceAnswer, schemaAnswer}}
	s := &fakeSlack{ts: "111.222"}
	ws, cfg, deps, buf := fixture(t, r, s)
	run(t, ws, cfg, deps, buf) // recommends the choice

	s.reactions = []slack.Reaction{{Name: "white_check_mark", Users: []string{"U-APPROVER"}}}
	run(t, ws, cfg, deps, buf) // confirms the choice, recommends the schema
	run(t, ws, cfg, deps, buf) // confirms the schema

	choice, err := os.ReadFile(filepath.Join(ws.RepoDir(), decide.ConfirmedDir, "payments-database.md"))
	if err != nil {
		t.Fatalf("a later stage cannot read the confirmed choice: %v", err)
	}
	if !strings.Contains(string(choice), "Postgres 16") {
		t.Errorf("the confirmed choice does not carry what was recommended:\n%s", choice)
	}
	schema, err := os.ReadFile(filepath.Join(ws.RepoDir(), decide.ConfirmedDir, "payments-schema.md"))
	if err != nil {
		t.Fatalf("a later stage cannot read the confirmed schema: %v", err)
	}
	if !strings.Contains(string(schema), "CREATE TABLE ledger") {
		t.Errorf("the confirmed schema does not carry what was recommended:\n%s", schema)
	}
	if exists(ws.RepoDir(), decide.PendingDir, "payments-database") ||
		exists(ws.RepoDir(), decide.PendingDir, "payments-schema") {
		t.Errorf("a pending record still exists once its confirmed counterpart is readable")
	}
}

// Running the stage again once both records are confirmed must not buy
// another architect turn: the flow has nothing left to advance.
func TestRunningAgainAfterBothConfirmedCompletesAndSpendsNothing(t *testing.T) {
	r := &runner{final: []string{choiceAnswer, schemaAnswer}}
	s := &fakeSlack{ts: "111.222"}
	ws, cfg, deps, buf := fixture(t, r, s)
	run(t, ws, cfg, deps, buf)
	s.reactions = []slack.Reaction{{Name: "white_check_mark", Users: []string{"U-APPROVER"}}}
	run(t, ws, cfg, deps, buf)
	run(t, ws, cfg, deps, buf)

	out := run(t, ws, cfg, deps, buf)

	if len(r.prompts) != 2 {
		t.Errorf("the architect ran %d times; running an already-settled flow spent another turn",
			len(r.prompts))
	}
	if !strings.Contains(out, "both confirmed") {
		t.Errorf("a settled flow does not say it is done:\n%s", out)
	}
}

// A Slack outage on the way out must not lose the recommendation: the
// record is the safe state to be stuck in, and the failure is only worth
// reporting, not fatal.
func TestSlackPostingFailureWarnsButStillWritesRecordToPending(t *testing.T) {
	r := &runner{final: []string{choiceAnswer}}
	s := &erroringSlack{fakeSlack: fakeSlack{ts: "111.222"}, postErr: fmt.Errorf("slack: connection refused")}
	ws, cfg, deps, buf := fixture(t, r, s)
	deps.Slack = s

	out := run(t, ws, cfg, deps, buf)

	body := pendingBody(t, ws.RepoDir(), "payments-database")
	if !strings.Contains(body, "Postgres 16") {
		t.Errorf("a Slack failure lost the recommendation instead of just the question:\n%s", body)
	}
	if !strings.Contains(out, "Slack was not asked") {
		t.Errorf("the run does not warn that Slack could not be reached:\n%s", out)
	}
}

// The recommendation is posted where the project actually talks: the
// workspace's own configured Slack channel, not some default or blank one.
func TestSlackMessagePostedToConfiguredProjectChannel(t *testing.T) {
	r := &runner{final: []string{choiceAnswer}}
	s := &fakeSlack{ts: "111.222"}
	ws, cfg, deps, buf := fixture(t, r, s)

	run(t, ws, cfg, deps, buf)

	if len(s.channels) != 1 {
		t.Fatalf("Slack was posted to %d channels, want one", len(s.channels))
	}
	if s.channels[0] != ws.Task.Slack.ID {
		t.Errorf("posted to channel %q, want the workspace's configured %q",
			s.channels[0], ws.Task.Slack.ID)
	}
}

// Two records, not one: the choice and the schema must exist as distinct
// files rather than one growing document, because they are confirmed by
// different people at different times.
func TestTwoSeparateRecordsExistForChoiceAndSchemaNotMerged(t *testing.T) {
	r := &runner{final: []string{choiceAnswer, schemaAnswer}}
	s := &fakeSlack{ts: "111.222"}
	ws, cfg, deps, buf := fixture(t, r, s)
	run(t, ws, cfg, deps, buf)
	s.reactions = []slack.Reaction{{Name: "white_check_mark", Users: []string{"U-APPROVER"}}}
	run(t, ws, cfg, deps, buf)

	choicePath := filepath.Join(ws.RepoDir(), decide.ConfirmedDir, "payments-database.md")
	schemaPath := filepath.Join(ws.RepoDir(), decide.PendingDir, "payments-schema.md")
	if choicePath == schemaPath {
		t.Fatalf("the choice and the schema resolved to the same path")
	}
	choice, err := os.ReadFile(choicePath)
	if err != nil {
		t.Fatalf("the choice record is missing: %v", err)
	}
	schema, err := os.ReadFile(schemaPath)
	if err != nil {
		t.Fatalf("the schema record is missing: %v", err)
	}
	if strings.Contains(string(choice), "CREATE TABLE ledger") {
		t.Errorf("the schema was merged into the choice record:\n%s", choice)
	}
	if strings.Contains(string(schema), "Rejected DynamoDB") {
		t.Errorf("the choice's reasoning was merged into the schema record:\n%s", schema)
	}
}

// Every record must point at the two documents it was grounded in, so a
// reader -- or the next advisor scoped to them -- can go check the source
// rather than trust the reasoning on its word.
func TestRecordsReferenceGroundingDocuments(t *testing.T) {
	r := &runner{final: []string{choiceAnswer, schemaAnswer}}
	s := &fakeSlack{ts: "111.222"}
	ws, cfg, deps, buf := fixture(t, r, s)
	run(t, ws, cfg, deps, buf)

	choiceBody := pendingBody(t, ws.RepoDir(), "payments-database")
	wantGrounding := "docs/intent/payments.md and specs/payments.spec.md"
	if !strings.Contains(choiceBody, wantGrounding) {
		t.Errorf("the choice record does not reference its grounding documents:\n%s", choiceBody)
	}

	s.reactions = []slack.Reaction{{Name: "white_check_mark", Users: []string{"U-APPROVER"}}}
	run(t, ws, cfg, deps, buf)

	schemaBody := pendingBody(t, ws.RepoDir(), "payments-schema")
	if !strings.Contains(schemaBody, wantGrounding) {
		t.Errorf("the schema record does not reference its grounding documents:\n%s", schemaBody)
	}
}

// No Slack is a normal configuration: the record is still written, and it
// stays where nothing downstream reads it.
func TestWithoutSlackTheRecordIsWrittenAndStaysUnconfirmed(t *testing.T) {
	r := &runner{final: []string{choiceAnswer, schemaAnswer}}
	ws, cfg, deps, buf := fixture(t, r, &fakeSlack{ts: "111.222"})
	deps.Slack = nil
	ws.Task.Slack = nil

	run(t, ws, cfg, deps, buf)
	run(t, ws, cfg, deps, buf)

	if !exists(ws.RepoDir(), decide.PendingDir, "payments-database") {
		t.Errorf("no record was written when Slack was absent")
	}
	if len(r.prompts) != 1 {
		t.Errorf("the architect ran %d times; an unconfirmable choice was designed on", len(r.prompts))
	}
}

// A poll before anybody has reacted must not buy the schema either -- the
// invariant holds across however many times the operator runs it, not just
// the very next one. Only once a reaction actually lands does the run after
// it design the schema, on the confirmed choice.
func TestThirdRunAfterDatabaseConfirmedDesignsTheSchema(t *testing.T) {
	r := &runner{final: []string{choiceAnswer, schemaAnswer}}
	s := &fakeSlack{ts: "111.222"}
	ws, cfg, deps, buf := fixture(t, r, s)

	run(t, ws, cfg, deps, buf) // 1: recommends the choice, unconfirmed
	run(t, ws, cfg, deps, buf) // 2: nobody has reacted yet; still pending
	if len(r.prompts) != 1 {
		t.Fatalf("the architect ran %d times before anybody confirmed anything", len(r.prompts))
	}

	s.reactions = []slack.Reaction{{Name: "white_check_mark", Users: []string{"U-APPROVER"}}}
	out := run(t, ws, cfg, deps, buf) // 3: confirmed; the schema is designed

	if !exists(ws.RepoDir(), decide.ConfirmedDir, "payments-database") {
		t.Fatalf("the choice was not confirmed:\n%s", out)
	}
	if len(r.prompts) != 2 {
		t.Fatalf("the architect ran %d times; the third run did not design the schema "+
			"on the confirmed choice", len(r.prompts))
	}
	if !exists(ws.RepoDir(), decide.PendingDir, "payments-schema") {
		t.Errorf("the schema was not recorded after the choice was confirmed")
	}
}

// The schema prompt does not paraphrase the decision -- it quotes the whole
// confirmed record, because a fresh session that has not seen the first run
// cannot be trusted with a summary of what was agreed.
func TestSchemaPromptIncludesTheFullConfirmedRecommendation(t *testing.T) {
	r := &runner{final: []string{choiceAnswer, schemaAnswer}}
	s := &fakeSlack{ts: "111.222"}
	ws, cfg, deps, buf := fixture(t, r, s)
	run(t, ws, cfg, deps, buf)

	s.reactions = []slack.Reaction{{Name: "white_check_mark", Users: []string{"U-APPROVER"}}}
	run(t, ws, cfg, deps, buf)

	confirmed, err := os.ReadFile(filepath.Join(ws.RepoDir(), decide.ConfirmedDir, "payments-database.md"))
	if err != nil {
		t.Fatalf("the confirmed choice is missing: %v", err)
	}
	if len(r.prompts) != 2 {
		t.Fatalf("the architect ran %d times, want the schema run too", len(r.prompts))
	}
	// The prompt quotes the record indented, line for line -- not a
	// paraphrase, so every line of what was confirmed must appear.
	for _, line := range strings.Split(strings.TrimRight(string(confirmed), "\n"), "\n") {
		if line == "" {
			continue
		}
		if !strings.Contains(r.prompts[1], line) {
			t.Errorf("the schema prompt is missing a line of the confirmed record: %q\nprompt:\n%s",
				line, r.prompts[1])
		}
	}
}

// The schema recommendation goes through the exact same unconfirmed flow as
// the database choice: written to the pending directory, marked unconfirmed,
// and asked about in Slack -- there is no second, lighter-weight path for the
// second record.
func TestSchemaRecommendationIsRecordedUnconfirmedLikeTheDatabaseChoice(t *testing.T) {
	r := &runner{final: []string{choiceAnswer, schemaAnswer}}
	s := &fakeSlack{ts: "111.222"}
	ws, cfg, deps, buf := fixture(t, r, s)
	run(t, ws, cfg, deps, buf)

	s.reactions = []slack.Reaction{{Name: "white_check_mark", Users: []string{"U-APPROVER"}}}
	run(t, ws, cfg, deps, buf)

	if exists(ws.RepoDir(), decide.ConfirmedDir, "payments-schema") {
		t.Errorf("the schema was written straight into %s", decide.ConfirmedDir)
	}
	body := pendingBody(t, ws.RepoDir(), "payments-schema")
	if !strings.Contains(body, "- Status: unconfirmed") {
		t.Errorf("the schema record does not say it is unconfirmed:\n%s", body)
	}
	if !strings.Contains(body, "CREATE TABLE ledger") {
		t.Errorf("the schema record does not carry the recommendation:\n%s", body)
	}
	if len(s.posted) != 2 {
		t.Errorf("Slack was asked %d times, want once for the choice and once for the schema",
			len(s.posted))
	}
	if !strings.Contains(s.posted[1], "CREATE TABLE ledger") {
		t.Errorf("what Slack was asked about the schema does not carry the recommendation:\n%s",
			s.posted[1])
	}
}

// The sentinel gate applies to the schema step too: a design step's run that
// marked no DBA RECOMMENDATION has proposed no schema, and recording its
// closing message anyway would put an unmarked blob in front of somebody to
// confirm.
func TestSchemaRecordWithoutTheSentinelIsRejected(t *testing.T) {
	r := &runner{final: []string{choiceAnswer, "I designed a schema, trust me."}}
	s := &fakeSlack{ts: "111.222"}
	ws, cfg, deps, buf := fixture(t, r, s)
	run(t, ws, cfg, deps, buf)

	s.reactions = []slack.Reaction{{Name: "white_check_mark", Users: []string{"U-APPROVER"}}}
	err := Run(ws, cfg, deps, Options{Out: buf})

	if err == nil {
		t.Fatalf("a schema run with no %s reported success:\n%s", supervisor.DBARecommendation, buf.String())
	}
	if !strings.Contains(err.Error(), supervisor.DBARecommendation) {
		t.Errorf("the error does not say what was missing: %v", err)
	}
	if exists(ws.RepoDir(), decide.PendingDir, "payments-schema") {
		t.Errorf("prose with no %s was recorded as a schema recommendation", supervisor.DBARecommendation)
	}
}

// A run after both the choice and the schema have been confirmed finds
// nothing left to do: both records already read as confirmed off the disk,
// with no reaction left to poll for.
func TestFourthRunAfterSchemaConfirmationFindsBothRecordsConfirmed(t *testing.T) {
	r := &runner{final: []string{choiceAnswer, schemaAnswer}}
	s := &fakeSlack{ts: "111.222"}
	ws, cfg, deps, buf := fixture(t, r, s)
	run(t, ws, cfg, deps, buf) // 1: recommends the choice
	s.reactions = []slack.Reaction{{Name: "white_check_mark", Users: []string{"U-APPROVER"}}}
	run(t, ws, cfg, deps, buf) // 2: confirms the choice, recommends the schema
	run(t, ws, cfg, deps, buf) // 3: confirms the schema, on the same reaction

	run(t, ws, cfg, deps, buf) // 4: a follow-up with nothing left to confirm

	if !exists(ws.RepoDir(), decide.ConfirmedDir, "payments-database") {
		t.Errorf("the database is not confirmed on the fourth run")
	}
	if !exists(ws.RepoDir(), decide.ConfirmedDir, "payments-schema") {
		t.Errorf("the schema is not confirmed on the fourth run")
	}
}

// The final state says so, in words, and buys nothing more to say it: once
// both records are confirmed the stage has nothing left to ask the architect,
// so a follow-up run must not spend another agent turn finding that out.
func TestOutputReportsBothConfirmedAndSpendsNoFurtherTurns(t *testing.T) {
	r := &runner{final: []string{choiceAnswer, schemaAnswer}}
	s := &fakeSlack{ts: "111.222"}
	ws, cfg, deps, buf := fixture(t, r, s)
	run(t, ws, cfg, deps, buf) // 1: recommends the choice
	s.reactions = []slack.Reaction{{Name: "white_check_mark", Users: []string{"U-APPROVER"}}}
	run(t, ws, cfg, deps, buf) // 2: confirms the choice, recommends the schema
	run(t, ws, cfg, deps, buf) // 3: confirms the schema

	promptsBefore := len(r.prompts)
	out := run(t, ws, cfg, deps, buf) // 4: both already confirmed

	if len(r.prompts) != promptsBefore {
		t.Errorf("the architect ran again once both records were confirmed: %d prompts, want %d",
			len(r.prompts), promptsBefore)
	}
	if !strings.Contains(out, "the database and the initial schema are both confirmed") {
		t.Errorf("the output does not report both as confirmed:\n%s", out)
	}
}
