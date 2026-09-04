package discovery

// Cases assigned under OR-152: the escalation message's content (ceiling,
// round detail, gate message, answer command) and question deduplication
// (same round, across rounds, case, punctuation, spacing) plus the single
// intent file as the only place questions land.

import (
	"strconv"
	"strings"
	"testing"
)

// The escalation names the ceiling and how many rounds actually ran, not just
// that it stopped -- "it stopped" is what an unbounded loop also looks like.
func TestEscalationNamesCeilingAndRoundsCompleted(t *testing.T) {
	path := write(t, intentWithOneOpen)
	calls := 0
	c := Converge(path, 3, forever(t, 3, 1, &calls), nil)

	if !c.Escalated {
		t.Fatal("precondition: this run must escalate")
	}
	msg := c.EscalationMessage("OR-152")
	if !strings.Contains(msg, "did not converge in 3 round(s)") {
		t.Errorf("escalation does not name the ceiling/rounds completed:\n%s", msg)
	}
	if len(c.Rounds) != 3 {
		t.Errorf("recorded %d rounds, want 3", len(c.Rounds))
	}
}

// The escalation includes round detail -- which agent added what -- not just
// a bare count, because that is the first thing anybody asks about a gate
// that stopped.
func TestEscalationIncludesRoundDetailsPerAgent(t *testing.T) {
	path := write(t, intentWithOneOpen)
	c := Converge(path, 2, func(round int) []Added {
		return []Added{
			{Agent: "analyst", Questions: []string{"who are the users, round " + strconv.Itoa(round) + "?"}},
			{Agent: "security", Questions: []string{"what data classification, round " + strconv.Itoa(round) + "?"}},
		}
	}, nil)

	if !c.Escalated {
		t.Fatal("precondition: this run must escalate")
	}
	msg := c.EscalationMessage("OR-152")
	for _, want := range []string{"analyst +1", "security +1"} {
		if !strings.Contains(msg, want) {
			t.Errorf("escalation omits round detail %q:\n%s", want, msg)
		}
	}
}

// The escalation hands over to the gate message, which lists every open
// question -- there is one place a person reads what is still unanswered.
func TestEscalationIncludesGateMessageListingOpenQuestions(t *testing.T) {
	path := write(t, intentWithOneOpen)
	calls := 0
	c := Converge(path, 2, forever(t, 2, 1, &calls), nil)

	msg := c.EscalationMessage("OR-152")
	if !strings.Contains(msg, c.GateMessage("OR-152")) {
		t.Errorf("escalation does not include the gate message verbatim:\n%s", msg)
	}
	if !strings.Contains(msg, "What is the retention period?") {
		t.Errorf("escalation omits an open question:\n%s", msg)
	}
}

// The escalation names the orion command to resolve it -- a block that
// doesn't say how to clear it is a block people route around.
func TestEscalationNamesTheAnswerCommand(t *testing.T) {
	path := write(t, intentWithOneOpen)
	calls := 0
	c := Converge(path, 1, forever(t, 1, 1, &calls), nil)

	msg := c.EscalationMessage("OR-152")
	if !strings.Contains(msg, "orion answer OR-152") {
		t.Errorf("escalation does not name the answer command:\n%s", msg)
	}
}

// Two agents asking the same question in the same round is one question in
// the file, not two.
func TestDuplicateQuestionInSameRoundAppearsOnce(t *testing.T) {
	path := write(t, "# Intent\nSomething.\n")
	c := Converge(path, 1, func(int) []Added {
		return []Added{
			{Agent: "analyst", Questions: []string{"what is the retention period?"}},
			{Agent: "architect", Questions: []string{"what is the retention period?"}},
		}
	}, nil)

	if c.Open != 1 {
		t.Errorf("Open = %d, want 1", c.Open)
	}
	if n := strings.Count(readIntent(t, path), "what is the retention period?"); n != 1 {
		t.Errorf("question appears %d times in the file, want 1", n)
	}
}

