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
