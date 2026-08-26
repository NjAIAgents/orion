package supervisor

import (
	"fmt"
	"strings"

	"github.com/orion-sdlc/orion/internal/workspace"
)

// stagePrompt builds the prompt for a stage.
//
// Every prompt ends by naming the artifact the stage must commit, because
// the artifact is the handoff. A stage that finishes without writing its
// artifact has not finished: the next stage reads files, not conversation,
// so an unwritten artifact breaks the chain silently.
//
// These are starting prompts. The skills shipped with the plugin carry
// the detail; keeping the prompt short here means the skill stays the
// single source of truth rather than drifting against a duplicate.
func stagePrompt(ws *workspace.Workspace, stage string) (string, error) {
	idea := ws.Task.Idea

	switch strings.ToLower(stage) {
	case "intent":
		return join(
			"Capture the intent behind this idea, in the originator's words:",
			quote(idea),
			"",
			"Use the /capture-intent skill. It writes docs/intent/<slug>.md with a fixed",
			"shape and proposes the commit; the path is part of its contract, so do not",
			"relocate the file. /pm-plan later points at this capture as grounding.",
			"",
			"Interrogate the idea first, the way an analyst would: who is affected, what is",
			"out of scope, what constraints apply, what success looks like. Where it is",
			"ambiguous, record the question rather than inventing an answer.",
			"",
			"Record only what was actually said. Do not write code or design a solution.",
		), nil

	case "spec", "design":
		return join(
			"Read docs/intent/"+ws.Task.Slug+".md.",
			"",
			"Produce a requirements and design spec. Apply every skill available to you so the",
			"design conforms to the security, UX and API standards in force.",
			"",
			"Flag areas of concern explicitly, especially anywhere two policies contradict and",
			"you cannot satisfy both. A flagged concern is more useful than a confident guess.",
			"",
			"Write specs/"+ws.Task.Slug+".spec.md and commit it. No implementation.",
		), nil

	case "plan":
		return join(
			"Read docs/intent/"+ws.Task.Slug+".md and specs/"+ws.Task.Slug+".spec.md.",
			"",
			"Produce an implementation plan naming: the files that change, the order of work,",
			"the tests that prove it, and the risks. Interrogate your own plan: what could this",
			"break, which step is riskiest, what did you reject and why.",
			"",
			"The bar: an engineer who has never seen this conversation could implement the change",
			"from the plan alone.",
			"",
			"Write plans/"+ws.Task.Slug+".plan.md and commit it. Do not implement yet.",
		), nil

	case "ticket":
		// Filled in by TicketPrompt, which needs the issue. Reaching this
		// through stagePrompt means a caller forgot to supply one, and
		// inventing a task from the workspace idea would be the agent
		// working on something nobody asked for.
		return "", fmt.Errorf("the ticket stage requires an issue: use TicketPrompt")

	case "scaffold":
		return join(
			"Lay out the repository skeleton for this project.",
			"",
			"Use the /scaffold-project skill. It grounds the security and governance",
			"layer in the OpenSSF OSPS Baseline and delegates the stack layout to the",
			"ecosystem's own generator rather than inventing one.",
			"",
			"Read docs/intent/"+ws.Task.Slug+".md and specs/"+ws.Task.Slug+".spec.md first",
			"so the stack choice follows the design rather than a default.",
			"",
			"Work on a branch cut from develop. Do not commit to develop or main directly;",
			"the gate will refuse it and the refusal is correct.",
		), nil

	case "decompose":
		return join(
			"Decompose plans/"+ws.Task.Slug+".plan.md into tracker work items.",
			"",
			"Use /pm-plan. Preview the ENTIRE Epic, Story and Task tree and wait for",
			"explicit approval before creating anything.",
			"",
			"This approval is not optional and is not waived by auto-merge being on.",
			"A sandboxed workspace can be deleted; issues in a shared tracker are seen",
			"by other people and cannot be cleanly withdrawn.",
			"",
			"Search the tracker first and reconcile, so a re-run never double-creates.",
		), nil

	case "build", "implement":
		return join(
			"Implement plans/"+ws.Task.Slug+".plan.md.",
			"",
			"First cut a branch from develop for this task. Every task gets its own",
			"branch; it merges into develop, and develop reaches main later through",
			"its own reviewed pull request.",
			"",
			"Rules:",
			"- Follow the plan. If implementation must depart from it, update the plan in the",
			"  same commit rather than letting the two drift apart.",
			"- Write the tests the plan names. Run them. Do not report done until they pass.",
			"- Do not widen scope. Anything you notice but were not asked to fix goes in a note,",
			"  not in this diff.",
			"",
			"Commit the diff and its tests.",
		), nil

	case "verify", "test":
		return join(
			"Verify the change works.",
			"",
			"Run the build, the tests and the linter. Exercise the changed behaviour and the two",
			"nearest neighbouring flows. Report what you ran, what you saw, and anything that does",
			"not match plans/"+ws.Task.Slug+".plan.md.",
			"",
			"Report only. Do not fix anything you find; a fix here would be an unreviewed change",
			"riding along with a verification pass.",
		), nil

	case "review":
		return join(
			"Review the working tree against REVIEW.md.",
			"",
			"Run three passes and tag every finding with its pass: Bugs, Security, Compliance",
			"against the spec and plan. Rank findings by severity. Reserve Important for anything",
			"that would break behaviour, leak data or breach a policy.",
			"",
			"You are not approving anything. Produce findings; a human decides.",
		), nil

	case "pr", "ship":
		return join(
			"Open a pull request for this change.",
			"",
			"Use /pr-describe rather than composing the PR by hand. It grounds the title",
			"and body in the actual commits and diff, and fills the repository's own",
			"PULL_REQUEST_TEMPLATE.md when one exists.",
			"",
			"The base branch is develop, not main. Feature branches merge into develop;",
			"develop reaches main later through its own reviewed pull request.",
			"",
			"Link intent, spec and plan in the body, and attach the verification output.",
			"",
			"Never push to develop or main directly. If a push is refused, the refusal is",
			"correct: report it rather than working around it.",
		), nil

	default:
		return "", fmt.Errorf(
			"unknown stage %q (want: intent, spec, plan, scaffold, decompose, build, verify, review, pr)", stage)
	}
}

