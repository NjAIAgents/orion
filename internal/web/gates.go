package web

// Gates is the survey OR-274 asked for: every place Orion stops and waits for a
// PERSON, and which of the gate board's states each one is drawn as.
//
// cards.go leaves Card.Gate zero and says why -- an outcome judgement is not in
// the event stream, and each judgement is its own ticket. This file is the half
// of that judgement that can be settled by reading the tree rather than a log:
// WHAT THE GATES ARE. Filling Card.Gate from a run is a later ticket; it names
// the kinds below rather than inventing a second vocabulary, the same way every
// consumer of model.go names those fields.
//
// THE MOCKUP IS THE CLOSED SET. docs/design/web/02-gate-board.html draws four
// states and no others -- blocked, awaiting approval, question, plan
// confirmation -- so those four are what a gate may map to. A fifth state is a
// design change, not a mapping decision, and gates_test.go reads the mockup to
// make that stick.
//
// AN UNMAPPED GATE IS LISTED, NOT DROPPED. A gate that exists and is not on the
// board is the failure this whole story is against: the operator is waiting on
// something, the board says nothing is waiting on them, and the run sits.
// Silently omitting one is therefore worse than having no board at all, because
// the empty state ("Nothing else is waiting on you") is read as evidence. So a
// gate with no board state stays in this list with State empty and Why filled,
// and the test refuses a gate that is neither mapped nor explained.
//
// The two unmapped ones are unmapped for the same structural reason and it is
// worth stating once: the board's row is a TICKET, grouped under a project.
// Both budget and lessons are keyed on something else -- a spend window across
// every project, a lesson signature across every project -- so neither has a
// ticket to be drawn on. That is a real gap in the board, not a reason the gate
// does not matter, and it is written down here so the next surface (a header
// chip, an account-level strip) is designed deliberately rather than discovered
// during an outage.

// GateState is what the gate board draws a waiting ticket as. The zero value
// means the gate has no board representation yet.
type GateState string

// The four states, spelled exactly as the mockup spells them.
const (
	GateBlocked  GateState = "blocked"
	GateApproval GateState = "awaiting approval"
	GateQuestion GateState = "question"
	GatePlan     GateState = "plan confirmation"
)

// Gate is one human gate: what stops, where it is produced, what a person does
// about it, and which board state it becomes.
type Gate struct {
	// Kind is the stable identifier a later ticket writes into Card.Gate.
	Kind string
	// State is the board state, or empty when the board cannot draw this gate.
	State GateState
	// Source is the file that produces the gate, so the survey can be checked
	// against the tree rather than believed. A path and not a line: a line
	// number is wrong within a week and the test that pinned it starts failing
	// for edits that changed nothing about the gate.
	Source string
	// Clears is what the person does. The board's buttons are drawn from this,
	// and it is also the answer to "what do I type" when the board is not up.
	Clears string
	// Why is why there is no board state, and is filled only when State is
	// empty. A gap with no reason is indistinguishable from an oversight.
	Why string
}

