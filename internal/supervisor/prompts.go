package supervisor

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/orion-sdlc/orion/internal/changelog"
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
			"",
			// Routing reads a marker off the created ticket. Nothing that
			// created a ticket knew the vocabulary existed, so the metadata
			// was set by luck and in practice never -- every ticket defaulted
			// to the implementer while the log correctly announced the
			// default on every run (OR-191).
			//
			// The vocabulary is NOT restated here. `orion routes` prints it,
			// and a prompt carrying its own copy is a second copy that drifts
			// from the table the moment either changes. Orion owns the
			// contract; the skill applies it (CLAUDE.md's precedence rule).
			"Run `orion routes` before you write the tree, and set the marker it names on",
			"every item you create -- as the issue type, a component, or a label. A ticket",
			"with no marker goes to the backend developer, which is correct for backend work",
			"and wrong for everything else. Routing reads what you write here and infers",
			"nothing from the summary, so an unmarked docs ticket is worked by the wrong",
			"actor and nothing anywhere reports a mistake.",
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

// NoopMarker is the sentence an agent writes when it finds this issue's work
// already done. It lives here, next to the prompt that asks for it, so the
// instruction and the thing that reads it cannot drift apart.
//
// A sentinel rather than a reading of the prose: "no change was needed" and
// "I could not work out what to change" are a few words apart in English and
// opposite in meaning, and a run that reports one as the other is the whole
// defect this exists to fix.
const NoopMarker = "NOTHING TO DO"

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
//
// ROOT CAUSE FIRST requires a stated root cause before any patch, because a
// patch derived from the symptom in the log often just addresses the
// symptom -- that is what burns a fix attempt, and the ceiling is three
// (ci.max_fix_attempts), so a wasted attempt is a third of the budget. The
// root cause is asked for as the first line of the closing message on
// purpose: OR-129 already surfaces that message in the console, so the
// diagnosis becomes visible there instead of buried in a log file nobody
// opens.
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
		"ROOT CAUSE FIRST",
		"Reproduce it locally first -- run ./scripts/test.sh if it exists, or the",
		"project's own suite. A fix for a failure you have not seen is a guess.",
		"Before you write the patch, state the root cause in one sentence: not",
		"what the log shows, but why the code produces it. A patch aimed at the",
		"symptom the log names, rather than the reason for it, is the single",
		"most common way a fix attempt gets spent without moving the ticket",
		"forward.",
		"",
		"WHAT TO DO",
		"Make the smallest change that fixes the root cause you stated, not",
		"the symptom.",
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
		"Commit on this branch. Say in the message what the root cause was and",
		"why the change fixes it; 'fix CI' tells a reviewer nothing they did not",
		"know. Lead your closing message with the root cause, in one sentence,",
		"before anything else -- that line is what carries into the console.",
		"Do not push, merge, or open a pull request: Orion does that.",
	}, "\n")
}

// LogTriagePrompt asks a subagent to reduce a raw CI log to what broke and
// why, before that log ever reaches the fix run.
//
// A failing job's log can run to thousands of lines, and embedded straight
// into FixPrompt it rides along on every turn the fix run takes -- read once
// by a human, paid for again on every turn by the model. This subagent gets
// its own context, reads the log once, and reports a few lines; the fix run
// never carries the log itself, only the answer (OR-143).
//
// Deliberately narrow. This agent answers one question and changes nothing,
// so none of TicketPrompt's or FixPrompt's machinery -- scope, commits, the
// noop marker -- applies here.
func LogTriagePrompt(branch, log string) string {
	return join(
		"A CI job failed on "+branch+". Read the log below and report what broke and why.",
		"",
		"THE LOG",
		quote(log),
		"",
		"Report:",
		"- which check failed",
		"- the specific error, with file:line where the log names one",
		"- your best read of the root cause, in a sentence or two",
		"",
		"Read only. Do not edit, run, or commit anything -- a different agent fixes",
		"the branch next, using what you report here.",
		"Keep the report short: a few lines, not a retelling of the log.",
	)
}

// ExplorePathsPrefix opens the last line of every explore answer: the files
// the answer was read out of, comma-separated, or "none".
//
// A literal marker rather than a reading of the prose, because the difference
// it encodes is the one the caller has to act on. An answer with a path can be
// opened and checked; an answer without one is the subagent's word for it, and
// telling those two apart has to be mechanical or it will not happen (OR-183).
const ExplorePathsPrefix = "PATHS:"

