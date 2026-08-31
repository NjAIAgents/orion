package supervisor

import (
	"strings"
	"testing"
)

// The explore subagent shares a worktree with a run that is mid-change, and
// nothing but the prompt stops it writing there -- there is no second
// checkout to isolate it into (OR-183).
func TestExplorePromptCarriesTheQuestionAndForbidsWriting(t *testing.T) {
	p := ExplorePrompt("where is the rate limiter defined?")

	if !strings.Contains(p, "where is the rate limiter defined?") {
		t.Error("prompt is missing the question; explore cannot answer one it was not given")
	}
	lower := strings.ToLower(p)
	if !strings.Contains(lower, "read only") {
		t.Error("prompt must state that the run is read-only")
	}
	if !strings.Contains(lower, "do not edit") {
		t.Error("prompt must forbid editing outright: it shares a worktree with a writing run")
	}
	if !strings.Contains(lower, "worktree") {
		t.Error("prompt must say WHY it may not write -- another agent is in this tree right " +
			"now -- or the rule reads as boilerplate to argue with")
	}
}

// The risk that makes this different from log triage: a subagent that
// under-reports loses information silently, and the caller cannot tell "the
// repository does not contain this" from "I did not find it". For an
// architectural decision the first is a fact to design around and the second
// is a gap, so the prompt has to force the distinction.
func TestExplorePromptForcesTheNotFoundDistinction(t *testing.T) {
	p := ExplorePrompt("does a retry helper already exist?")

	if !strings.Contains(p, ExploreNotFound) {
		t.Errorf("prompt must name the %q marker, or absence and failure-to-find come back "+
			"phrased identically", ExploreNotFound)
	}
	lower := strings.ToLower(p)
	if !strings.Contains(lower, "cannot tell") && !strings.Contains(lower, "ran out of turns") {
		t.Error("prompt must give the subagent a way to say it could not establish an " +
			"answer; without one, the only phrasing available to it reads as absence")
	}
}

// A cited path is what makes an answer checkable later. The marker is parsed
// by the caller, so the prompt has to ask for that exact spelling and has to
// offer "none" -- an answer forced to invent a citation is worse than one
// that admits it has none.
func TestExplorePromptRequiresCitedPaths(t *testing.T) {
	p := ExplorePrompt("what is in orion.json?")

	if !strings.Contains(p, ExplorePathsPrefix) {
		t.Errorf("prompt must ask for the %q line the caller parses", ExplorePathsPrefix)
	}
	if !strings.Contains(p, ExplorePathsPrefix+" none") {
		t.Errorf("prompt must offer %q none, or an answer with no source will invent one",
			ExplorePathsPrefix)
	}
	if !strings.Contains(strings.ToLower(p), "last line") {
		t.Error("the paths line must be pinned to the END of the answer; anywhere else and " +
			"the caller cannot tell a citation from prose that mentions the marker")
	}
}

// The offer is a FIXED PREFIX on the ticket prompt, re-sent on every turn for
// the life of the run. It earns that only if it names the command exactly and
// states the fallback in the same breath -- an implementer left unsure
// whether to wait for a failed subagent costs more than the subagent saves.
func TestTicketPromptOffersExploreWithItsFallback(t *testing.T) {
	p := TicketPrompt("OR-1", "do the thing", "", "http://x/OR-1", "", nil)

	if !strings.Contains(p, `orion explore "<question>"`) {
		t.Error("the ticket prompt must name the explore command exactly; an agent that has " +
			"to guess the syntax will grep instead, which is the cost being removed")
	}
	lower := strings.ToLower(p)
	if !strings.Contains(lower, "read for yourself") {
		t.Error("the prompt must state the fallback: a failed explore must send the agent " +
			"back to reading, not leave it waiting")
	}
	if !strings.Contains(lower, "unproven") {
		t.Error("the prompt must say an uncited answer is unproven; a caller that builds on " +
			"one cannot audit it later")
	}
}