// Gates enumerates every human gate found in the tree as of OR-274. The five
// the ticket named are all here; the rest were found beside them and are listed
// for the same reason the gaps are.
var Gates = []Gate{
	{
		// `orion answer` walks the intent's open questions and the spec's
		// [NEEDS CLARIFICATION] markers, and the plan stage will not run while
		// either is open. The mockup's question row is this gate: an agent
		// asked something, one step in, and nothing is being spent.
		Kind:   "open-questions",
		State:  GateQuestion,
		Source: "internal/discovery/discovery.go",
		Clears: "orion answer <id>",
	},
	{
		// A recommendation is not a decision (internal/decide). It sits in the
		// pending directory, outside every later stage's reading scope, until a
		// person confirms it -- which is precisely the mockup's plan
		// confirmation row: the plan is drafted, it changes something that
		// already exists, and implementation has not started.
		Kind:   "plan-confirmation",
		State:  GatePlan,
		Source: "internal/decide/decide.go",
		// One path and no second one, deliberately: internal/decide reuses the
		// merge-approval reaction rather than growing a rival allowlist.
		Clears: "react " + confirmAffordance + " on the Slack request",
	},
	{
		// The merge approval (OR-228): a request message, an allowlist, the
		// bot's own reactions excluded, and a rejection beating every approval.
		// Orion posts and then waits, which is the whole reason the board
		// exists -- the ticket is finished and nothing else will happen.
		Kind:   "merge-approval",
		State:  GateApproval,
		Source: "internal/collect/approval.go",
		Clears: "approve on the request message in Slack",
	},
	{
		// The agent stopped rather than guess. The ticket wears orion-failed
		// and leaves the queue, so nothing retries it until a person answers
		// and requeues -- the mockup's blocked row, down to the label.
		Kind:   "agent-blocked",
		State:  GateBlocked,
		Source: "internal/work/slackmsg.go",
		Clears: "answer, then remove orion-failed and add ORION",
	},
	{
		// The breaker trip. plans/BLOCKED.md is the account of it, written into
		// a worktree nobody browses, in a session nobody reads -- which is why
		// collect learned to surface it (OR-232) and why the board must too.
		// Blocked rather than a state of its own: to the operator this is the
		// same sentence as agent-blocked, arrived at by a different route.
		Kind:   "breaker-trip",
		State:  GateBlocked,
		Source: "internal/hook/breaker.go",
		Clears: "read plans/BLOCKED.md on the branch, fix, then orion reset",
	},
	{
		// An environment fault, held rather than blamed on the ticket (OR-212,
		// OR-214). One fault, several tickets: a board row per held key, all
		// carrying the same cause and the same fix, because the operator's
		// question is "which of my tickets is stuck" and the answer is all of
		// them.
		Kind:   "environment-hold",
		State:  GateBlocked,
		Source: "internal/work/hold.go",
		Clears: "fix the environment, then react " + confirmAffordance + " (Orion re-checks before releasing)",
	},
	{
		// A run that ended holding uncommitted work Orion could not commit
		// itself. Nothing is reverted and nothing retries: collect cannot
		// rebase the branch until the tree is clean, so the ticket sits behind
		// develop indefinitely with no failure anywhere to explain it.
		Kind:   "dirty-worktree",
		State:  GateBlocked,
		Source: "internal/work/residue.go",
		Clears: "orion settle <KEY>",
	},
	{
		// UNMAPPED. A crossed checkpoint stops EVERY run -- budget.Admit
		// refuses until it is acknowledged -- so this is the gate with the
		// widest blast radius and the one most costly to miss. It has no board
		// row because it has no ticket: the ledger is one spend window under
		// ORION_HOME, spanning every project the board groups by.
		Kind:   "budget-ack",
		Source: "internal/budget/admit.go",
		Clears: "orion budget ack <pct>",
		Why: "the board's row is a ticket under a project; a crossed checkpoint " +
			"belongs to the ORION_HOME spend window and stops every project at " +
			"once, so there is no ticket to draw it on",
	},
	{
		// UNMAPPED. A lesson seen twice is offered for a decision and waits
		// indefinitely for one; nothing stops meanwhile, which is why this is
		// the gate easiest to leave sitting for a month. Keyed on a signature
		// across projects, so like budget-ack it has no ticket.
		Kind:   "lessons-pending",
		Source: "internal/lessons/propose.go",
		Clears: "orion lessons approve|reject <signature>",
		Why: "a candidate is keyed on a lesson signature across projects, not on " +
			"a ticket, and nothing is stopped while it waits -- so it belongs on " +
			"an account-level surface rather than in the waiting-on-me rows",
	},
}

// confirmAffordance is the emoji Orion adds to its own question so a phone user
// can tap rather than type. Spelled here only so two Clears lines cannot drift
// apart; the mechanism itself is internal/decide's and internal/work/hold.go's.
const confirmAffordance = "✅"