// ExploreNotFound is how the subagent says the repository genuinely does not
// contain the thing asked about, as distinct from a search that established
// nothing. Part of the answer's contract, so the caller can tell them apart.
const ExploreNotFound = "NOT FOUND"

// ExplorePrompt asks a subagent one narrow question about the repository and
// nothing else.
//
// The reading needed to answer "where is this defined" or "does this pattern
// already exist" is unbounded, and every file opened on the way stays in the
// asking run's context for the rest of the run -- paid for again on every
// turn. The answer is one line. So the reading happens in a context that is
// thrown away and only the answer crosses back (OR-183).
//
// Two clauses here are load-bearing and neither is obvious.
//
// READ ONLY, said in the prompt rather than enforced by a sandbox, because
// this subagent shares a worktree with a run that is mid-change. There is no
// separate checkout to isolate it into; a write from here lands inside
// somebody else's diff.
//
// The not-found distinction is the risk this pattern carries that log triage
// does not. A subagent that under-reports loses information silently, and
// "the repository does not contain this" and "I did not find it" read
// identically while meaning opposite things. Acting on the first when the
// second is true means designing around an absence that is not real -- a
// wrong architectural decision, not merely a worse fix attempt.
func ExplorePrompt(question string) string {
	return join(
		"Answer this one question about the repository you are in, and nothing else:",
		quote(question),
		"",
		"READ ONLY",
		"Do not edit, create, delete, run or commit anything. Another agent is",
		"working in this same worktree right now, and a write from you would land",
		"in the middle of its change.",
		"",
		"ANSWER",
		"A few lines. No preamble, no account of how you searched.",
		"",
		"SAY WHICH OF THE TWO IT IS",
		"If you searched and the repository genuinely does not contain it, begin",
		"with "+ExploreNotFound+" and say what you searched for.",
		"If you looked and cannot tell, or ran out of turns, say exactly that --",
		"do NOT report it as absent. \"It is not there\" and \"I did not find it\"",
		"read the same and mean opposite things, and whoever asked will act on",
		"the first by building around an absence that does not exist.",
		"",
		"CITE",
		"Make the LAST line exactly:",
		"  "+ExplorePathsPrefix+" <the file paths the answer came from, comma-separated>",
		"Write "+ExplorePathsPrefix+" none when it came from no file. A path is what",
		"makes the answer checkable by whoever asked; without one it is only your",
		"word for it, and it will be treated as unproven.",
	)
}

// FanChildPrompt instructs one subagent working one Go package while its
// peers work others in the same worktree (OR-230).
//
// The ownership clause is first and is absolute, because it is the only thing
// standing between this and the failure the design exists to avoid. Orion's
// validator has already established that no two assigned packages import one
// another; that guarantee is worth nothing if a child edits outside its own.
//
// The no-commands clause is stated even though it is enforced by the tool
// list, and the reason is stated with it. An agent that discovers it cannot
// run the suite and is not told why concludes the environment is broken and
// spends its turns working around it -- which is how a restriction meant to
// save a suite run costs several.
func FanChildPrompt(pkg, task string) string {
	return join(
		"You are one of several subagents changing this repository at the same",
		"time. Each of us owns exactly one Go package.",
		"",
		"YOU OWN: "+pkg,
		"Edit files in that package and nowhere else -- not a caller, not a test",
		"in another package, not a shared helper. Another subagent owns every",
		"other package being changed right now, and an edit outside yours will",
		"either overwrite their work or be overwritten by it, silently, with no",
		"conflict marker to warn anyone.",
		"",
		"WHAT TO CHANGE",
		quote(task),
		"",
		"YOU HAVE NO SHELL",
		"You cannot build, test, lint, or commit, and this is deliberate rather",
		"than a fault to work around: the tree you are in is being written by",
		"your peers right now, so a suite run here would report failures that",
		"are not yours. The parent run builds and tests ONCE, after every",
		"subagent has landed.",
		"",
		"WHEN YOU FINISH",
		"Say what you changed, file by file, and name anything you could not do",
		"or that needs a change outside your package. The parent acts on that",
		"list; anything you leave out of it is lost.",
	)
}

