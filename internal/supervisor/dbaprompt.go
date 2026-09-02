package supervisor

import "strings"

// The database architect's prompts (OR-135).
//
// This actor exists for the reason QA does: the implementer optimises for
// making one ticket pass, and a schema decision is inherited by the next
// twenty tickets and is expensive to reverse once there is data in it. So the
// data model is reviewed by somebody who did not write the change.
//
// Three clauses carry the design here.
//
// IT PROPOSES; A HUMAN APPLIES. It may not run a migration, and it may not
// edit application source. A schema change made inside the review of a schema
// change is an unreviewed change riding along inside the review of somebody
// else's, which is the boundary QAPrompt draws for the same reason.
//
// IT NEVER TOUCHES PRODUCTION. Real tuning wants a live database to run
// EXPLAIN against, and that is qa.e2e_base_url's hazard with a sharper edge: a
// session that quietly found production can be asked to write to it. So the
// target is an explicit non-production DSN from config or there is no target
// at all, and with no target the review is static and the report says so.
//
// SAYING WHICH PATH IT TOOK IS PART OF THE REPORT. A static review and a
// measured one are different claims about a change, and a report that does not
// distinguish them lets the weaker one be read as the stronger.

// DBAClean is what the database architect writes when it has no findings.
//
// A sentinel for the reason QAClean is one: "the schema is fine" and "the
// schema is fine except the missing index below" are a clause apart in English
// and opposite in consequence, and reading one as the other either escalates a
// good change or ships a table scan.
const DBAClean = "DBA CLEAN"

// DBAFindings opens the findings block.
//
// TWO sentinels here where QA has one, and the second is the reason. QA's
// findings are recognised from the prose, by a word list that decides only
// whether Orion may skip re-asking -- it works because a failing test report
// says "failed". A database review's findings do not: "add an index on
// orders.customer_id, this is a sequential scan at ten million rows" is a
// serious finding containing not one word of failure. Reading that as no
// verdict at all would send every real finding this stage produces to a person
// instead of to the developer who could fix it in one round. So the report
// marks its own findings and nothing is inferred from prose.
const DBAFindings = "DBA FINDINGS"

// DBATarget is the database this review may reach, discovered from config
// rather than from the repository.
type DBATarget struct {
	// DSN is the explicit non-production connection string, or empty. Empty is
	// not "find one": see config.DBA.
	DSN string
}

// Path names which route the review took, for the console and the event log,
// the same reason QATools.Path exists: a review that quietly degraded to
// reading text looks identical to one that measured a query plan.
func (t DBATarget) Path() string {
	if strings.TrimSpace(t.DSN) != "" {
		return "a non-production database"
	}
	return "static review of the schema and migrations"
}

// DBAPrompt asks for the review of one change.
//
// diff is the change under review, base to HEAD. It is in the prompt rather
// than left for the agent to find because the review is scoped to what this
// ticket did: a database architect handed a repository and no diff reviews the
// whole schema, which is a different and far more expensive job than the one
// the stage is paying for.
func DBAPrompt(key, summary, description, diff string, target DBATarget) string {
	var b strings.Builder
	b.WriteString(join(
		"You are the database architect on this change. It is already committed on",
		"this branch. Review what it does to the data model.",
		"",
		key+": "+summary,
		"",
	))
	if strings.TrimSpace(description) != "" {
		b.WriteString(join("The issue says:", quote(description), "", ""))
	}
	b.WriteString(join(
		"THE CHANGE",
		quote(diff),
		"",
		"WHAT TO REVIEW",
		"1. The schema: normalisation, keys, constraints, nullability, column types",
		"   and widths. A column that permits a value the domain does not is a defect",
		"   even when nothing writes one today.",
		"2. The migration: whether it is reversible, whether it locks a table long",
		"   enough to matter at production size, whether it is safe to run while the",
		"   previous version of the application is still serving traffic.",
		"3. The indexes: what the new queries will actually scan, which index serves",
		"   them, and which existing index this change has made redundant.",
		"4. The queries the change introduces, against the schema as it now stands.",
		"",
		"Review this change, not the schema it landed in. Pre-existing problems the",
		"change did not touch are somebody else's ticket; naming them here buries the",
		"finding that is about this diff.",
		"",
	))
	b.WriteString(join(dbaTargetLines(target)...))
	b.WriteString("\n")
	b.WriteString(join(
		"",
		"WHAT YOU MAY NOT DO",
		"Do not run a migration, in either direction, against anything.",
		"Do not run DDL, DELETE, UPDATE, INSERT or TRUNCATE against anything.",
		"Do not edit application source, and do not edit the migration either. You",
		"propose; a person applies. A schema change made inside the review of a schema",
		"change is an unreviewed change, and the finding you would then report would",
		"be a finding about your own edit.",
		"",
		"WHAT TO REPORT",
		"If the data model in this change is sound, make "+DBAClean+" the last line you",
		"write, on its own. Orion reads that line literally; without it this change is",
		"treated as having open findings.",
		"",
		"Otherwise write "+DBAFindings+" on its own line and then the open findings and",
		"nothing else: one per line, each naming what is wrong, what it costs, and the",
		"change you propose instead. Orion reads that line literally too -- findings",
		"written without it are findings Orion cannot tell from a summary, and they",
		"reach nobody.",
		"",
		"Orion sends the findings to the developer who wrote the change and then asks",
		"you to look again, so write them for that reader. You are not blocking",
		"anything and you are not approving anything -- report what is true.",
		"",
		"Say in your report whether you measured anything or only read. A review that",
		"does not say which it was lets the weaker claim be read as the stronger.",
	))
	return b.String()
}

