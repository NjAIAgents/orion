package supervisor

import "strings"

// The QA stage's prompts.
//
// QA exists because verification today is whatever the implementer wrote plus
// the CI gate, and both of those inherit the implementer's reading of the
// ticket. An agent that misread an acceptance criterion writes tests that
// agree with the misreading and a green build that proves nothing. So the
// cases are derived from the ticket, independently, by an actor that did not
// write the code.
//
// Two clauses here carry the design.
//
// The evidence standard: a test that would not fail if the behaviour
// regressed does not count. It is the same rule as the implementer prompt's
// EVIDENCE section and the CI-fix prompt's refusal to weaken a test, said a
// third time because this is the actor most able to satisfy the letter of
// "there are tests" without the substance.
//
// The boundary: tests only. QA writing application code would make it the
// second author of the diff under review, and the finding it reports would be
// a finding about its own change.

// QAClean is what the QA agent writes when its cases all pass.
//
// A sentinel rather than a reading of the prose, for the reason NoopMarker is
// one: "everything passes" and "everything passes except the two below" are a
// clause apart in English and opposite in meaning, and a run that reads one
// as the other either escalates a clean branch or ships a failing one.
const QAClean = "QA CLEAN"

// QATools is what this repository actually offers the QA agent, discovered at
// claim time rather than assumed.
type QATools struct {
	// Skills is true when nj-agents' testing class is installed, so the
	// prompt can name /test-suite-author and /e2e-suite instead of leaving
	// the agent to invent an approach.
	Skills bool
	// E2EBaseURL is the explicit non-production target, or empty. Empty is
	// not "find one": see config.QA.
	E2EBaseURL string
}

// Path names which route the stage took, for the console and the event log.
// A run that quietly degraded looks identical to one that did not.
func (t QATools) Path() string {
	if t.Skills {
		return "nj-agents testing skills"
	}
	return "this repository's own test tooling"
}

// QACasesPrompt asks a subagent to turn the ticket's acceptance criteria and
// the diff into the list of cases QA has to cover, and nothing else.
//
// Deriving the cases is wide reading with a short answer; writing the tests
// is the work. Done in one context they are paid for together: the criteria
// and the diff stay in the prompt for every turn of the authoring that
// follows, re-sent to say once what a dozen lines of case list say (OR-182).
//
// READ ONLY is load-bearing here for the reason it is in ExplorePrompt: this
// subagent shares a worktree with a QA run that is about to write test files
// into it, and there is no separate checkout to isolate it into.
//
// The cases have to be self-contained, because the run that receives them
// does not receive the criteria they came from -- that omission is the whole
// saving. A case that says "as described in the ticket" arrives at a reader
// who cannot follow the pointer.
func QACasesPrompt(key, summary, description, diff string) string {
	return join(
		"Work out what a QA engineer has to test about this change. Do not test it.",
		"",
		key+": "+summary,
		"",
		"THE ACCEPTANCE CRITERIA",
		quote(description),
		"",
		"THE DIFF",
		quote(diff),
		"",
		"WHAT TO RETURN",
		"The list of cases to cover, one per line, and nothing else. No preamble,",
		"no account of how you read it, no test code.",
		"Derive them from the criteria -- what SHOULD be true of this change --",
		"and use the diff only to see what the change actually touches. A case",
		"read off the implementation only re-states what it already does.",
		"Include the ones an implementer skips: boundaries, negative paths, error",
		"branches, authorisation.",
		"",
		"Write each case so it stands on its own. Whoever writes the tests gets",
		"this list and NOT the ticket text above, so a case that points back at",
		"the criteria points at something they cannot read.",
		"",
		"READ ONLY",
		"Do not edit, create, delete, run or commit anything. Another agent is",
		"about to write test files into this same worktree, and a write from you",
		"would land in the middle of its change.",
	)
}