// fanOffer tells the implementer that independent packages can be worked
// concurrently, and that Orion decides whether they are independent.
//
// The split matters more than the speedup. An agent asked to judge its own
// fan width judges it optimistically, and a wrong guess here does not fail --
// it corrupts a tree quietly and is discovered at merge. So the agent
// proposes and a deterministic check disposes, in the same shape as the
// plan-before-edit gate.
//
// Conditional on there being a go.mod, for the same reason testEnv's lines
// are conditional: naming a Go-only mechanism in a repository that has no Go
// in it teaches the agent to distrust the instruction and go exploring, which
// costs more than saying nothing.
func fanOffer(repoPath string) string {
	if repoPath == "" {
		return ""
	}
	if _, err := os.Stat(filepath.Join(repoPath, "go.mod")); err != nil {
		return ""
	}
	return "\n\n" + join(
		"WORKING SEVERAL PACKAGES AT ONCE",
		"When this change touches Go packages that do not import one another, you",
		"can have them written concurrently instead of working them in file order.",
		"Write the assignment to a file -- one package each, and the task in full,",
		"because the subagent is given that text and nothing else:",
		`  {"assignments": [{"package": "./internal/a", "task": "..."},`,
		`                   {"package": "./internal/b", "task": "..."}]}`,
		"then run: orion fan <that file>",
		"Orion checks it -- no package assigned twice, the width within this",
		"project's limit, and no import edge between the packages assigned, from",
		"go list. Any failure and it tells you to work serially. That is not a",
		"negotiation and there is no better argument to make; just do the work.",
		"The subagents can only read and edit. They cannot run anything, so",
		"nothing is built or tested until they have all landed and YOU run the",
		"suite once, yourself. At most two rounds of fixing what it reports, then",
		"stop and say what is still red.",
	)
}

// AIOpsNonePrefix is how the triage subagent says the leftover events are
// all explainable and nothing should be filed.
//
// A literal marker rather than a reading of the prose, for the same reason
// ExploreNotFound is one: "nothing here is worth reporting" is the answer
// this agent should give most nights, and an answer that has to be inferred
// from prose is one a parser will eventually read as a proposal.
const AIOpsNonePrefix = "NOTHING TO REPORT"

// AIOpsProposePrefix opens each proposed finding, one per line.
const AIOpsProposePrefix = "PROPOSE:"

// AIOpsPrompt asks a subagent whether any leftover event in a finished run is
// worth filing a ticket about.
//
// It is handed ONLY the concerning events that no rule recognised. The rules
// are pure functions over typed events -- they cannot hallucinate and they
// cost nothing -- so everything they can already explain is settled before
// this agent is started, and what is left is the far smaller and more honest
// question of whether an unrecognised pattern matters (OR-168).
//
// Two clauses carry the whole prompt.
//
// SAYING NOTHING IS THE EXPECTED ANSWER. Orion degrades on purpose in many
// places: a lock timeout proceeds unlocked, a QA failure is a warning, an
// absent optional tool is fine. An agent that reads "blocked" or "failed" and
// proposes a ticket is filing against behaviour that is working correctly,
// and the backlog is already hard to scan. So the default is stated as the
// default, not as a permitted exception.
//
// IT PROPOSES; IT DOES NOT FILE. Said here as well as enforced by the type
// the caller uses, because an agent told it may create tickets will look for
// a way to, and the tracker credentials are in the environment it runs in.
func AIOpsPrompt(key, lines string) string {
	return join(
		"A run working "+key+" has FINISHED. Below are the events from its log that",
		"went wrong and that Orion's own rules did not already recognise.",
		"",
		"THE UNRECOGNISED EVENTS",
		quote(lines),
		"",
		"ONE QUESTION: is any of this worth a person filing a ticket about?",
		"",
		"ALMOST ALWAYS THE ANSWER IS NO, AND THAT IS THE RIGHT ANSWER",
		"Orion degrades on purpose. A lock timeout proceeds unlocked and says so.",
		"A QA failure is a warning, not a block. A missing optional tool is a",
		"supported configuration. A run that found the work already present and",
		"changed nothing is a correct outcome. None of those is a defect, and a",
		"ticket filed against one teaches everybody that these tickets mean",
		"nothing. Propose something only when you can say what is BROKEN, not",
		"merely what looks alarming.",
		"",
		"DO NOT CREATE ANYTHING",
		"Do not open a ticket, comment on one, or run any command that would.",
		"A person decides what gets created. You are writing a suggestion.",
		"",
		"ANSWER",
		"If nothing is worth reporting, reply with exactly:",
		"  "+AIOpsNonePrefix,
		"Otherwise write one line per finding, and nothing else:",
		"  "+AIOpsProposePrefix+" <one-line title> | <why this is broken rather than",
		"  degrading on purpose, in a sentence>",
		"At most three. If you have more than three, you are pattern-matching on",
		"the word \"failed\" rather than judging, and none of them will be read.",
	)
}

