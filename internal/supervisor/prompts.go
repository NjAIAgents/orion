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

	case "build", "implement":
		return join(
			"Implement plans/"+ws.Task.Slug+".plan.md.",
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
			"Push the working branch, then open the PR with a body that links intent, spec and",
			"plan, summarizes what changed and why, and attaches the verification output.",
			"",
			"Never push to the default branch. If the push is refused, that refusal is correct;",
			"report it rather than working around it.",
		), nil

	default:
		return "", fmt.Errorf("unknown stage %q (want: intent, spec, plan, build, verify, review, pr)", stage)
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
