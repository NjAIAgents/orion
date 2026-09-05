package dbaplan

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/orion-sdlc/orion/internal/config"
	"github.com/orion-sdlc/orion/internal/decide"
	"github.com/orion-sdlc/orion/internal/slack"
	"github.com/orion-sdlc/orion/internal/supervisor"
	"github.com/orion-sdlc/orion/internal/workspace"
)

// fakeSlack is the merge-approval surface, answering from fixtures -- the
// same shape internal/decide's own tests use, because this stage confirms
// through that package and nothing else.
type fakeSlack struct {
	ts        string
	posted    []string
	reactions []slack.Reaction
}

func (f *fakeSlack) PostTS(channel, text string) (string, error) {
	f.posted = append(f.posted, text)
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

func fixture(t *testing.T, r *runner, s *fakeSlack) (*workspace.Workspace, config.Config, Deps, *bytes.Buffer) {
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