// DonePrompt asks a subagent the ONE question about a finished, green run
// that no rule in internal/done expresses: does this diff do what the ticket
// asked for?
//
// Everything mechanical is settled before this runs -- whether QA reached a
// verdict, whether a test is stranded in a worktree, whether the new tests
// survive -count=2 are each a pure function over evidence that already
// exists, and all three of the 2026-08-30 cases this pass was built from are
// caught by them. What is left is the part that is not expressible as a rule,
// and the reason a model is here at all.
//
// Three clauses carry it.
//
// THE DIFF IS THE ONLY EVIDENCE. Not the ticket's status, not the branch
// name, not a commit message claiming the work is done. The failure this pass
// exists to catch is precisely a run that SAYS it is finished, so the agent
// is told which of the two to believe.
//
// DONE IS THE EXPECTED ANSWER. Most changes are what they say they are, and
// an agent that hands work back on a hunch produces a verdict people learn to
// wave through -- at which point the pass is worse than not running. So the
// bar is a criterion it can NAME and cannot find, not a feeling of
// incompleteness.
//
// MISSING EVIDENCE IS NOT MISSING WORK. A truncated diff is the ordinary case
// for a large change, and "I could not see it" and "it is not there" are
// different claims. Only the second is a hand-back.
func DonePrompt(key, summary, criteria, stat, patch string, truncated bool) string {
	lines := []string{
		"A run working " + key + " has finished and its checks are green. Before a",
		"person is asked to approve the merge, answer one question about it.",
		"",
		"THE TICKET",
		quote(summary),
		"",
		"WHAT IT ASKED FOR",
		quote(criteria),
		"",
		"WHAT THE BRANCH ACTUALLY CARRIES",
		quote(stat),
		"",
		"THE DIFF",
		quote(patch),
	}
	if truncated {
		lines = append(lines, "",
			"THIS DIFF IS TRUNCATED. Parts of it are not shown to you. A criterion",
			"you cannot find may simply be in the part that was cut.")
	}
	lines = append(lines,
		"",
		"THE QUESTION: does each thing the ticket asked for correspond to",
		"something in this diff?",
		"",
		"JUDGE THE DIFF, NOT THE CLAIM",
		"A green check says the build compiles and the existing tests pass. A",
		"commit message says what somebody meant to do. Neither is evidence that",
		"the ticket was implemented, and a run that only LOOKS done is exactly",
		"what you are here to catch.",
		"",
		"DONE IS THE EXPECTED ANSWER, AND USUALLY THE RIGHT ONE",
		"Most changes are what they say they are. Say NOT DONE only when you can",
		"NAME a specific thing the ticket asked for and point at its absence from",
		"the diff. Not because the change looks small, not because you would have",
		"written it differently, not because you would like more tests, and not",
		"because something adjacent could also be improved. A hand-back on a",
		"hunch costs a person a ticket and teaches everybody that this verdict",
		"means nothing.",
		"",
		"IF YOU CANNOT SEE IT, THAT IS NOT THE SAME AS IT NOT BEING THERE",
		"An unreadable or truncated diff is missing evidence, not missing work.",
		"Answer "+doneReplyDone+" and let the checks that DO have evidence stand.",
		"",
		"DO NOT CHANGE ANYTHING",
		"Do not edit a file, commit, merge, approve, comment on the ticket, or run",
		"any command that would. You are reporting a verdict; Orion acts on it.",
		"",
		"ANSWER WITH ONE LINE AND NOTHING ELSE",
		"  "+doneReplyDone,
		"or",
		"  "+doneReplyNotDone+" <the criterion the ticket asked for, and what the diff",
		"  does not contain, in one sentence>",
	)
	return join(lines...)
}

