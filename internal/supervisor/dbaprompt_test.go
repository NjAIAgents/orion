package supervisor

import (
	"strings"
	"testing"
)

// THE SAFETY PROPERTY, and the reason this actor is allowed near a database at
// all: it proposes and a person applies. Every prompt that can reach a
// database has to say so, or the constraint lives only in a design document.
func TestEveryDBAPromptForbidsRunningAMigration(t *testing.T) {
	prompts := map[string]string{
		"review": DBAPrompt("X-1", "add an index", "it got slow", "diff",
			DBATarget{DSN: "postgres://localhost/scratch"}),
		"review, static": DBAPrompt("X-1", "add an index", "it got slow", "diff", DBATarget{}),
		"ask": DBAAskPrompt("this query got slow",
			DBATarget{DSN: "postgres://localhost/scratch"}),
		"ask, static": DBAAskPrompt("this query got slow", DBATarget{}),
		"choose":      DBAChoosePrompt("ORPAY", "a payments ledger", []string{"specs/pay.spec.md"}),
		"schema": DBASchemaPrompt("ORPAY", "a payments ledger", "PostgreSQL 16",
			[]string{"specs/pay.spec.md"}),
	}
	for name, p := range prompts {
		low := strings.ToLower(p)
		if !strings.Contains(low, "do not run a migration") {
			t.Errorf("the %s prompt does not forbid running a migration:\n%s", name, p)
		}
		if !strings.Contains(low, "propose") {
			t.Errorf("the %s prompt does not say it proposes rather than applies", name)
		}
	}
}

// No DSN means STATIC, and it must say so rather than leaving the agent to
// find a database. The one it would find is the one lying around, and the one
// this project would most regret it finding is exactly that one.
func TestAStaticDBAReviewIsToldNotToGoLookingForADatabase(t *testing.T) {
	for name, p := range map[string]string{
		"review": DBAPrompt("X-1", "s", "d", "diff", DBATarget{}),
		"ask":    DBAAskPrompt("why is this slow", DBATarget{}),
	} {
		if !strings.Contains(p, "No non-production database is configured") {
			t.Errorf("the %s prompt does not say the review is static:\n%s", name, p)
		}
		for _, forbidden := range []string{"environment variable", "compose file"} {
			if !strings.Contains(p, forbidden) {
				t.Errorf("the %s prompt does not rule out a database found in a %s",
					name, forbidden)
			}
		}
	}
}

// With a DSN it may look at THAT ONE and be told to stop rather than
// substitute another when it is unreachable -- the same rule qa.e2e_base_url
// carries, on a target with a sharper edge.
func TestAConfiguredDBATargetIsTheOnlyOneAllowed(t *testing.T) {
	const dsn = "postgres://user:pw@localhost:5432/scratch"
	p := DBAPrompt("X-1", "s", "d", "diff", DBATarget{DSN: dsn})
	if !strings.Contains(p, dsn) {
		t.Fatalf("the prompt does not name the configured target:\n%s", p)
	}
	if !strings.Contains(p, "and nothing else") {
		t.Error("the prompt does not say the configured target is the only one")
	}
	if !strings.Contains(strings.ToLower(p), "read only") {
		t.Error("the prompt does not restrict the connection to reads")
	}
	if !strings.Contains(p, "substitute another") {
		t.Error("an unreachable target must not be silently substituted; that is how a " +
			"tuning session finds production")
	}
}

// Which path it took is part of the report. A static review reported as a
// measured one is a stronger claim than was made.
func TestPathNamesWhichReviewItWas(t *testing.T) {
	if got := (DBATarget{}).Path(); !strings.Contains(got, "static") {
		t.Errorf("Path() with no DSN = %q, want it to say static", got)
	}
	if got := (DBATarget{DSN: "postgres://localhost/x"}).Path(); strings.Contains(got, "static") {
		t.Errorf("Path() with a DSN = %q, want it to say a database was available", got)
	}
	p := DBAPrompt("X-1", "s", "d", "diff", DBATarget{})
	if !strings.Contains(p, "whether you measured anything or only read") {
		t.Error("the prompt does not ask the report to say which review it was")
	}
}

// The two sentinels are the whole verdict contract, so the prompt has to name
// both. Findings written without the marker are findings Orion cannot tell
// from a summary, and they reach nobody.
func TestTheReviewPromptNamesBothSentinels(t *testing.T) {
	p := DBAPrompt("X-1", "s", "d", "diff", DBATarget{})
	for _, s := range []string{DBAClean, DBAFindings} {
		if !strings.Contains(p, s) {
			t.Errorf("the review prompt never names %q", s)
		}
	}
	again := DBAReviewAgainMessage()
	for _, s := range []string{DBAClean, DBAFindings} {
		if !strings.Contains(again, s) {
			t.Errorf("the re-review message never names %q, so a second round has no "+
				"way to report a verdict", s)
		}
	}
}

// The review is scoped to the change, not to the schema it landed in: a
// database architect handed a repository and no diff reviews everything, which
// is a far more expensive job than the stage is paying for.
func TestTheReviewIsScopedToTheChange(t *testing.T) {
	p := DBAPrompt("X-1", "s", "d", "-- the diff\nCREATE INDEX ...", DBATarget{})
	if !strings.Contains(p, "CREATE INDEX") {
		t.Error("the diff is not in the prompt, so the review has to go and find the change")
	}
	if !strings.Contains(p, "Review this change, not the schema it landed in") {
		t.Error("the review is not scoped to the change; pre-existing findings bury the " +
			"one that is about this diff")
	}
}