// A question asked in round 1 and asked again in round 2 (by any agent) is
// one question, not a second entry -- otherwise rounds compound the same
// idea once per round.
func TestDuplicateQuestionAcrossRoundsAppearsOnce(t *testing.T) {
	path := write(t, "# Intent\nSomething.\n")
	c := Converge(path, 2, func(round int) []Added {
		return []Added{{Agent: "analyst", Questions: []string{"what is the retention period?"}}}
	}, nil)

	if c.Open != 1 {
		t.Errorf("Open = %d, want 1 (round 2 re-asked round 1's question)", c.Open)
	}
	if n := strings.Count(readIntent(t, path), "what is the retention period?"); n != 1 {
		t.Errorf("question appears %d times in the file, want 1", n)
	}
}

// Matching is case-insensitive: "Which tenant model?" and "which tenant
// model?" are the same question to a human resolving them.
func TestQuestionMatchingIsCaseInsensitive(t *testing.T) {
	path := write(t, "# Intent\nSomething.\n")
	c := Converge(path, 1, func(int) []Added {
		return []Added{
			{Agent: "analyst", Questions: []string{"Which tenant model?"}},
			{Agent: "architect", Questions: []string{"which tenant model?"}},
		}
	}, nil)

	if c.Open != 1 {
		t.Errorf("Open = %d, want 1", c.Open)
	}
}

// Matching ignores trailing punctuation: "?" is not part of the identity of
// the question.
func TestQuestionMatchingIgnoresTrailingPunctuation(t *testing.T) {
	path := write(t, "# Intent\nSomething.\n")
	c := Converge(path, 1, func(int) []Added {
		return []Added{
			{Agent: "analyst", Questions: []string{"which tenant model?"}},
			{Agent: "architect", Questions: []string{"which tenant model"}},
		}
	}, nil)

	if c.Open != 1 {
		t.Errorf("Open = %d, want 1", c.Open)
	}
}

// Matching ignores internal spacing differences: extra whitespace between
// words is not a new question.
func TestQuestionMatchingIgnoresInternalSpacing(t *testing.T) {
	path := write(t, "# Intent\nSomething.\n")
	c := Converge(path, 1, func(int) []Added {
		return []Added{
			{Agent: "analyst", Questions: []string{"which  tenant   model?"}},
			{Agent: "architect", Questions: []string{"which tenant model?"}},
		}
	}, nil)

	if c.Open != 1 {
		t.Errorf("Open = %d, want 1", c.Open)
	}
}

// When every question an agent offered in a round turns out to be a
// duplicate, the round log says it added nothing -- not that it contributed.
func TestRoundLogShowsAgentAddedNothingWhenAllDuplicates(t *testing.T) {
	path := write(t, intentWithOneOpen)
	var logged []Round
	Converge(path, 2, func(round int) []Added {
		if round == 1 {
			return []Added{{Agent: "analyst", Questions: []string{"which tenant model?"}}}
		}
		// round 2: same question, differently cased/punctuated -- entirely a
		// duplicate of what round 1 already wrote.
		return []Added{{Agent: "analyst", Questions: []string{"Which tenant model"}}}
	}, func(r Round) { logged = append(logged, r) })

	if len(logged) != 2 {
		t.Fatalf("logged %d rounds, want 2", len(logged))
	}
	line := logged[1].Line()
	if !strings.Contains(line, "nothing added") {
		t.Errorf("round 2 added only a duplicate; its log line should say so, got %q", line)
	}
}

// Every agent's questions land in the single intent file -- there is no
// per-agent queue to check separately.
func TestAllAgentsQuestionsLandInTheSingleIntentFile(t *testing.T) {
	path := write(t, "# Intent\nSomething.\n")
	Converge(path, 1, func(int) []Added {
		return []Added{
			{Agent: "analyst", Questions: []string{"who are the users?"}},
			{Agent: "architect", Questions: []string{"which tenant model?"}},
			{Agent: "security", Questions: []string{"what is the data classification?"}},
			{Agent: "qa", Questions: []string{"what does success look like?"}},
		}
	}, nil)

	body := readIntent(t, path)
	for _, q := range []string{
		"who are the users?", "which tenant model?",
		"what is the data classification?", "what does success look like?",
	} {
		if !strings.Contains(body, "- "+q) {
			t.Errorf("%q is not in the one intent file %s:\n%s", q, path, body)
		}
	}
}