// The reply contract for DonePrompt. Stated here rather than imported from
// internal/done, so this package -- which every stage in Orion runs through --
// takes no dependency on the one that parses its output. The coupling is real
// either way, so it is pinned by a test that asserts the prompt states exactly
// the markers internal/done reads.
const (
	doneReplyDone    = "DONE"
	doneReplyNotDone = "NOT DONE:"
)

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
// Child is one sub-task of the issue being worked, in the order a person
// ranked it.
type Child struct {
	Key         string
	Summary     string
	Description string
}

func TicketPrompt(key, summary, description, url, repoPath string, artifacts []string) string {
	return TicketPromptWithChildren(key, summary, description, url, repoPath, artifacts, nil)
}

// TicketPromptWithChildren is TicketPrompt plus the issue's sub-tasks.
//
// The children are a CHECKLIST inside one piece of work, not separate jobs.
// Orion is otherwise flat -- the queue is a label search and the unit of work
// is whatever carries the label -- so a Story decomposed into Tasks used to
// be either invisible (label the Story, the agent never learns the Tasks
// exist) or a hazard (label each Task, and two Tasks touching one file
// collide on separate branches, which is exactly how two tickets both came
// to create src/fcia/cli.py from scratch).
//
// In order, and said so explicitly: a Story's Tasks are usually sequenced by
// dependency, and an agent told to "do these" without being told the order
// will pick its own.
func TicketPromptWithChildren(key, summary, description, url, repoPath string,
	artifacts []string, children []Child) string {

	var b strings.Builder

	opening := "Implement this tracker issue, and only this issue."
	if len(children) > 0 {
		opening = "Implement this tracker issue and all of its sub-tasks, and nothing else."
	}
	b.WriteString(join(
		opening,
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
	if len(children) > 0 {
		b.WriteString(childList(children))
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
		"WHEN THE WORK IS ALREADY THERE",
		"If you find this issue's change already present in the repository, stop and",
		"say so. Do not manufacture a diff to justify the run, and do not widen the",
		"issue until it has something left to do.",
		"Make the LAST line of your closing message exactly:",
		"  "+NoopMarker+": <one line naming your evidence -- the commit, the file, the test>",
		"That line is how Orion tells 'there was nothing to do' from 'I could not do",
		"it'. Without it an idempotent run is recorded as a failure. Write it only",
		"when you are confident, and ask instead when you are not.",
		"",
		"EVIDENCE",
		"Add or extend tests that would FAIL if this behaviour regressed. 'I added",
		"tests' and 'these tests prove the change' are different claims and only the",
		"second is worth committing. Run the tests for the PACKAGES YOU TOUCHED",
		"before you finish; a green test you did not run is not evidence.",
		"",
		"Do NOT run the whole suite. CI runs it on three platforms for every push,",
		"which is what regression detection is for, and it costs nothing there.",
		"Running it here costs model time on the critical path and holds a job",
		"slot: on OR-135 it was run four times and spent 37 minutes, most of it",
		"waiting on packages the change never touched (OR-266).",
	))
	b.WriteString(testEnv(repoPath))
	b.WriteString(waitingForALongCommand())
	b.WriteString(exploreOffer())
	b.WriteString(fanOffer(repoPath))
	b.WriteString(changelogFragment(repoPath, key))
	b.WriteString(join(
		"",
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

// changelogFragment tells the implementer to write a fragment rather than to
// edit CHANGELOG.md.
//
// Every ticket writes a changelog entry and every entry went into the same
// section of the same file, so any two branches in flight conflicted there
// whatever code they touched. Three tickets once partitioned the code across
// three packages cleanly and still blocked each other on CHANGELOG.md alone.
// A file per ticket cannot collide; `orion changelog --version vX.Y.Z`
// collates them at release.
//
// Conditional on the directory existing, for the same reason testEnv's lines
// are: naming a mechanism a repository does not have teaches the agent to
// distrust the instruction and go exploring, which costs more than saying
// nothing. `orion init` creates it, so an adopted repository has one.
func changelogFragment(repoPath, key string) string {
	if repoPath == "" || key == "" {
		return ""
	}
	if fi, err := os.Stat(filepath.Join(repoPath, changelog.Dir)); err != nil || !fi.IsDir() {
		return ""
	}
	return join(
		"",
		"",
		"CHANGELOG",
		"Do not edit CHANGELOG.md. Write "+changelog.Dir+"/"+key+".md instead:",
		"",
		"  ### Added",
		"  - What a reader deciding whether to upgrade needs to know.",
		"",
		"Sections: "+strings.Join(changelog.Sections, ", ")+". Any other name fails at",
		"collation. Skip the fragment entirely when the change is invisible to a user",
		"of this repository -- a refactor, a test, internal tooling.",
		"One file per ticket is the point: two tickets editing CHANGELOG.md conflict",
		"every time regardless of what else they change.",
	)
}

// exploreOffer tells the implementer that questions about unfamiliar code can
// be asked instead of read for, and asked ALL AT ONCE.
//
// This clears the bar testEnv sets for a fixed prefix -- a line here is
// re-sent on every turn for the life of the ticket -- because what it
// replaces is the most repeated unbounded cost in a run. Finding where one
// thing is defined can take a dozen greps and reads, and every file opened on
// the way stays in context afterwards, re-sent every turn, to have said one
// sentence once.
//
// Asked as a PHASE rather than one at a time is what OR-229 adds, and the
// wording is the mechanism. An agent told it may ask "one narrow question"
// asks at most one, then greps for the rest: a question at a time serialises
// the run behind subagents it is not doing anything while it waits for, so
// reading for itself is genuinely the faster move and it takes it. Asked
// together they run concurrently under supervisor.Fan and the run waits once
// -- at which point the cheap path is also the fast one, which is the only
// version of this an agent actually follows.
//
// The fallback is stated in the same breath as the command, because a
// subagent that fails and leaves the implementer waiting for permission to
// read a file itself would cost more than it ever saved.
func exploreOffer() string {
	return join(
		"",
		"",
		"FINDING YOUR WAY AROUND THIS REPOSITORY",
		"Start by working out what you do NOT know -- where a thing is defined,",
		"whether a pattern already exists, what a config actually holds -- and ask",
		"all of it in ONE call, before you start reading:",
		"  orion explore \"<question>\" \"<question>\" \"<question>\"",
		"Each question is answered in a subagent's own context and the answers come",
		"back citing the files they were read out of, so what those subagents read",
		"never enters your context. They run concurrently, so asking four costs you",
		"about what asking one costs -- ask them together rather than one at a time.",
		"Batch any later questions the same way.",
		"Your own greps and file reads are the expensive path: each one stays in",
		"your context and is re-sent on every turn for the rest of this ticket.",
		"An answer citing no file is unproven: confirm it before building on it. If",
		"the command fails, just read for yourself.",
	)
}

// testEnv names how THIS repository runs its tests, when it can be told
// without guessing.
//
// The entry point was documented nowhere the implementer could see it: the
// only mention of scripts/test.sh lived in the CI-fix prompt, so an agent
// starting a ticket rediscovered it by running cat on a hunch and then found
// out how to make it work by trial and error -- seventeen shell calls on one
// ticket, paid again from zero on the next, because nothing carried forward.
//
// Both lines are conditional, and that is the point. A prompt that
// confidently names a command the repository does not have is worse than
// silence: the agent runs it, it fails, and now it distrusts the instruction
// and goes exploring anyway -- the cost this was meant to remove, plus a
// wasted turn. So the lines appear only when the things they name exist.
//
// Kept to two lines because this text is a fixed prefix, re-read on every
// turn of the run. A line here costs its tokens once per turn for the life of
// the ticket. These two earn it -- they replace turns of rediscovery -- but
// the next candidate has to clear the same bar, and most advice does not.
// Notably absent: the contents of scripts/test.sh. The agent can read the
// file; inlining it would go stale the first time somebody edited the script.
// waitingForALongCommand tells the agent HOW to wait, because the absence of
// an answer to that is what killed two finished tickets.
//
// OR-189 and OR-191 each backgrounded the suite -- nine minutes, before
// OR-202 cut it -- re-read its output file while it ran, and tripped the
// identical-repeat breaker with their work complete, green, and entirely
// uncommitted. Both agents diagnosed themselves correctly in BLOCKED.md and
// both were still lost: nothing had ever told them another way existed, so
// they reached for the only one they could see.
//
// The breaker no longer counts that poll (internal/hook/breaker.go), but the
// instruction is the half that matters here. A rule the agent cannot read is
// not a rule it can follow, and the fix has to be stated where the agent
// looks, not only enforced where it does not.
//
// Unconditional, unlike testEnv's lines. Every repository has some command
// slow enough to be worth waiting for, and this costs four lines to say.
func waitingForALongCommand() string {
	return "\n\n" + join(
		"WAITING FOR A LONG COMMAND",
		"Run the suite in the FOREGROUND and give the call a generous timeout. One",
		"Bash call that waits several minutes is ONE tool call, and waiting is free.",
		"Do NOT launch it in the background and then re-read its output file to see",
		"whether it has finished. Two tickets finished their work, had it green, and",
		"were lost exactly that way: from outside, polling an unchanging file and",
		"looping are the same action.",
		"If something must run in the background, ask the tool that reports on a",
		"background task, rather than re-reading its output file by hand.",
	)
}

func testEnv(repoPath string) string {
	if repoPath == "" {
		return ""
	}
	var lines []string
	if _, err := os.Stat(filepath.Join(repoPath, "scripts", "test.sh")); err == nil {
		lines = append(lines,
			"./scripts/test.sh runs the whole suite. It is what CI runs on every "+
				"push, so do NOT run it here -- run `go test ./internal/<pkg>/` "+
				"(or the equivalent) for what you changed. Read the script when "+
				"you need to know what CI will check.")
	}
	if py := venvPython(repoPath); py != "" {
		lines = append(lines,
			"The virtualenv is already built: "+py+" is the interpreter to use.")
	}
	if len(lines) == 0 {
		return ""
	}
	return "\n" + strings.Join(lines, "\n")
}

// venvPython resolves the interpreter the same way scripts/test.sh does: this
// directory's .venv, else the MAIN worktree's.
//
// The fallback is not decoration. Orion runs the agent in a git worktree,
// which shares history but not ignored files, so the virtualenv built once
// per sandbox at adoption time lives in the clone the worktree hangs off,
// never in the worktree itself. Resolving it the script's way is deliberate:
// two answers to "which python" is how the prompt and the suite end up
// disagreeing.
func venvPython(repoPath string) string {
	here := filepath.Join(repoPath, ".venv", "bin", "python")
	if _, err := os.Stat(here); err == nil {
		return here
	}
	out, err := exec.Command("git", "-C", repoPath, "rev-parse", "--git-common-dir").Output()
	if err != nil {
		return ""
	}
	common := strings.TrimSpace(string(out))
	if !filepath.IsAbs(common) {
		common = filepath.Join(repoPath, common)
	}
	main := filepath.Join(filepath.Dir(common), ".venv", "bin", "python")
	if _, err := os.Stat(main); err != nil {
		return ""
	}
	return main
}

// childList renders the sub-tasks as the ordered checklist they are.
//
// Numbered rather than bulleted, and the order stated in words as well as in
// the numbering, because the order is load-bearing: these are the steps of
// one change, not a set of independent asks. An agent that does step 3 first
// writes code against something that does not exist yet.
//
// Each child keeps its KEY. When the run reports what it did, the keys are
// what let a person match the report back to the tracker without guessing
// from summaries -- and they are what the closing pass needs.
func childList(children []Child) string {
	var b strings.Builder
	b.WriteString(join(
		"SUB-TASKS",
		"This issue is decomposed into the sub-tasks below.",
		"Do ALL of them, in this order -- they are the steps of one change, and a",
		"later one usually depends on an earlier one already being in place.",
		"",
		"They are one piece of work: one branch, one commit series, one pull",
		"request. Do not treat them as separate deliverables.",
		"",
	))
	b.WriteString("\n")
	for i, c := range children {
		b.WriteString(fmt.Sprintf("  %d. %s  %s\n", i+1, c.Key, c.Summary))
		if d := strings.TrimSpace(c.Description); d != "" {
			for _, line := range strings.Split(d, "\n") {
				b.WriteString("       " + line + "\n")
			}
		}
	}
	b.WriteString(join(
		"",
		"When you finish, say which sub-task keys you completed and which you did",
		"not, by key. A sub-task you could not do is not a failure of the run --",
		"it is a thing a person needs to know about, and saying nothing about it",
		"is the only wrong answer.",
		"",
	))
	b.WriteString("\n")
	return b.String()
}