// The developer must not be told to make the tests pass by weakening what
// caught the problem -- the schema version of QAFindingsMessage's rule, which
// here is "do not edit a migration that has already been applied".
func TestTheFindingsMessageSaysHowToFixASchema(t *testing.T) {
	m := DBAFindingsMessage("orders.customer_id has no index")
	if !strings.Contains(m, "orders.customer_id has no index") {
		t.Error("the findings did not reach the developer")
	}
	if !strings.Contains(m, "NEW migration") {
		t.Error("the message does not say a schema fix is a new migration rather than an " +
			"edit to one that has already been applied")
	}
}

// THE PLANNING ORDER (OR-154). The prompt that chooses the database must not
// also ask for the schema: a person confirming the engine would be confirming
// a schema they were never asked about, and the design is thrown away if the
// choice changes.
func TestTheChoosePromptDoesNotAskForASchema(t *testing.T) {
	p := DBAChoosePrompt("ORPAY", "a payments ledger", []string{"specs/pay.spec.md"})
	if !strings.Contains(p, "Do not design the schema") {
		t.Errorf("the choose prompt does not defer the schema:\n%s", p)
	}
	if !strings.Contains(p, "specs/pay.spec.md") {
		t.Error("the choose prompt does not name the committed artifacts to design from")
	}
	if !strings.Contains(p, "RECOMMEND NOTHING") {
		t.Error("the choose prompt does not tell it to recommend nothing when the artifacts " +
			"do not settle the choice; a database chosen from a sentence is chosen by " +
			"whoever wrote the sentence")
	}
}

// With nothing committed there is nothing to choose from, and the prompt has
// to say so rather than naming files that are not there -- an agent handed a
// path that does not exist goes looking, or invents what it would have said.
func TestWithNoCommittedArtifactsTheChoosePromptSaysSo(t *testing.T) {
	p := DBAChoosePrompt("ORPAY", "a payments ledger", nil)
	if !strings.Contains(p, "Nothing is committed yet") {
		t.Errorf("the prompt does not say there is nothing to design from:\n%s", p)
	}
}

// A choose prompt with no committed artifacts must not merely note the fact:
// it has to tell the architect to recommend nothing. Naming the gap without
// forbidding the guess leaves a model free to choose from the one-line idea
// anyway -- a database chosen from a sentence is a database chosen by
// whoever wrote the sentence.
func TestWithNoArtifactsTheChoosePromptSaysToRecommendNothing(t *testing.T) {
	p := DBAChoosePrompt("ORPAY", "a payments ledger", nil)
	if !strings.Contains(strings.ToLower(p), "recommend nothing") {
		t.Errorf("the choose prompt does not tell the architect to recommend nothing "+
			"when there is nothing committed to design from:\n%s", p)
	}
}

// The choose prompt must forbid designing the schema outright, in its own
// "WHAT YOU MAY NOT DO" clause -- not merely imply it by never mentioning a
// schema. A person confirming the engine would otherwise be confirming a
// schema they were never asked about.
func TestTheChoosePromptForbidsDesigningTheSchema(t *testing.T) {
	p := DBAChoosePrompt("ORPAY", "a payments ledger", []string{"specs/pay.spec.md"})
	if !strings.Contains(p, "Do not design the schema") {
		t.Errorf("the choose prompt does not forbid designing the schema:\n%s", p)
	}
	if !strings.Contains(p, "WHAT YOU MAY NOT DO") {
		t.Errorf("the prohibition is not filed under what the architect may not do:\n%s", p)
	}
}

// Both planning prompts have to ask for the reasoning as loudly as for the
// answer, because Orion refuses a report that carries only one of them.
func TestBothPlanningPromptsRequireTheReasoning(t *testing.T) {
	for name, p := range map[string]string{
		"choose": DBAChoosePrompt("ORPAY", "a payments ledger", nil),
		"schema": DBASchemaPrompt("ORPAY", "a payments ledger", "PostgreSQL 16", nil),
	} {
		for _, want := range []string{DBARecommends, DBABecause, "REFUSED"} {
			if !strings.Contains(p, want) {
				t.Errorf("the %s prompt does not mention %q:\n%s", name, want, p)
			}
		}
		if !strings.Contains(p, "unconfirmed") {
			t.Errorf("the %s prompt does not say what it writes is a recommendation "+
				"nobody has agreed to yet", name)
		}
	}
}

// The schema is designed against the record a person confirmed, quoted rather
// than paraphrased: Orion's summary of what was agreed is not what was agreed.
func TestTheSchemaPromptQuotesTheConfirmedChoice(t *testing.T) {
	p := DBASchemaPrompt("ORPAY", "a payments ledger",
		"# ORPAY: the database\n- Status: confirmed\n\nPostgreSQL 16", nil)
	if !strings.Contains(p, "PostgreSQL 16") || !strings.Contains(p, "Status: confirmed") {
		t.Errorf("the confirmed record is not in the schema prompt:\n%s", p)
	}
}

// VERBATIM, not a paraphrase. A summary of the confirmed record is Orion's
// words, not the words a person actually confirmed, and the two can quietly
// diverge -- a rewritten sentence, a dropped caveat -- without either side
// noticing. The full confirmed body has to appear in the prompt byte for
// byte, not merely its headline conclusion.
func TestTheSchemaPromptQuotesTheConfirmedRecordVerbatim(t *testing.T) {
	record := "# ORPAY: the database\n" +
		"- Status: confirmed\n" +
		"- By: U-APPROVER\n\n" +
		"PostgreSQL 16\n\n" +
		"BECAUSE the ledger is relational and the balance invariant needs a\n" +
		"transaction across two tables. I rejected DynamoDB: it would push that\n" +
		"invariant into application code."
	p := DBASchemaPrompt("ORPAY", "a payments ledger", record, nil)
	if !strings.Contains(p, quote(record)) {
		t.Errorf("the schema prompt does not carry the confirmed record verbatim:\n%s", p)
	}
}