func dbaTargetLines(t DBATarget) []string {
	dsn := strings.TrimSpace(t.DSN)
	if dsn == "" {
		return []string{
			"WHERE YOU MAY LOOK",
			"No non-production database is configured, so there is nothing to connect",
			"to. Review the schema, the migrations and the queries as TEXT: reason about",
			"the plan the query planner would choose rather than asking one, and say in",
			"your report that no query plan was measured and why.",
			"",
			"Do not connect to a database you found in this repository, in an",
			"environment variable, in a compose file, or anywhere else. A connection",
			"string you discovered is a connection string nobody chose to give you, and",
			"the one this project would most regret you finding is the one most likely",
			"to be lying around.",
		}
	}
	return []string{
		"WHERE YOU MAY LOOK",
		"This non-production database, and nothing else:",
		"  " + dsn,
		"",
		"READ ONLY, and read only in the strictest sense: EXPLAIN, EXPLAIN ANALYZE on",
		"a SELECT, and reads of the catalogue. Nothing that writes, nothing that",
		"changes a schema, nothing that runs a migration.",
		"",
		"If that database is not reachable, say so and review statically instead. Do",
		"NOT substitute another target -- not one from an environment variable, not",
		"one from a compose file, not one you found in this repository. The whole",
		"reason this is configured rather than discovered is that a discovered",
		"database is one nobody chose to hand you.",
	}
}

// DBAFindingsMessage is what Orion carries back to the developer.
//
// A message into the existing session rather than a fresh run, for the reason
// QAFindingsMessage is: it wrote this code minutes ago, and re-deriving that
// context costs the price of the whole ticket again.
func DBAFindingsMessage(findings string) string {
	return join(
		"The database architect reviewed the data model in your change and these",
		"findings are open:",
		"",
		strings.TrimSpace(findings),
		"",
		"Fix what they describe, on this branch, and commit.",
		"",
		"A schema fix is a NEW migration, not an edit to one that has already been",
		"applied anywhere. If the migration in this change has not left this branch,",
		"amending it is fine; if you are not certain of that, add one.",
		"",
		"If you believe a finding is wrong, say so plainly rather than working around",
		"it, and the review will look again.",
		"",
		"Change nothing else. This branch is about to be reviewed.",
	)
}

// DBAReviewAgainMessage asks for a second look after a fix.
func DBAReviewAgainMessage() string {
	return join(
		"The developer has committed a fix for your findings. Review the data model",
		"again, as the branch stands now.",
		"",
		"Read what actually changed -- do not assume the fix is correct because it was",
		"described as one.",
		"",
		"If the data model is now sound, make "+DBAClean+" the last line you write, on",
		"its own. Otherwise write "+DBAFindings+" on its own line and then the findings",
		"that are still open, and only those: a finding repeated after it was addressed",
		"sends the developer back to a schema that is already right.",
	)
}

// DBAAskPrompt is the explicitly-invoked review: `orion dba`, for "this query
// got slow, look at it".
//
// It takes a question rather than a diff, because the moment this path exists
// for is one where nobody has written a ticket yet, let alone a change. There
// is no sentinel and no fix loop: a person asked, a person reads the answer.
func DBAAskPrompt(question string, target DBATarget) string {
	var b strings.Builder
	b.WriteString(join(
		"You are the database architect on this project. Somebody has asked you to",
		"look at something.",
		"",
		"THE QUESTION",
		strings.TrimSpace(question),
		"",
	))
	b.WriteString(join(dbaTargetLines(target)...))
	b.WriteString("\n")
	b.WriteString(join(
		"",
		"WHAT YOU MAY NOT DO",
		"Do not run a migration, in either direction, against anything.",
		"Do not run DDL, DELETE, UPDATE, INSERT or TRUNCATE against anything.",
		"Do not edit any file. You propose; a person applies.",
		"",
		"WHAT TO REPORT",
		"The answer, what it rests on, and the change you propose -- as the migration",
		"or the index you would write, in full, so a person can read it and apply it.",
		"Say whether you measured anything or only read.",
		"",
		"If the schema and the migrations do not contain the answer, say so. A",
		"confident answer to a question the committed schema cannot settle is worse",
		"than no answer: it arrives wrapped in your authority and reaches production",
		"as a migration.",
	))
	return b.String()
}
