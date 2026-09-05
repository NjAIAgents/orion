package supervisor

import "strings"

// The database architect's prompts for PLANNING (OR-154).
//
// The review prompts next door (dbaprompt.go) all have a change to look at.
// These two do not: they run before anything exists, when the only ground
// truth is what the intent and the spec say the project is for. That is why
// they are separate prompts rather than a mode of DBAPrompt -- an actor asked
// to review a diff that is not there reviews the repository instead, which is
// a different and far more expensive job.
//
// TWO PROMPTS, NOT ONE, AND THE ORDER IS THE POINT. The database is chosen
// first and confirmed by a person before the schema is designed on it. A
// schema designed against a database nobody has agreed to is work that is
// thrown away the moment they say no -- and worse, it is a second unconfirmed
// artifact arguing for the first. So the choose prompt is forbidden to design
// a schema, and the schema prompt is given the confirmed choice as settled.
//
// NEITHER PROMPT WRITES THE RECORD. The agent reports; Orion records, through
// internal/decide, which is what puts the proposal in the pending directory,
// asks Slack about it and marks it unconfirmed. An agent that filed its own
// recommendation into the repository would be filing something that reads as
// settled to every later stage -- the exact laundering internal/decide exists
// to prevent -- and the record would carry no Slack message, so nobody could
// ever confirm it.

// DBARecommendation opens the block Orion records.
//
// A sentinel for the reason DBAFindings is one, with an extra edge here: what
// follows it is not read by Orion at all, it is copied verbatim into a
// document a named person is asked to approve. Inferring the boundary from
// prose would put the agent's preamble -- "I read the spec, here is my
// thinking" -- into the thing being confirmed.
const DBARecommendation = "DBA RECOMMENDATION"

// DBARecommends returns what the agent asked to have recorded, and whether it
// marked anything at all.
//
// Everything from the marker to the end, trimmed. A marker with nothing under
// it is NOT a recommendation: the run said it had something and then did not
// say what, and recording that would put an empty proposal in front of a
// person to confirm.
func DBARecommends(final string) (string, bool) {
	lines := strings.Split(final, "\n")
	for i, line := range lines {
		trimmed := strings.TrimLeft(strings.TrimSpace(line), "*#->_ ")
		if len(trimmed) < len(DBARecommendation) ||
			!strings.EqualFold(trimmed[:len(DBARecommendation)], DBARecommendation) {
			continue
		}
		if rest := strings.TrimSpace(strings.Join(lines[i+1:], "\n")); rest != "" {
			return rest, true
		}
		return "", false
	}
	return "", false
}

