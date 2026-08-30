package main

import (
	"strings"
	"testing"

	"github.com/orion-sdlc/orion/internal/actors"
	"github.com/orion-sdlc/orion/internal/events"
	"github.com/orion-sdlc/orion/internal/supervisor"
)

// Same argument as TestTriageOptionsRunAsItsOwnActorOnItsOwnModel (OR-143):
// a subagent that inherits the caller's model, or whose spend lands under
// another actor, has added a run and saved nothing. This one runs nightly
// over finished tickets, so an unattributed bill is one nobody can trace.
func TestAIOpsOptionsRunAsItsOwnActorOnItsOwnModel(t *testing.T) {
	o := aiopsOptions("OR-1", "10:00:01  blocked   ...")

	if o.Actor != events.ActorAIOps {
		t.Errorf("Actor = %q, want %q so its spend is its own row in the cost report",
			o.Actor, events.ActorAIOps)
	}
	if o.Key != "OR-1" {
		t.Errorf("Key = %q, want the ticket so the cost lands on that ticket", o.Key)
	}
	if want := actors.Model(events.ActorAIOps); o.Model != want {
		t.Errorf("Model = %q, want the roster's %q", o.Model, want)
	}
	if o.MaxTurns == 0 || o.MaxMinutes == 0 {
		t.Error("the triage subagent runs unbounded; a nightly pass with no ceiling " +
			"is the runaway spend this design is arranged to avoid")
	}
}

// The prompt has to say the two things that keep this pass honest, because
// the agent is the only part that can invent a ticket.
func TestAIOpsPromptSaysNothingIsTheExpectedAnswerAndForbidsFiling(t *testing.T) {
	p := supervisor.AIOpsPrompt("OR-1", "10:00:01  blocked   orion  something odd")

	if !strings.Contains(p, supervisor.AIOpsNonePrefix) {
		t.Errorf("the prompt never tells the agent how to say nothing is worth reporting, "+
			"which is the answer it should give most nights:\n%s", p)
	}
	if !strings.Contains(p, "degrades on purpose") {
		t.Error("the prompt does not warn that Orion degrades on purpose, so the agent " +
			"will propose tickets for behaviour that is working correctly")
	}
	if !strings.Contains(p, "DO NOT CREATE ANYTHING") {
		t.Error("the prompt does not forbid creating tickets; the tracker credentials " +
			"are in the environment this agent runs in")
	}
}

// parseAIOps is the one path in the pass that can turn model prose into a
// proposed ticket, so it is the one that has to be strict.
func TestParseAIOpsReadsOnlyMarkedLines(t *testing.T) {
	cases := []struct {
		name  string
		final string
		want  []string // titles, in order
	}{{
		name:  "the expected answer proposes nothing",
		final: supervisor.AIOpsNonePrefix,
	}, {
		name: "prose that discusses the marker is not a proposal",
		final: "I considered whether to write " + supervisor.AIOpsProposePrefix +
			" for the lock timeout, but that is Orion degrading on purpose.\n" +
			supervisor.AIOpsNonePrefix,
	}, {
		name: "a marked line is read, decoration and all",
		final: "Here is what I found:\n" +
			"- " + supervisor.AIOpsProposePrefix + " the worktree lock is never released | " +
			"the run exits with the lock still held, so the next run waits the full timeout\n",
		want: []string{"the worktree lock is never released"},
	}, {
		name: "several proposals are all read",
		final: supervisor.AIOpsProposePrefix + " first thing | because a\n" +
			supervisor.AIOpsProposePrefix + " second thing | because b\n",
		want: []string{"first thing", "second thing"},
	}, {
		name:  "a proposal with no title is dropped",
		final: supervisor.AIOpsProposePrefix + "  | just a reason",
	}}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := parseAIOps(c.final)
			if len(got) != len(c.want) {
				t.Fatalf("got %d proposal(s), want %d, from:\n%s", len(got), len(c.want), c.final)
			}
			for i, want := range c.want {
				if got[i].Title != want {
					t.Errorf("proposal %d title = %q, want %q", i, got[i].Title, want)
				}
				if !got[i].Novel {
					t.Errorf("proposal %d is not marked Novel; a rule cannot be wrong "+
						"about what it saw and this can, so the draft has to say which it is", i)
				}
			}
		})
	}
}

// A proposal that does not say why it is broken still gets a Why, and one
// that reads as a reason to distrust it. Silently blank would present the
// agent's least-supported output as though it were its best.
func TestAProposalWithNoReasonSaysSo(t *testing.T) {
	got := parseAIOps(supervisor.AIOpsProposePrefix + " something is wrong")
	if len(got) != 1 {
		t.Fatalf("got %d proposals, want 1", len(got))
	}
	if !strings.Contains(got[0].Why, "scepticism") {
		t.Errorf("Why = %q, want it to flag the missing justification", got[0].Why)
	}
}

// The signature is what stops a proposal being re-made every night once
// somebody files it, so it has to be stable and readable.
func TestNovelFindingsAreSignedStably(t *testing.T) {
	a := parseAIOps(supervisor.AIOpsProposePrefix + " The Worktree Lock Is Never Released | x")
	b := parseAIOps(supervisor.AIOpsProposePrefix + " the worktree lock is never released | y")

	if len(a) != 1 || len(b) != 1 {
		t.Fatal("both fixtures should parse to one proposal")
	}
	if a[0].Rule != b[0].Rule {
		t.Errorf("the same title cased differently signed as %q and %q, so filing it "+
			"once would not stop it being proposed again", a[0].Rule, b[0].Rule)
	}
	if !strings.HasPrefix(a[0].Rule, "novel-") {
		t.Errorf("signature = %q, want a novel- prefix so a reader can tell an agent's "+
			"proposal from a rule's finding at a glance", a[0].Rule)
	}
}
