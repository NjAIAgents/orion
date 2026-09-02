package discovery

// The property under test is TERMINATION, and it is proved rather than
// observed: the adversarial ask below adds new questions every round and never
// answers one, which is exactly the run that used to have no end. A test that
// merely finished would prove nothing -- "it stopped this time" is what an
// unbounded loop also looks like on a lucky day -- so each of these asserts the
// COUNT: how many times ask was called, and that it was called no more than the
// ceiling allows.

import (
	"fmt"
	"os"
	"strings"
	"testing"
)

const intentWithOneOpen = `# Intent
Something.

## Open questions
- What is the retention period?
`

// forever adds n brand-new questions on every round and answers nothing. It
// fails the test rather than returning if it is called past the ceiling, so a
// regression that made the loop unbounded fails loudly instead of hanging.
func forever(t *testing.T, ceiling, n int, calls *int) Ask {
	t.Helper()
	return func(round int) []Added {
		*calls++
		if *calls > ceiling {
			t.Fatalf("ask was called %d times with a ceiling of %d: the loop is unbounded",
				*calls, ceiling)
		}
		var qs []string
		for i := 0; i < n; i++ {
			qs = append(qs, fmt.Sprintf("round %d question %d", round, i))
		}
		return []Added{{Agent: "analyst", Questions: qs}}
	}
}

func TestConvergeStopsAtTheCeilingAndEscalates(t *testing.T) {
	path := write(t, intentWithOneOpen)
	calls := 0
	c := Converge(path, 2, forever(t, 2, 2, &calls), nil)

	if calls != 2 {
		t.Errorf("ask ran %d times; the ceiling is 2", calls)
	}
	if len(c.Rounds) != 2 {
		t.Errorf("recorded %d rounds, want 2", len(c.Rounds))
	}
	if !c.Escalated {
		t.Error("the ceiling was reached with questions open; that must escalate")
	}
	// One from the file plus two per round: the count RISING is the failure
	// mode, and the ceiling is what ends it anyway.
	if c.Open != 5 {
		t.Errorf("Open = %d, want 5", c.Open)
	}
}

// The ceiling is the ceiling for every value of it, not just the default. Any
// N: exactly N rounds, never N+1.
func TestConvergeTerminatesForEveryCeiling(t *testing.T) {
	for _, ceiling := range []int{0, 1, 2, 3, 7} {
		path := write(t, intentWithOneOpen)
		calls := 0
		c := Converge(path, ceiling, forever(t, ceiling, 3, &calls), nil)
		if calls != ceiling {
			t.Errorf("ceiling %d: ask ran %d times", ceiling, calls)
		}
		if len(c.Rounds) != ceiling {
			t.Errorf("ceiling %d: recorded %d rounds", ceiling, len(c.Rounds))
		}
		if !c.Escalated {
			t.Errorf("ceiling %d: questions are still open, so it must escalate", ceiling)
		}
	}
}

// Escalating is not proceeding. The whole point of the ceiling is that it ends
// the spending, and the one thing it must never do is buy a silent guess.
func TestEscalatedIsNeverReadyToDesignFrom(t *testing.T) {
	path := write(t, intentWithOneOpen)
	calls := 0
	c := Converge(path, 2, forever(t, 2, 1, &calls), nil)
	if !c.Escalated {
		t.Fatal("precondition: this run must escalate")
	}
	if c.Ready() {
		t.Error("a run that escalated reported ready; the chain would design from an unanswered question")
	}
}

// The cheap path stays cheap: an agent with nothing to add on a file with
// nothing open costs one round, not the ceiling.
func TestConvergeStopsAsSoonAsNothingIsOpen(t *testing.T) {
	path := write(t, "# Intent\n\n## Open questions\n- None\n")
	calls := 0
	c := Converge(path, 5, func(int) []Added { calls++; return nil }, nil)

	if calls != 1 {
		t.Errorf("ask ran %d times; nothing was open after the first round", calls)
	}
	if c.Escalated {
		t.Error("nothing is open, so there is nobody to escalate to")
	}
	if !c.Ready() {
		t.Error("no open questions and a file that exists is ready")
	}
}