// DBAPlanChoosePrompt asks which database this project should use, and why.
//
// The reasoning is asked for as loudly as the choice, because it is the part
// that decays first. "Postgres" is not something a reader can evaluate or
// revisit; it is a name, and eighteen months later the only way to find out
// what it was weighed against is to ask somebody who has left.
func DBAPlanChoosePrompt(idea, intentPath, specPath string) string {
	return join(
		"You are the database architect on this project. Nothing has been built yet:",
		"this is the planning phase, and you are being asked to settle one question",
		"before anybody writes code against a guess.",
		"",
		"THE PROJECT",
		quote(idea),
		"",
		"READ FIRST",
		"  "+intentPath+"  -- what is being built, why, and how success is measured",
		"  "+specPath+"  -- the requirements and the design",
		"Read both before you decide anything. They are the only ground truth here;",
		"there is no code to look at and no data to measure.",
		"",
		"WHAT TO DECIDE",
		"Whether this project needs a database at all, and if it does, which one.",
		"\"None\" is a real answer and a good one when it is true -- a project that",
		"keeps its state in files it already writes should not be handed a server to",
		"run for the rest of its life.",
		"",
		"GATHER WHAT YOU NEED FIRST, from those two documents: the entities and how",
		"they relate, the read and write patterns, the volume and growth expected, the",
		"consistency and transactional boundaries, the latency the product promises,",
		"and who will operate the thing once it exists.",
		"",
		"Where the documents do not say, SAY SO AND SAY WHAT YOU ASSUMED. An assumption",
		"written as a statement is indistinguishable from a decision by the time the",
		"next stage reads it, and every stage after this one inherits this choice.",
		"",
		"RECOMMEND EXACTLY ONE, WITH THE REASONING. The reasoning matters as much as",
		"the choice and is the part nobody can reconstruct later:",
		"  - what the requirements above demand of a database, in their own terms",
		"  - why the one you name meets them",
		"  - what you rejected, and the specific reason each was rejected",
		"  - what would have to change for this to be the wrong answer",
		"Somebody reading this in eighteen months must be able to ask \"why this one\"",
		"and get an answer, rather than start an archaeology project.",
		"",
		"DO NOT DESIGN THE SCHEMA YET. That is the next question and it is a separate",
		"recommendation. It is not asked for until a person has confirmed the database,",
		"because a schema designed on a database nobody agreed to is thrown away when",
		"they say no -- and until then it is a second unconfirmed document arguing for",
		"the first.",
		"",
		"WHAT YOU MAY NOT DO",
		"Create nothing, install nothing, connect to nothing. Do not go looking for a",
		"database in this repository, in an environment variable or in a compose file:",
		"a connection string you discovered is one nobody chose to give you.",
		"Write no file. Orion records what you report, in the shape a person is asked",
		"to confirm; a proposal you file yourself is one every later stage reads as",
		"settled the day you wrote it.",
		"",
		"WHAT TO REPORT",
		"Write "+DBARecommendation+" on its own line, and then the recommendation and",
		"nothing else. Orion copies everything after that line VERBATIM into the record",
		"a named person is asked to confirm, so write it for that reader and put your",
		"working above the line, not below it. Without the line nothing is recorded --",
		"Orion reads it literally and infers nothing from prose.",
	)
}

// DBAPlanSchemaPrompt asks for the initial schema, on the database a person
// has already confirmed.
//
// choice is the CONFIRMED record, quoted in full rather than summarised. This
// run is a fresh session that has not seen the first one, and a paraphrase of
// what was agreed is a second account of it that can disagree with the first.
func DBAPlanSchemaPrompt(choice, intentPath, specPath string) string {
	return join(
		"You are the database architect on this project. The database is settled: a",
		"person confirmed this recommendation, so it is a decision and you may design",
		"on it.",
		"",
		"THE CONFIRMED DECISION",
		quote(choice),
		"",
		"READ FIRST",
		"  "+intentPath,
		"  "+specPath,
		"The schema follows the requirements in those documents, not a shape that is",
		"conventional for this kind of project.",
		"",
		"WHAT TO DESIGN",
		"The INITIAL schema, and only the initial one: the tables or collections the",
		"spec actually calls for, their keys, their constraints, their nullability and",
		"their column types and widths. A column that permits a value the domain does",
		"not is a defect on the day it is created, and it is cheapest to fix now --",
		"before there is data in it -- which is the whole reason this is asked before",
		"anybody implements.",
		"",
		"Then the indexes the spec's read patterns need, each named with the query it",
		"serves, and the first migration that creates all of it.",
		"",
		"Do not revisit the database. It has been agreed. If you now believe it is the",
		"wrong one, say that plainly instead of designing around it -- a schema quietly",
		"shaped to compensate for a decision you disagree with is the worst of both.",
		"",
		"WHAT YOU MAY NOT DO",
		"Create nothing, install nothing, connect to nothing, and run no migration in",
		"either direction. Write no file: Orion records what you report, and this is a",
		"RECOMMENDATION until somebody confirms it too. You propose; a person applies.",
		"",
		"WHAT TO REPORT",
		"Write "+DBARecommendation+" on its own line, and then the schema and nothing",
		"else -- the DDL in full, the indexes, the migration, and one line per table",
		"saying what it is for. Orion copies everything after that line VERBATIM into",
		"the record a named person is asked to confirm. Without the line nothing is",
		"recorded.",
	)
}
