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
			"You are capturing intent. The idea, in the originator's words:",
			quote(idea),
			"",
			"Interrogate it the way an analyst would: who is affected, what is out of scope,",
			"what constraints apply, what success looks like. Where the idea is ambiguous, list",
			"the question rather than inventing an answer.",
			"",
			"Then write intent/"+ws.Task.Slug+".intent.md with these sections:",
			"Problem, Proposed outcome, Affected users and systems, Constraints, Open questions.",
			"Commit it. Do not write code, do not design a solution.",
		), nil

	case "spec", "design":
		return join(
			"Read intent/"+ws.Task.Slug+".intent.md.",
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
			"Read intent/"+ws.Task.Slug+".intent.md and specs/"+ws.Task.Slug+".spec.md.",
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

	case "scaffold":
		return join(
			"Lay out the repository skeleton for this project.",
			"",
			"Use the /scaffold-project skill. It grounds the security and governance",
			"layer in the OpenSSF OSPS Baseline and delegates the stack layout to the",
			"ecosystem's own generator rather than inventing one.",
			"",
			"Read intent/"+ws.Task.Slug+".intent.md and specs/"+ws.Task.Slug+".spec.md first",
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
