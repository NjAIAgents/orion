package discovery

// Cases assigned under OR-152: the questions written to the intent file are
// well-formed regardless of what an agent hands in, the onRound callback's
// own contract (nil-safe, fires per round, fires even on the round that
// escalates), and the gate's core promise -- it never reports Ready while
// there is something unanswered or unresolved, and never hides a question
// from the message a person reads.

import (
	"os"
	"strings"
	"testing"
)

// A round's write always leaves the intent file ending in a newline, whether
// or not the caller's questions happened to include one -- a file editors and
// `orion answer` both expect to be well-formed text, not a stream that lucked
// into ending mid-line.
func TestQuestionsWrittenToFileMaintainTrailingNewline(t *testing.T) {
	path := write(t, "# Intent\nSomething.\n")
	Converge(path, 1, func(int) []Added {
		return []Added{{Agent: "analyst", Questions: []string{"who are the users?"}}}
	}, nil)

	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(string(b), "\n") {
		t.Errorf("intent file does not end in a newline:\n%q", string(b))
	}
}

// A question that is only whitespace is not a question -- writing it would
// give the human an empty bullet to puzzle over, and it would never resolve
// under any answer, so it must never reach the file.
func TestWhitespaceOnlyQuestionsAreRejected(t *testing.T) {
	path := write(t, intentWithOneOpen)
	c := Converge(path, 1, func(int) []Added {
		return []Added{{Agent: "analyst", Questions: []string{"   ", "\t", ""}}}
	}, nil)

	if c.Open != 1 {
		t.Errorf("Open = %d, want 1 (the whitespace-only questions must not count)", c.Open)
	}
	body := readIntent(t, path)
	if strings.Count(body, "- ") != 1 {
		t.Errorf("a blank bullet was written for a whitespace-only question:\n%s", body)
	}
}

// onRound fires once per round that actually ran, after that round's
// questions were written and the file reassessed -- so the Round it receives
// reflects the state the write just produced, not a stale count taken before
// the write.
func TestOnRoundCallbackFiresAfterQuestionsAreWrittenAndAssessed(t *testing.T) {
	path := write(t, intentWithOneOpen)
	var seenOpen []int
	Converge(path, 1, func(int) []Added {
		return []Added{{Agent: "analyst", Questions: []string{"who are the users?"}}}
	}, func(r Round) { seenOpen = append(seenOpen, r.Open) })

	if len(seenOpen) != 1 {
		t.Fatalf("onRound was called %d times, want 1", len(seenOpen))
	}
	if seenOpen[0] != 2 {
		t.Errorf("onRound saw Open = %d, want 2 (1 original + 1 just written)", seenOpen[0])
	}
}

// A nil onRound is how a caller opts out of the log -- Converge must not
// panic or otherwise assume a callback exists. The ask here adds a genuinely
// new question ("who are the users?", distinct from the fixture's existing
// "What is the retention period?"), so Open never reaches zero and both
// rounds of the ceiling run -- a nil callback must not shorten that.
func TestOnRoundCallbackIsNotCalledIfNil(t *testing.T) {
	path := write(t, intentWithOneOpen)
	c := Converge(path, 2, func(int) []Added {
		return []Added{{Agent: "analyst", Questions: []string{"who are the users?"}}}
	}, nil)

	if len(c.Rounds) != 2 {
		t.Errorf("Rounds = %d, want 2 (nil onRound must not change how Converge runs, and Open never reached 0)", len(c.Rounds))
	}
}

// N rounds that actually complete means N calls to onRound -- no batching,
// no dropped final round.
func TestOnRoundCallbackCalledNTimesForNRoundsThatComplete(t *testing.T) {
	path := write(t, intentWithOneOpen)
	calls := 0
	Converge(path, 4, func(int) []Added {
		return []Added{{Agent: "analyst", Questions: []string{"who are the users?"}}}
	}, func(Round) { calls++ })

	if calls != 4 {
		t.Errorf("onRound called %d times, want 4", calls)
	}
}

// A round where every agent added nothing new still ran, still consumed a
// round of the ceiling, and still gets logged -- a caller reading the log
// must be able to see that a round was spent for no gain, not have it vanish.
func TestOnRoundCallbackInvokedEvenIfRoundAddedNothing(t *testing.T) {
	path := write(t, intentWithOneOpen)
	calls := 0
	Converge(path, 3, func(int) []Added { return nil }, func(Round) { calls++ })

	if calls != 3 {
		t.Errorf("onRound called %d times, want 3 (the ceiling, since nothing ever closes)", calls)
	}
}

// The round that hits the ceiling and triggers escalation is still a round
// that ran a write and an assessment -- it must be logged like any other, not
// swallowed because Converge is about to return Escalated.
func TestOnRoundCallbackInvokedEvenOnEscalationCase(t *testing.T) {
	path := write(t, intentWithOneOpen)
	var logged []Round
	c := Converge(path, 2, func(round int) []Added {
		return []Added{{Agent: "analyst", Questions: []string{"q" + string(rune('0'+round)) + "?"}}}
	}, func(r Round) { logged = append(logged, r) })

	if !c.Escalated {
		t.Fatal("precondition: this run must escalate")
	}
	if len(logged) != 2 {
		t.Fatalf("onRound was called %d times, want 2 (including the round that escalated)", len(logged))
	}
}

// Escalated and Ready are mutually exclusive by construction: a run that hit
// the ceiling with questions still open must never report itself fit to
// design from, no matter how the ceiling was reached.
func TestNeverReturnsReadyTrueWhileEscalated(t *testing.T) {
	path := write(t, intentWithOneOpen)
	c := Converge(path, 1, func(int) []Added {
		return []Added{{Agent: "analyst", Questions: []string{"new one?"}}}
	}, nil)

	if !c.Escalated {
		t.Fatal("precondition: this run must escalate")
	}
	if c.Ready() {
		t.Error("Ready() was true on an escalated run; a stage would design from an unanswered question")
	}
}

// Open > 0 alone is enough to block Ready, independent of whether the run
// escalated -- a round that stopped early (ceiling not yet reached) with
// something still open must not be mistaken for done.
func TestNeverReturnsReadyTrueWhileOpenIsGreaterThanZero(t *testing.T) {
	path := write(t, intentWithOneOpen)
	c := Converge(path, 5, func(round int) []Added {
		if round == 1 {
			return []Added{{Agent: "analyst", Questions: []string{"still open?"}}}
		}
		return nil
	}, nil)

	if c.Open == 0 {
		t.Fatal("precondition: this run must leave a question open")
	}
	if c.Ready() {
		t.Error("Ready() was true with Open > 0")
	}
}

// The gate message is read back by a human deciding what to answer. Every
// question still open when the message is built must be named in it -- one
// left out is one the human never sees and never answers.
func TestGateMessageAlwaysNamesEveryOpenQuestionWhenReadBack(t *testing.T) {
	path := write(t, intentWithOneOpen)
	c := Converge(path, 1, func(int) []Added {
		return []Added{
			{Agent: "analyst", Questions: []string{"who are the users?"}},
			{Agent: "security", Questions: []string{"what is the auth model?"}},
			{Agent: "architect", Questions: []string{"single tenant or multi?"}},
		}
	}, nil)

	msg := c.GateMessage("OR-152")
	for _, q := range c.Questions {
		if q.Answered {
			continue
		}
		if !strings.Contains(msg, q.Text) {
			t.Errorf("gate message omits open question %q:\n%s", q.Text, msg)
		}
	}
}
