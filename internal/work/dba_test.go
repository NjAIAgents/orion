package work

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/orion-sdlc/orion/internal/events"
	"github.com/orion-sdlc/orion/internal/supervisor"
	"github.com/orion-sdlc/orion/internal/tracker"
	"github.com/orion-sdlc/orion/internal/workspace"
)

// dbaCfg is a project that has never heard of the stage, which is how a
// repository is configured by default and the case where it must run.
const dbaCfg = `{"vcs":{"default_branch":"main","work_branch":"develop","branch_prefix":"orion/"},
                 "tracker":{"enabled":true,"project_key":"FCIA","queue_label":"ORION"},
                 "qa":{"enabled":false}}`

// dbaFake is a Supervise stand-in that commits a file of the caller's choosing
// on the FIRST ticket run, so the branch's diff decides whether the stage
// triggers -- which is the thing under test.
type dbaFake struct {
	t *testing.T
	// file is what the implementer "wrote". The gate reads the diff, so this
	// is the whole input to the detection.
	file string
	// dbaReplies are the review's closing messages, in order. Past the end the
	// last one repeats, which is what a review that keeps finding the same
	// thing looks like.
	dbaReplies []string
	stages     []string
	prompts    []string
	dbaCalls   int
	n          int
}

func (f *dbaFake) run(ws *workspace.Workspace, o supervisor.Options) (*supervisor.Result, error) {
	f.stages = append(f.stages, o.Stage)
	f.prompts = append(f.prompts, o.Prompt)
	f.n++

	name := f.file
	if o.Stage != "ticket" || f.n > 1 {
		// Later runs write something unremarkable, so a fix round is not
		// "nothing to commit" and does not itself trigger the gate.
		name = "notes-" + o.Stage + itoa(f.n) + ".md"
	}
	path := filepath.Join(ws.RepoDir(), filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	if err := os.WriteFile(path, []byte("x\n"), 0o644); err != nil {
		return nil, err
	}
	git(f.t, ws.RepoDir(), "add", ".")
	git(f.t, ws.RepoDir(), "commit", "-q", "-m", o.Stage+" work")

	res := &supervisor.Result{ExitCode: 0, SessionID: o.Stage + "-session-" + itoa(f.n)}
	if o.Stage == "dba" {
		i := f.dbaCalls
		if i >= len(f.dbaReplies) {
			i = len(f.dbaReplies) - 1
		}
		if len(f.dbaReplies) > 0 {
			res.Final = f.dbaReplies[i]
		}
		f.dbaCalls++
	}
	return res, nil
}

func (f *dbaFake) sequence() string { return strings.Join(f.stages, ",") }

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

func runTicket(t *testing.T, home string, j *fakeJira, f *dbaFake) []Result {
	t.Helper()
	var out strings.Builder
	return Run(Options{Keys: []string{"FCIA-6"}, Out: &out, Home: home},
		Deps{
			Jira: j, Supervise: f.run,
			Push:   func(string, string) error { return nil },
			OpenPR: func(string, string, string, string, string) (string, error) { return "https://pr/1", nil },
		})
}

// THE ECONOMICS OF THE STAGE, and the assertion that keeps them true. A ticket
// that touches no data must not buy an opus review; the gate is deterministic
// and free, so this is a property rather than a hope.
func TestNoDatabaseReviewWhenTheChangeTouchesNoData(t *testing.T) {
	home := project(t, dbaCfg)
	f := &dbaFake{t: t, file: "cmd/orion/main.go"}

	res := runTicket(t, home, &fakeJira{}, f)

	if res[0].Outcome != OutcomeCIWait {
		t.Fatalf("outcome = %q", res[0].Outcome)
	}
	if strings.Contains(f.sequence(), "dba") {
		t.Errorf("the database review ran on a change with no data in it: %s.\n"+
			"This stage is only affordable because tickets it is not about pay nothing",
			f.sequence())
	}
}

// The other side: a migration in the diff is exactly what this stage exists
// for, and it has to trigger with no label and no planner involved.
func TestADatabaseReviewRunsWhenTheDiffTouchesAMigration(t *testing.T) {
	home := project(t, dbaCfg)
	f := &dbaFake{t: t, file: "migrations/0007_add_orders_index.sql",
		dbaReplies: []string{"DBA CLEAN"}}

	res := runTicket(t, home, &fakeJira{}, f)

	if res[0].Outcome != OutcomeCIWait {
		t.Fatalf("outcome = %q", res[0].Outcome)
	}
	if !strings.Contains(f.sequence(), "dba") {
		t.Errorf("no database review for a change that added a migration: %s", f.sequence())
	}
}

// The ticket's own markers are the other half of the signal, and they are the
// only one a ticket whose change happens not to touch a schema file has.
//
// The ticket here routes to the FRONTEND developer and carries a `database`
// marker as well -- a UI ticket a planner flagged as touching data, which is
// the shape this half of the signal is actually for. A ticket that routes to
// the database architect itself is covered below, and is deliberately not
// reviewed: see TestTheDatabaseArchitectDoesNotReviewItsOwnTicket.
func TestADatabaseReviewRunsOnTheTicketsOwnLabel(t *testing.T) {
	home := project(t, dbaCfg)
	j := &fakeJira{issue: &tracker.Issue{
		Key: "FCIA-6", Summary: "speed up the orders report",
		Description: "it got slow", Labels: []string{"ORION", "frontend", "database"},
		URL: "https://x/browse/FCIA-6",
	}}
	f := &dbaFake{t: t, file: "web/src/OrdersReport.tsx",
		dbaReplies: []string{"DBA CLEAN"}}

	runTicket(t, home, j, f)

	if !strings.Contains(f.sequence(), "dba") {
		t.Errorf("no database review for a ticket marked `database`: %s.\n"+
			"The diff touched no schema file, so the ticket's own marker was the only "+
			"signal there was", f.sequence())
	}
}

// Off means off, exactly as it does for QA: a repository with no database
// should not spend on the stage at all.
func TestTheDatabaseStageIsSkippedWhenSwitchedOff(t *testing.T) {
	home := project(t, `{"vcs":{"default_branch":"main","work_branch":"develop","branch_prefix":"orion/"},
	                     "tracker":{"enabled":true,"project_key":"FCIA","queue_label":"ORION"},
	                     "qa":{"enabled":false},"dba":{"enabled":false}}`)
	f := &dbaFake{t: t, file: "migrations/0007_add.sql"}

	runTicket(t, home, &fakeJira{}, f)

	if strings.Contains(f.sequence(), "dba") {
		t.Errorf("the database review ran for a project that switched it off: %s", f.sequence())
	}
}

// The exchange: the review finds something, the developer fixes it, the review
// looks again and it is sound. It must not escalate, and the pull request
// opens -- this stage reports, it does not block.
func TestSchemaFindingsGoToTheDeveloperAndAreReviewedAgain(t *testing.T) {
	home := project(t, dbaCfg)
	j := &fakeJira{}
	f := &dbaFake{t: t, file: "migrations/0007_add.sql", dbaReplies: []string{
		"DBA FINDINGS\norders.customer_id has no index; this report is a sequential scan.",
		"DBA CLEAN",
	}}

	res := runTicket(t, home, j, f)

	if res[0].Outcome != OutcomeCIWait {
		t.Fatalf("outcome = %q; the stage reports, it does not block", res[0].Outcome)
	}
	// dba, then the developer resumed, then dba again.
	if got := f.sequence(); !strings.Contains(got, "dba,ticket,dba") {
		t.Errorf("sequence = %s; want a review, a fix round, and a second review", got)
	}
	// The findings, in the developer's message, without the marker line.
	var fixPrompt string
	for i, s := range f.stages {
		if s == "ticket" && i > 0 {
			fixPrompt = f.prompts[i]
		}
	}
	if !strings.Contains(fixPrompt, "sequential scan") {
		t.Errorf("the fix round did not carry the findings:\n%s", fixPrompt)
	}
	if strings.Contains(fixPrompt, supervisor.DBAFindings) {
		t.Errorf("the marker line reached the developer as if it were a finding:\n%s", fixPrompt)
	}
	if !anyComment(j, "Database review round 1") {
		t.Errorf("no round comment on the ticket; weeks later it is the only place left "+
			"to look: %v", j.comments)
	}
}

// A review that keeps finding the same thing has to stop. The ceiling is a
// count rather than either agent's judgement, for the reason QA's is.
func TestTheSchemaFixLoopStopsAtTheCeilingAndTellsAPerson(t *testing.T) {
	home := project(t, `{"vcs":{"default_branch":"main","work_branch":"develop","branch_prefix":"orion/"},
	                     "tracker":{"enabled":true,"project_key":"FCIA","queue_label":"ORION"},
	                     "qa":{"enabled":false},"dba":{"max_rounds":1}}`)
	j := &fakeJira{}
	f := &dbaFake{t: t, file: "migrations/0007_add.sql", dbaReplies: []string{
		"DBA FINDINGS\nstill no index on orders.customer_id.",
	}}

	res := runTicket(t, home, j, f)

	if res[0].Outcome != OutcomeCIWait {
		t.Fatalf("outcome = %q; the run continues past an escalation, it is not blocked "+
			"on this stage's authority", res[0].Outcome)
	}
	// The first implementation run, plus exactly one fix round.
	if got := strings.Count(f.sequence(), "ticket"); got != 2 {
		t.Errorf("sequence = %s; max_rounds=1 buys exactly one fix round", f.sequence())
	}
	if !anyComment(j, "still open after 1 fix round") {
		t.Errorf("the ticket was not told the findings are still open: %v", j.comments)
	}
}

// THE OR-204 RULE, APPLIED TO THIS ACTOR. A review that named neither sentinel
// has no verdict in it, and dispatching that as findings tells a developer to
// fix something nobody described -- against a schema, the cheapest way to
// satisfy that is to drop whatever constraint looks closest, which makes the
// migration succeed rather than fail.
func TestAReviewWithNoVerdictGoesToAPersonAndNotToAFixRound(t *testing.T) {
	home := project(t, dbaCfg)
	j := &fakeJira{}
	f := &dbaFake{t: t, file: "migrations/0007_add.sql", dbaReplies: []string{
		"I looked at the schema and the migration. Here is a summary of the tables.",
	}}

	runTicket(t, home, j, f)

	if got := strings.Count(f.sequence(), "ticket"); got != 1 {
		t.Errorf("sequence = %s; an ambiguous review must not buy a fix round", f.sequence())
	}
	if !anyComment(j, "UNREVIEWED") {
		t.Errorf("the ticket does not say the data model is unreviewed: %v", j.comments)
	}
}

// The reviewer must not review its own change. A ticket labelled `database`
// routes to this actor, and the finding it would then report would be a
// finding about its own edit -- the boundary QAPrompt draws, one level up.
func TestTheDatabaseArchitectDoesNotReviewItsOwnTicket(t *testing.T) {
	home := project(t, dbaCfg)
	j := &fakeJira{issue: &tracker.Issue{
		Key: "FCIA-6", Summary: "add the orders index",
		Description: "it got slow", Labels: []string{"ORION", "schema"},
		URL: "https://x/browse/FCIA-6",
	}}
	f := &dbaFake{t: t, file: "migrations/0007_add.sql", dbaReplies: []string{"DBA CLEAN"}}

	runTicket(t, home, j, f)

	if strings.Contains(f.sequence(), "dba") {
		t.Errorf("the database architect reviewed its own change: %s", f.sequence())
	}
}

// The sentinels, read literally. Nothing is inferred from prose here: a schema
// finding contains no word of failure -- "this is a sequential scan" names
// none -- so QA's word list would read every real finding as no verdict at all.
func TestDBAVerdictReadsSentinelsAndNothingElse(t *testing.T) {
	cases := []struct {
		name  string
		final string
		want  qaVerdictKind
		body  string
	}{
		{"clean", "everything checks out.\nDBA CLEAN", qaVerdictClean, ""},
		{"clean with decoration", "**DBA CLEAN**", qaVerdictClean, ""},
		{"findings", "Summary:\nDBA FINDINGS\nno index on orders.customer_id.",
			qaVerdictFindings, "no index on orders.customer_id."},
		{"prose naming no failure", "The schema looks reasonable to me.", qaVerdictNone, ""},
		{"prose naming a scan", "orders.customer_id is a sequential scan at ten million rows.",
			qaVerdictNone, ""},
		{"marker with nothing under it", "DBA FINDINGS", qaVerdictNone, ""},
		{"nothing at all", "", qaVerdictNone, ""},
		{"quoting the instruction", `write "DBA CLEAN" when the schema is sound`, qaVerdictNone, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, kind := dbaVerdict(c.final)
			if kind != c.want {
				t.Errorf("kind = %v, want %v for %q", kind, c.want, c.final)
			}
			if strings.TrimSpace(got) != c.body {
				t.Errorf("findings = %q, want %q", got, c.body)
			}
		})
	}
}

// The routing table and the stage trigger are ONE vocabulary. A stage
// triggering on words `orion routes` does not print is the drift OR-191
// established there is exactly one list to avoid.
func TestADataTicketRoutesToTheDatabaseArchitect(t *testing.T) {
	for _, marker := range []string{"database", "schema", "migration", "SQL", "query"} {
		actor, why := Route(tracker.Issue{Key: "X-1", Labels: []string{"ORION", marker}})
		if actor != events.ActorDBA {
			t.Errorf("a %q ticket routed to %q (%s), want the database architect",
				marker, actor, why)
		}
	}
	// And an ordinary ticket still defaults, or every backend ticket has just
	// become a database ticket.
	if actor, _ := Route(tracker.Issue{Key: "X-1", Labels: []string{"ORION"}}); actor != DefaultActor {
		t.Errorf("an unmarked ticket routed to %q, want the default", actor)
	}
}

func anyComment(j *fakeJira, want string) bool {
	for _, c := range j.comments {
		if strings.Contains(c, want) {
			return true
		}
	}
	return false
}
