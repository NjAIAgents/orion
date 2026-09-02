package discovery

// Bounded convergence for a discovery round in which SEVERAL agents may each
// add questions (OR-152).
//
// The gate this package already enforces is "zero open questions". A single
// agent cannot defeat it: it asks what it does not know, a person answers, the
// chain proceeds. Several agents can, and not by misbehaving -- each round is
// free to add questions the previous round's answers made thinkable, so the
// count the gate watches can rise every round while every participant is doing
// exactly what it was asked to do. The gate then recedes forever and each
// receding round is paid for.
//
// So the thing that ends it is a COUNT, not anyone's judgement about whether
// the questions are getting better. That is the same answer qa.max_rounds
// gives to two agents disagreeing about a test, and it is the only answer that
// can be proved rather than hoped for: Converge calls ask at most rounds
// times, so it terminates for every ask, including one that adds new questions
// forever.
//
// At the ceiling it ESCALATES. It does not proceed with unanswered questions
// -- that would make the ceiling a way of buying a silent guess, which is the
// failure the discovery gate exists to prevent, arrived at by a longer route.
//
// Every question lands in the ONE intent file, whoever asked it. Orion writes
// them rather than trusting each agent to, because "answer these in four
// queues" is a workflow nobody follows: `orion answer <id>` already reads that
// file, so a human resolves the whole round in one pass.

import (
	"fmt"
	"os"
	"strings"
)

// Added is what one agent contributed in one round.
type Added struct {
	Agent     string
	Questions []string
}

// Round is the record of one convergence round: who added what, and what is
// still open after it. A gate that moves without explanation cannot be
// debugged, and the count alone does not say which agent moved it.
type Round struct {
	N, Of int
	Added []Added
	// Open is the unanswered count in the intent file AFTER this round's
	// questions were written to it.
	Open int
}

// Line is the one-line record a caller emits to the event log.
func (r Round) Line() string {
	var parts []string
	for _, a := range r.Added {
		if len(a.Questions) == 0 {
			continue
		}
		parts = append(parts, fmt.Sprintf("%s +%d", a.Agent, len(a.Questions)))
	}
	what := "nothing added"
	if len(parts) > 0 {
		what = strings.Join(parts, ", ")
	}
	return fmt.Sprintf("discovery round %d/%d: %s -- %d open", r.N, r.Of, what, r.Open)
}

// Ask runs one round of question-adding agents and returns what each of them
// added. It is given the round number so a caller can prompt differently on a
// later round; it is never called more than the ceiling allows.
type Ask func(round int) []Added

// Convergence is the outcome of a bounded round of discovery.
type Convergence struct {
	Assessment
	Rounds []Round
	// Escalated is true when the ceiling was reached with questions still
	// open. It is the only state in which a person must be told; it is never
	// a state in which the chain may proceed.
	Escalated bool
}

// Converge runs at most rounds rounds of question-adding, writing every
// question into the single intent file, and stops the moment nothing is open
// or the ceiling is reached -- whichever comes first.
//
// onRound, when non-nil, is called with each round's record AS IT COMPLETES
// rather than at the end, so a run that is killed mid-convergence still leaves
// the rounds it paid for in the log.
//
// rounds is the ceiling from config (config.Discovery.Rounds). Zero or less
// runs no rounds at all: it assesses the file and returns, which is a caller
// asking for no discovery, not for unbounded discovery.
func Converge(path string, rounds int, ask Ask, onRound func(Round)) Convergence {
	c := Convergence{Assessment: Assess(path)}
	for n := 1; n <= rounds; n++ {
		var added []Added
		if ask != nil {
			added = ask(n)
		}
		for i, a := range added {
			// What was actually written, not what was offered: two agents
			// asking the same thing differently-cased is one question for the
			// human, and recording it twice would say the round moved the
			// gate twice as far as it did.
			kept, err := addQuestions(path, a.Questions)
			if err != nil {
				kept = nil
			}
			added[i].Questions = kept
		}
		c.Assessment = Assess(path)
		r := Round{N: n, Of: rounds, Added: added, Open: c.Open}
		c.Rounds = append(c.Rounds, r)
		if onRound != nil {
			onRound(r)
		}
		if c.Open == 0 {
			return c
		}
	}
	c.Escalated = c.Open > 0
	return c
}

// EscalationMessage is what a person is shown when the ceiling is reached with
// questions still open. It names the rounds, because the first thing anybody
// asks about a gate that stopped is what it spent getting there, and then
// hands over to the gate's own message so there is one way to answer.
func (c Convergence) EscalationMessage(id string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "discovery did not converge in %d round(s); it stops here rather than asking again.\n\n",
		len(c.Rounds))
	for _, r := range c.Rounds {
		fmt.Fprintf(&b, "  %s\n", r.Line())
	}
	b.WriteString("\n")
	b.WriteString(c.GateMessage(id))
	return b.String()
}

// addQuestions appends questions to the intent file's Open questions section
// and returns the ones it actually wrote.
//
// A question already in the file is dropped. Rounds compound by design, so
// without this the fourth round re-asks the first round's questions and the
// human is asked to answer the same idea in as many places as there were
// agents -- the thing this is supposed to stop.
func addQuestions(path string, qs []string) ([]string, error) {
	if len(qs) == 0 {
		return nil, nil
	}
	b, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	src := string(b)

	seen := map[string]bool{}
	for _, q := range Assess(path).Questions {
		seen[normalize(q.Text)] = true
	}
	var write []string
	for _, q := range qs {
		q = strings.TrimSpace(q)
		if q == "" || seen[normalize(q)] {
			continue
		}
		seen[normalize(q)] = true
		write = append(write, q)
	}
	if len(write) == 0 {
		return nil, nil
	}

	lines := strings.Split(src, "\n")
	at := sectionEnd(lines)
	if at < 0 {
		// No section yet: the file may be an intent capture that had nothing
		// open, or may not exist at all. Either way the questions go where
		// Assess and `orion answer` look for them.
		if strings.TrimSpace(src) == "" {
			lines = nil
		} else {
			lines = append(trimTrailingBlank(lines), "")
		}
		lines = append(lines, "## Open questions", "")
		at = len(lines) - 1
	}
	out := append([]string{}, lines[:at]...)
	for _, q := range write {
		out = append(out, "- "+q)
	}
	out = append(out, lines[at:]...)

	body := strings.Join(out, "\n")
	if !strings.HasSuffix(body, "\n") {
		body += "\n"
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		return nil, err
	}
	return write, nil
}

// sectionEnd is the line index a new question is inserted at: just past the
// last bullet of the Open questions section, or -1 when there is no section.
func sectionEnd(lines []string) int {
	in, end := false, -1
	for i, line := range lines {
		t := strings.TrimSpace(line)
		if headingRe.MatchString(t) {
			in, end = true, i+1
			continue
		}
		if in && strings.HasPrefix(t, "#") {
			return end
		}
		if in && bulletRe.MatchString(line) {
			end = i + 1
		}
	}
	return end
}

func trimTrailingBlank(lines []string) []string {
	for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

// normalize is how two spellings of one question are recognised as one. Crude
// on purpose: it catches the case that actually happens, which is the same
// sentence from two agents with different casing or trailing punctuation.
func normalize(s string) string {
	return strings.ToLower(strings.Trim(strings.Join(strings.Fields(s), " "), " .?!*_`"))
}