// QAPrompt asks for the cases, the tests, and the run.
//
// cases is the derived case list from QACasesPrompt, or empty when that step
// did not run or did not produce one. Empty is today's behaviour: QA reads
// the criteria and derives the cases itself, inside its own run. A derive
// step that silently produced nothing must never be the reason a ticket has
// no tests (OR-182).
func QAPrompt(key, summary, description, cases string, tools QATools) string {
	var b strings.Builder
	b.WriteString(join(
		"You are the QA engineer on this change. The implementation is already",
		"committed on this branch. Verify it independently.",
		"",
		key+": "+summary,
		"",
	))
	derived := strings.TrimSpace(cases) != ""
	switch {
	case derived:
		b.WriteString(join(
			"These cases were derived from the ticket's acceptance criteria and the",
			"diff, by an analyst who did not write the code:",
			quote(cases), "", ""))
	case strings.TrimSpace(description) != "":
		b.WriteString(join("The issue says:", quote(description), "", ""))
	}
	step1 := "1. Derive the test cases from the ticket's acceptance criteria -- what SHOULD\n" +
		"   be true of this change, read from the ticket rather than from the diff.\n" +
		"   Deriving them from the implementation only re-states what it already does."
	if derived {
		step1 = "1. Cover the cases above. They are the specification you are verifying\n" +
			"   against; add any the list missed, and say so if one of them turns out\n" +
			"   not to be testable as written."
	}
	b.WriteString(join(
		"WHAT TO DO",
		step1,
		"2. Read what the implementer already tested, and write the cases it missed:",
		"   boundaries, negative paths, error branches, authorisation.",
		"3. Run the suite and report the result per case.",
		"",
		"THE STANDARD",
		"A test that would not fail if this behaviour regressed does not count. Assert",
		"on the behaviour the ticket describes, not on the shape of the code that",
		"happens to implement it today.",
		"",
		"WHAT YOU MAY CHANGE",
		"Test files only, inside the directories this repository already keeps its",
		"tests in. Do not touch application source: a fix from you is an unreviewed",
		"change riding along inside the verification of somebody else's, and the",
		"finding you would then report would be a finding about your own edit.",
		"Do not weaken, skip or delete an existing assertion. If one looks wrong, say",
		"so in your report and leave it alone.",
		"",
	))
	b.WriteString(join(qaToolLines(tools)...))
	b.WriteString("\n")
	b.WriteString(join(
		"",
		"WHAT TO REPORT",
		"If every case passes, make "+QAClean+" the last line you write, on its own.",
		"Orion reads that line literally; without it the change is treated as failing.",
		"",
		"Otherwise, end with the open findings and nothing else: one per line, each",
		"naming the case, what you expected, and what happened. Orion sends them to",
		"the developer who wrote the change and then asks you to re-verify, so write",
		"them for that reader. You are not blocking anything and you are not",
		"approving anything -- report what is true.",
		"",
		"COMMITS",
		"Commit the tests you wrote on this branch, whether they pass or fail: a",
		"failing test that names a real defect is the evidence for the finding. Do",
		"not push, merge, or open a pull request -- Orion does that.",
	))
	return b.String()
}

func qaToolLines(t QATools) []string {
	lines := []string{"TOOLING"}
	if t.Skills {
		lines = append(lines,
			"nj-agents is installed here. Use /test-suite-author to go from the ticket",
			"to cases to specs to fixtures, and /e2e-suite to execute. They carry the",
			"contract in CONVENTIONS-testing.md, which is where the rules above come",
			"from, so following them is cheaper than re-deriving them.")
	} else {
		lines = append(lines,
			"nj-agents is not installed here, so there are no testing skills to call.",
			"Use this repository's own framework, runner and conventions -- the ones",
			"the existing tests already use. Do not install a test framework or add a",
			"dependency to get one.")
	}
	if t.E2EBaseURL != "" {
		lines = append(lines,
			"",
			"An end-to-end run may target "+t.E2EBaseURL+" and nothing else. If that",
			"target is not reachable, say so and stop rather than substituting another.")
	} else {
		lines = append(lines,
			"",
			"No non-production target is configured, so do not run an end-to-end suite",
			"against anything: author and run unit and integration tests only, and say",
			"in your report that end-to-end coverage was not attempted and why.")
	}
	return lines
}

// QAFindingsMessage is what Orion carries back to the implementer.
//
// A message into the implementer's existing session rather than a fresh run:
// it wrote this code minutes ago and re-deriving that context costs the price
// of the whole ticket again.
func QAFindingsMessage(findings string) string {
	return join(
		"QA verified your change independently and these findings are open:",
		"",
		strings.TrimSpace(findings),
		"",
		"Fix the behaviour they describe, on this branch, and commit.",
		"",
		"Do not change, skip or delete the tests that caught this to make them pass.",
		"A green suite bought that way is worse than a red one: the defect is still",
		"there and nothing is watching for it any more. If you believe a finding is",
		"wrong -- the test asserts something the ticket never asked for -- say so",
		"plainly instead of editing it, and QA will look again.",
		"",
		"Change nothing else. This branch is about to be reviewed.",
	)
}

// QAVerdictMessage asks for the one line the last run owed and did not write.
//
// Sent when a QA run ended with neither QAClean nor a finding. That run has
// no verdict in it, and the two things Orion could do without asking are both
// wrong: reading the prose is how a failing branch ships, and treating it as
// findings sends the developer to fix a defect nobody described. So it asks
// -- once, cheaply, of the actor that already did the work (OR-204).
func QAVerdictMessage() string {
	return join(
		"You ended without "+QAClean+" and without findings, so Orion has no verdict",
		"from you. It will not guess one from your prose, and it will not send a",
		"developer to fix something you did not describe.",
		"",
		"Reply with the verdict, and nothing else.",
		"",
		"If every case you ran passes, make "+QAClean+" the last line you write, on",
		"its own. Otherwise end with the open findings and only those: one per line,",
		"each naming the case, what you expected, and what happened.",
		"",
		"This is the conclusion you already reached, written in the form Orion reads.",
		"Do not re-run the suite, do not write another test, and change nothing.",
	)
}

// QAReverifyMessage asks QA to look again after a fix.
func QAReverifyMessage() string {
	return join(
		"The developer has committed a fix for your findings. Re-run your cases",
		"against the branch as it stands now.",
		"",
		"Re-run them -- do not assume the fix worked because it was described as one.",
		"",
		"If everything passes, make "+QAClean+" the last line you write, on its own.",
		"Otherwise end with the findings that are still open, and only those: a",
		"finding repeated after it was fixed sends the developer back to code that is",
		"already correct.",
	)
}