// A round that resolves what earlier rounds asked ends the loop early, from
// inside: the file is the state, so answering in it is what convergence is.
func TestConvergeStopsWhenTheRoundResolvesEverything(t *testing.T) {
	path := write(t, intentWithOneOpen)
	calls := 0
	c := Converge(path, 3, func(round int) []Added {
		calls++
		if round == 2 {
			if err := os.WriteFile(path,
				[]byte("# Intent\n\n## Open questions\n- [x] all answered\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			return nil
		}
		return []Added{{Agent: "architect", Questions: []string{"which tenant model?"}}}
	}, nil)

	if calls != 2 {
		t.Errorf("ask ran %d times; round 2 left nothing open", calls)
	}
	if c.Escalated || !c.Ready() {
		t.Errorf("round 2 answered everything: Escalated=%v Ready=%v", c.Escalated, c.Ready())
	}
}

// ONE place to answer. Four agents, one file, one `orion answer` -- the same
// person answering the same idea in four queues is the workflow this exists to
// avoid.
func TestEveryAgentsQuestionsLandInTheOneFile(t *testing.T) {
	path := write(t, "# Intent\nSomething.\n")
	c := Converge(path, 1, func(int) []Added {
		return []Added{
			{Agent: "analyst", Questions: []string{"who are the users?"}},
			{Agent: "architect", Questions: []string{"which tenant model?"}},
			{Agent: "security", Questions: []string{"what is the data classification?"}},
		}
	}, nil)

	body := readIntent(t, path)
	for _, q := range []string{"who are the users?", "which tenant model?", "what is the data classification?"} {
		if !strings.Contains(body, "- "+q) {
			t.Errorf("%q is not in the intent file, so `orion answer` cannot show it:\n%s", q, body)
		}
	}
	if c.Open != 3 {
		t.Errorf("Open = %d, want 3", c.Open)
	}
	// And the gate message -- what `orion answer` and the blocked stage both
	// print -- names all three.
	msg := c.GateMessage("OR-1")
	for _, q := range []string{"who are the users?", "which tenant model?", "what is the data classification?"} {
		if !strings.Contains(msg, q) {
			t.Errorf("the gate message omits %q:\n%s", q, msg)
		}
	}
}

// Two agents asking the same thing is one question for the human. Without
// this, a compounding round asks the same idea once per agent per round, which
// is the per-agent queue wearing a different hat.
func TestTheSameQuestionIsAskedOnce(t *testing.T) {
	path := write(t, intentWithOneOpen)
	c := Converge(path, 2, func(round int) []Added {
		return []Added{
			{Agent: "analyst", Questions: []string{"Which tenant model?"}},
			{Agent: "architect", Questions: []string{"which tenant model"}},
		}
	}, nil)

	if c.Open != 2 {
		t.Errorf("Open = %d, want 2 (the original plus one new question)", c.Open)
	}
	if n := strings.Count(strings.ToLower(readIntent(t, path)), "tenant model"); n != 1 {
		t.Errorf("the question is in the file %d times, want 1", n)
	}
	// Round two re-asked what round one already wrote, so it added nothing --
	// and the log says so rather than claiming a contribution.
	if got := c.Rounds[1].Line(); !strings.Contains(got, "nothing added") {
		t.Errorf("round 2 added no new question; its log line says %q", got)
	}
}

// A gate that moves without explanation cannot be debugged: each round says
// which agent added what, and what is left.
func TestEachRoundIsLoggedWithAgentsAndRemainingCount(t *testing.T) {
	path := write(t, intentWithOneOpen)
	var logged []Round
	Converge(path, 2, func(round int) []Added {
		return []Added{
			{Agent: "analyst", Questions: []string{fmt.Sprintf("q%d-a", round)}},
			{Agent: "security", Questions: []string{fmt.Sprintf("q%d-b", round), fmt.Sprintf("q%d-c", round)}},
		}
	}, func(r Round) { logged = append(logged, r) })

	if len(logged) != 2 {
		t.Fatalf("logged %d rounds, want one per round", len(logged))
	}
	line := logged[0].Line()
	for _, want := range []string{"round 1/2", "analyst +1", "security +2", "4 open"} {
		if !strings.Contains(line, want) {
			t.Errorf("round line %q omits %q", line, want)
		}
	}
	if got := logged[1].Open; got != 7 {
		t.Errorf("round 2 reported %d open, want 7", got)
	}
}

// The escalation names what it spent and hands over to the one answer path.
func TestEscalationMessageShowsTheRoundsAndHowToAnswer(t *testing.T) {
	path := write(t, intentWithOneOpen)
	calls := 0
	c := Converge(path, 2, forever(t, 2, 1, &calls), nil)

	msg := c.EscalationMessage("OR-152")
	for _, want := range []string{
		"did not converge in 2 round(s)",
		"discovery round 1/2",
		"discovery round 2/2",
		"What is the retention period?",
		"orion answer OR-152",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("the escalation omits %q:\n%s", want, msg)
		}
	}
}

// An intent file with no Open questions section yet, and a path with no file at
// all, both have to end up somewhere Assess and `orion answer` will find.
func TestQuestionsCreateTheSectionWhenThereIsNone(t *testing.T) {
	for _, tc := range []struct{ name, body string }{
		{"no section", "# Intent\nSomething.\n"},
		{"empty file", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := write(t, tc.body)
			c := Converge(path, 1, func(int) []Added {
				return []Added{{Agent: "analyst", Questions: []string{"who are the users?"}}}
			}, nil)
			if c.Open != 1 {
				t.Fatalf("Open = %d, want 1:\n%s", c.Open, readIntent(t, path))
			}
			if !strings.Contains(readIntent(t, path), "## Open questions") {
				t.Errorf("no Open questions section was created:\n%s", readIntent(t, path))
			}
		})
	}
}

// Questions are appended to the existing section rather than to the end of the
// file: a question written under a later heading is one Assess never counts,
// which is a gate that silently lets an unanswered question through.
func TestQuestionsAreAppendedInsideTheSection(t *testing.T) {
	path := write(t, `# Intent

## Open questions
- What is the retention period?

## Notes
- unrelated bullet
`)
	c := Converge(path, 1, func(int) []Added {
		return []Added{{Agent: "analyst", Questions: []string{"who are the users?"}}}
	}, nil)
	if c.Open != 2 {
		t.Fatalf("Open = %d, want 2:\n%s", c.Open, readIntent(t, path))
	}
	body := readIntent(t, path)
	if strings.Index(body, "who are the users?") > strings.Index(body, "## Notes") {
		t.Errorf("the question was written outside the section:\n%s", body)
	}
	if strings.Count(body, "unrelated bullet") != 1 {
		t.Errorf("the rest of the file was not left alone:\n%s", body)
	}
}

func readIntent(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