func join(lines ...string) string { return strings.Join(lines, "\n") }

func quote(s string) string {
	var b strings.Builder
	for _, line := range strings.Split(s, "\n") {
		b.WriteString("  " + line + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// FixPrompt asks an agent to make its own branch pass CI.
//
// Narrower than the ticket prompt on purpose. The agent already built this
// change and believed it worked; what it needs now is not latitude but a
// specific failure and a boundary. The failure mode being guarded against is
// an agent that "fixes" CI by weakening the test that caught it, which
// produces a green build and a defect, and is the single most likely wrong
// move available here.
func FixPrompt(key, branch, failure string) string {
	return strings.Join([]string{
		"CI failed on your branch. Fix it.",
		"",
		"Ticket: " + key,
		"Branch: " + branch + " (you are already on it; do not create another)",
		"",
		"THE FAILURE",
		strings.TrimSpace(failure),
		"",
		"WHAT TO DO",
		"Reproduce it locally first -- run ./scripts/test.sh if it exists, or the",
		"project's own suite. A fix for a failure you have not seen is a guess.",
		"Then make the smallest change that makes it pass.",
		"",
		"WHAT NOT TO DO",
		"Do not delete, skip, weaken or rewrite a test to make it pass unless the",
		"TEST is what is wrong -- and if it is, say so explicitly and explain why",
		"in the commit message. A green build bought by removing the check that",
		"failed is worse than a red one: the defect is still there and nothing is",
		"watching for it any more.",
		"Do not fix unrelated failures or tidy code you happen to be reading. This",
		"branch is under review, and a diff that also changes three other things",
		"is one a reviewer cannot approve without re-reading all of it.",
		"",
		"IF YOU CANNOT FIX IT",
		"Stop and say why, as the last thing you say. Do not commit a partial or",
		"speculative change hoping CI disagrees. Orion re-runs CI on whatever you",
		"push, so a guess costs a full build to disprove -- and if you produce no",
		"commit, Orion stops and asks a person, which is the correct outcome when",
		"the answer is not available to you.",
		"",
		"COMMITS",
		"Commit on this branch. Say in the message what was broken and why the",
		"change fixes it; 'fix CI' tells a reviewer nothing they did not know.",
		"Do not push, merge, or open a pull request: Orion does that.",
	}, "\n")
}

// TicketPrompt is the instruction for implementing one tracker issue.
//
// Every clause here is load-bearing, because this text is what decides how
// the agent spends money and what it does to the repository.
//
// The scope rule comes first and is absolute. An agent given a ticket and a
// codebase will find other things worth fixing; a pull request that also
// tidies three unrelated files is one a reviewer cannot approve without
// reading all of it, which destroys the reason for slicing work at all.
//
// The stop-and-ask rule matters more than it looks. An agent that hits a
// genuine ambiguity has three options: guess, loop, or stop. Guessing is the
// expensive one -- it produces a confident, wrong implementation that passes
// its own tests, and the cost is discovered in review or production. Stopping
// costs one run. So the prompt makes stopping the explicitly correct answer
// rather than an admission of failure.
//
// Tests are named as the acceptance evidence rather than "please test",
// because "I added tests" and "the tests I added would fail if the behaviour
// regressed" are different claims and only the second is worth anything.
func TicketPrompt(key, summary, description, url string, artifacts []string) string {
	var b strings.Builder

	b.WriteString(join(
		"Implement this tracker issue, and only this issue.",
		"",
		key+": "+summary,
		url,
		"",
	))
	b.WriteString("\n")
	if strings.TrimSpace(description) != "" {
		b.WriteString(join("The issue says:", quote(description), ""))
		b.WriteString("\n")
	}
	if len(artifacts) > 0 {
		b.WriteString(join(
			"Read these first. They are the agreed design and they outrank your own",
			"judgement about what this project should do:",
			"  "+strings.Join(artifacts, "\n  "),
			"",
		))
		b.WriteString("\n")
	}

	b.WriteString(join(
		"SCOPE",
		"Change only what this issue requires. If you notice an unrelated bug, a",
		"missing test elsewhere, or code you would write differently, leave it and",
		"say so at the end. A pull request that also fixes three other things is one",
		"a reviewer cannot approve without reading all of it, which defeats the point",
		"of a small slice.",
		"",
		"WHEN THE ANSWER IS NOT IN THE ARTIFACTS",
		"If something genuinely cannot be decided from the issue, the spec or the",
		"plan, STOP and write the question as the last thing you say. Do not guess.",
		"A guess produces a confident implementation that passes its own tests and is",
		"wrong in a way nobody catches until review or production; stopping costs one",
		"run and is the correct outcome, not a failure.",
		"Ask only when the answer changes what you build. Anything you can derive",
		"from the artifacts is not a question.",
		"",
		"EVIDENCE",
		"Add or extend tests that would FAIL if this behaviour regressed. 'I added",
		"tests' and 'these tests prove the change' are different claims and only the",
		"second is worth committing. Run the full suite before you finish; a green",
		"suite you did not run is not evidence.",
		"",
		"COMMITS",
		"Commit as you go, in small steps, on the branch you are already on. Do not",
		"create branches, do not merge, do not push, do not open a pull request:",
		"Orion does that after you exit, and doing it yourself makes the run",
		"unreviewable.",
		"",
		"Write the commit message so someone who has not read this ticket understands",
		"why the change exists, not merely what changed. The diff already says what.",
	))
	return b.String()
}
