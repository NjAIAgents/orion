package main

import (
	"strings"
	"testing"

	"github.com/orion-sdlc/orion/internal/actors"
	"github.com/orion-sdlc/orion/internal/events"
	"github.com/orion-sdlc/orion/internal/supervisor"
)

// The point of OR-183 is that explore is CHEAP and separately attributed. An
// explore that inherits the asking run's opus has added a second expensive
// run and saved nothing, and one whose spend lands under another actor is
// invisible in the cost report -- which matters more here than for triage,
// because a run asks several of these.
func TestExploreOptionsRunAsItsOwnActorOnItsOwnModel(t *testing.T) {
	o := exploreOptions("OR-1", "where is the rate limiter defined?")

	if o.Actor != events.ActorExplore {
		t.Errorf("Actor = %q, want %q so its spend is its own row in the cost report",
			o.Actor, events.ActorExplore)
	}
	if o.Key != "OR-1" {
		t.Errorf("Key = %q, want the ticket so the cost lands on that ticket", o.Key)
	}
	if want := actors.Model(events.ActorExplore); o.Model != want {
		t.Errorf("Model = %q, want the roster's %q -- pinning it cheap is the whole cost win",
			o.Model, want)
	}
	if o.MaxMinutes != exploreMaxMinutes || o.MaxTurns != exploreMaxTurns {
		t.Errorf("bounds = %d min / %d turns, want the tight explore bounds %d/%d",
			o.MaxMinutes, o.MaxTurns, exploreMaxMinutes, exploreMaxTurns)
	}
	if !strings.Contains(o.Prompt, "where is the rate limiter defined?") {
		t.Errorf("prompt must carry the question it is answering, got: %q", o.Prompt)
	}
}

func TestParseExploreAnswerSplitsAnswerFromCitations(t *testing.T) {
	got := parseExploreAnswer("Defined in internal/supervisor/ratelimit.go.\n" +
		"PATHS: internal/supervisor/ratelimit.go, internal/config/config.go\n")

	if got.Answer != "Defined in internal/supervisor/ratelimit.go." {
		t.Errorf("Answer = %q, want the prose without the citation line", got.Answer)
	}
	want := []string{"internal/supervisor/ratelimit.go", "internal/config/config.go"}
	if len(got.Paths) != len(want) {
		t.Fatalf("Paths = %v, want %v", got.Paths, want)
	}
	for i := range want {
		if got.Paths[i] != want[i] {
			t.Errorf("Paths[%d] = %q, want %q", i, got.Paths[i], want[i])
		}
	}
}

// "none" is the instructed way to say there is no source, so it must land in
// the same place as a missing line: unsourced. Recording a file called none
// would make an uncheckable answer look checkable, which is the one mistake
// this parse must not make.
func TestParseExploreAnswerTreatsNoneAndAMissingLineAsUnsourced(t *testing.T) {
	for name, final := range map[string]string{
		"none":         "NOT FOUND: no retry helper exists.\nPATHS: none",
		"missing line": "NOT FOUND: no retry helper exists.",
	} {
		got := parseExploreAnswer(final)
		if len(got.Paths) != 0 {
			t.Errorf("%s: Paths = %v, want none", name, got.Paths)
		}
		if !strings.Contains(got.Answer, "no retry helper exists") {
			t.Errorf("%s: Answer = %q, want the answer kept whole -- a formatting miss "+
				"must not throw away work already paid for", name, got.Answer)
		}
	}
}

// The marker is read from the END. An answer that quotes its own instructions
// -- easily done when the question is about citations -- would otherwise have
// its prose read as the citation.
func TestParseExploreAnswerTakesTheLastPathsLine(t *testing.T) {
	got := parseExploreAnswer("I was told to end with PATHS: <files>.\nPATHS: real/file.go")

	if len(got.Paths) != 1 || got.Paths[0] != "real/file.go" {
		t.Errorf("Paths = %v, want only the final citation line", got.Paths)
	}
}

// An unsourced answer cannot be audited: nobody can open the file it came
// from and check it. The caller has to be TOLD that, because the two answers
// are otherwise indistinguishable prose, and for an architectural question
// acting on an unproven one is the expensive mistake (OR-183).
func TestPrintExploreWarnsWhenTheAnswerCitesNothing(t *testing.T) {
	var unsourced, sourced strings.Builder
	printExplore(&unsourced, exploreAnswer{Answer: "It uses a token bucket."})
	printExplore(&sourced, exploreAnswer{
		Answer: "It uses a token bucket.", Paths: []string{"internal/supervisor/ratelimit.go"}})

	if !strings.Contains(unsourced.String(), "unproven") {
		t.Errorf("an uncited answer printed as %q, with nothing marking it unproven",
			unsourced.String())
	}
	if strings.Contains(sourced.String(), "unproven") {
		t.Errorf("a cited answer was marked unproven: %q", sourced.String())
	}
	if !strings.Contains(sourced.String(), "internal/supervisor/ratelimit.go") {
		t.Errorf("the cited path never reached the caller: %q", sourced.String())
	}
}

// What a subagent returns is all anyone ever sees of it -- the context it
// read in no longer exists. So the answer AND the paths it came from have to
// be written down, or there is no way to ask later what the run was told and
// whether it was true.
func TestLogExploreRecordsTheAnswerAndItsPaths(t *testing.T) {
	ws := triageWS(t)

	logExplore(ws, "OR-1", "where is the rate limiter defined?", exploreAnswer{
		Answer: "internal/supervisor/ratelimit.go, a token bucket.",
		Paths:  []string{"internal/supervisor/ratelimit.go"},
	})

	evs, err := events.Read(events.Path(ws.Dir))
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, e := range evs {
		if e.Actor != events.ActorExplore {
			continue
		}
		found = true
		if e.Key != "OR-1" {
			t.Errorf("event key = %q, want the ticket", e.Key)
		}
		if !strings.Contains(e.Msg, "token bucket") {
			t.Errorf("event msg = %q, want the answer in it", e.Msg)
		}
		paths, _ := e.Detail["paths"].([]any)
		if len(paths) != 1 || paths[0] != "internal/supervisor/ratelimit.go" {
			t.Errorf("event detail paths = %v; a cited path is what makes the answer "+
				"checkable later, and one that is not recorded cannot be queried", e.Detail["paths"])
		}
		if e.Detail["question"] != "where is the rate limiter defined?" {
			t.Errorf("event detail question = %v, want the question the answer belongs to",
				e.Detail["question"])
		}
	}
	if !found {
		t.Error("nothing was written to the event log; the answer is gone the moment " +
			"the process exits, and with it any way to audit what the run was told")
	}
}

// The parser and the prompt have to agree on the marker, or every answer
// comes back unsourced and the citation requirement quietly does nothing.
func TestExploreParserAndPromptShareTheMarker(t *testing.T) {
	p := supervisor.ExplorePrompt("q")
	if !strings.Contains(p, supervisor.ExplorePathsPrefix) {
		t.Fatal("the prompt does not ask for the marker the parser reads")
	}
	got := parseExploreAnswer("answer\n" + supervisor.ExplorePathsPrefix + " a.go")
	if len(got.Paths) != 1 || got.Paths[0] != "a.go" {
		t.Errorf("the parser did not read the marker the prompt asks for: %v", got.Paths)
	}
}
