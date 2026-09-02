package discovery

// Cases assigned under OR-152: the round log itself -- number and ceiling,
// per-agent counts, the open count after the round, an agent that added
// nothing showing nothing, and the callback firing as each round completes
// rather than being batched until Converge returns.

import (
	"fmt"
	"strings"
	"testing"
)

// Every round's log line names its own number and the ceiling it is running
// against, for every round in a multi-round run -- not just the first or the
// last.
func TestRoundLogNamesItsNumberAndTheCeiling(t *testing.T) {
	path := write(t, intentWithOneOpen)
	var logged []Round
	Converge(path, 3, func(round int) []Added {
		return []Added{{Agent: "analyst", Questions: []string{fmt.Sprintf("q%d?", round)}}}
	}, func(r Round) { logged = append(logged, r) })

	if len(logged) != 3 {
		t.Fatalf("logged %d rounds, want 3", len(logged))
	}
	for i, want := range []string{"round 1/3", "round 2/3", "round 3/3"} {
		if !strings.Contains(logged[i].Line(), want) {
			t.Errorf("round %d line %q omits %q", i+1, logged[i].Line(), want)
		}
	}
}

// The round log names each contributing agent by name, with how many
// questions it added -- a count alone does not say who moved the gate.
func TestRoundLogNamesEachAgentWithItsCount(t *testing.T) {
	path := write(t, intentWithOneOpen)
	var logged []Round
	Converge(path, 1, func(int) []Added {
		return []Added{
			{Agent: "analyst", Questions: []string{"a1?", "a2?", "a3?"}},
			{Agent: "security", Questions: []string{"s1?"}},
			{Agent: "architect", Questions: []string{"r1?", "r2?"}},
		}
	}, func(r Round) { logged = append(logged, r) })

	line := logged[0].Line()
	for _, want := range []string{"analyst +3", "security +1", "architect +2"} {
		if !strings.Contains(line, want) {
			t.Errorf("round line %q omits %q", line, want)
		}
	}
}

// Each round's logged Open count reflects what is open AFTER that round's
// questions were written, so a caller reading the log mid-run sees the
// state that round actually left behind.
func TestRoundLogOpenCountReflectsStateAfterThatRoundWasProcessed(t *testing.T) {
	path := write(t, intentWithOneOpen) // one already open
	var logged []Round
	Converge(path, 2, func(round int) []Added {
		// two brand-new questions per round
		return []Added{{Agent: "analyst", Questions: []string{
			fmt.Sprintf("round %d q1?", round), fmt.Sprintf("round %d q2?", round),
		}}}
	}, func(r Round) { logged = append(logged, r) })

	if len(logged) != 2 {
		t.Fatalf("logged %d rounds, want 2", len(logged))
	}
	if logged[0].Open != 3 {
		t.Errorf("round 1 Open = %d, want 3 (1 original + 2 new)", logged[0].Open)
	}
	if logged[1].Open != 5 {
		t.Errorf("round 2 Open = %d, want 5 (3 from round 1 + 2 more)", logged[1].Open)
	}
}

// An agent that contributed zero questions in a round is invisible in that
// round's log line -- it did not move the gate, so it is not credited with
// having tried.
func TestAgentWithZeroQuestionsShowsNothingInTheRoundLog(t *testing.T) {
	path := write(t, intentWithOneOpen)
	var logged []Round
	Converge(path, 1, func(int) []Added {
		return []Added{
			{Agent: "analyst", Questions: []string{"who are the users?"}},
			{Agent: "security", Questions: nil},
		}
	}, func(r Round) { logged = append(logged, r) })

	line := logged[0].Line()
	if !strings.Contains(line, "analyst +1") {
		t.Errorf("round line %q omits the agent that did contribute", line)
	}
	if strings.Contains(line, "security") {
		t.Errorf("round line %q names an agent that added zero questions", line)
	}
}

// When EVERY agent in a round added zero questions, the line says so
// explicitly rather than reading as an empty, unexplained gap.
func TestRoundLogSaysNothingAddedWhenEveryAgentAddedZero(t *testing.T) {
	path := write(t, intentWithOneOpen)
	var logged []Round
	Converge(path, 1, func(int) []Added {
		return []Added{
			{Agent: "analyst", Questions: nil},
			{Agent: "security", Questions: []string{}},
		}
	}, func(r Round) { logged = append(logged, r) })

	if !strings.Contains(logged[0].Line(), "nothing added") {
		t.Errorf("round line %q should say nothing added", logged[0].Line())
	}
}

// onRound fires AS EACH ROUND COMPLETES, not batched until Converge returns:
// the ask for round N must see N-1 already-logged rounds, which a batched
// implementation (collecting Round values and calling onRound only after the
// loop ends) could never produce.
func TestOnRoundCallbackFiresAsEachRoundCompletesNotBatched(t *testing.T) {
	path := write(t, intentWithOneOpen)
	var logged []Round
	var loggedCountWhenAsked []int
	Converge(path, 3, func(round int) []Added {
		loggedCountWhenAsked = append(loggedCountWhenAsked, len(logged))
		return []Added{{Agent: "analyst", Questions: []string{fmt.Sprintf("q%d?", round)}}}
	}, func(r Round) { logged = append(logged, r) })

	if len(loggedCountWhenAsked) != 3 {
		t.Fatalf("ask was called %d times, want 3", len(loggedCountWhenAsked))
	}
	for i, gotLogged := range loggedCountWhenAsked {
		if gotLogged != i {
			t.Errorf("round %d: onRound had delivered %d prior rounds by the time ask ran, want %d "+
				"-- the callback looks batched instead of firing as each round completes",
				i+1, gotLogged, i)
		}
	}
	if len(logged) != 3 {
		t.Errorf("onRound was called %d times overall, want 3", len(logged))
	}
}
