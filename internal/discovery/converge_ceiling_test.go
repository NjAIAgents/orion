package discovery

// A zero ceiling is "no discovery", not "discovery that instantly failed": if
// nothing was open before Converge ran, there is nobody to escalate to just
// because no rounds were spent finding out.

import "testing"

func TestConvergeZeroCeilingDoesNotEscalateWhenNothingWasOpen(t *testing.T) {
	path := write(t, "# Intent\n\n## Open questions\n- None\n")
	calls := 0
	c := Converge(path, 0, func(int) []Added { calls++; return nil }, nil)

	if calls != 0 {
		t.Errorf("ask ran %d times; a zero ceiling asks nobody", calls)
	}
	if len(c.Rounds) != 0 {
		t.Errorf("recorded %d rounds, want 0", len(c.Rounds))
	}
	if c.Escalated {
		t.Error("nothing was open before the (zero) ceiling; there is nothing to escalate")
	}
}

// The ticket states this as its own case, distinct from the one above: a
// zero ceiling must never escalate, even when the file already had an open
// question before Converge ran a single round. A zero ceiling means "the
// caller asked for no discovery" -- it ran no round, so there is nothing it
// tried and failed at, which is what Escalated is supposed to mean. Reporting
// Escalated=true here also breaks EscalationMessage, which would tell a human
// "discovery did not converge in 0 round(s)" about a run that never asked
// anyone anything.
func TestConvergeZeroCeilingDoesNotEscalateEvenWithPreexistingOpenQuestions(t *testing.T) {
	path := write(t, intentWithOneOpen)
	calls := 0
	c := Converge(path, 0, func(int) []Added { calls++; return nil }, nil)

	if calls != 0 {
		t.Errorf("ask ran %d times; a zero ceiling asks nobody", calls)
	}
	if len(c.Rounds) != 0 {
		t.Errorf("recorded %d rounds, want 0", len(c.Rounds))
	}
	if c.Escalated {
		t.Error("a zero ceiling ran no rounds; it must never escalate, regardless of what was already open")
	}
}

// Ready is the one signal the rest of the chain trusts to start design work,
// so it has to agree with both halves of its own definition at once: zero
// open questions, and a run that did not escalate. Neither alone is enough.
func TestReadyRequiresBothZeroOpenAndNotEscalated(t *testing.T) {
	converged := Converge(write(t, "# Intent\n\n## Open questions\n- None\n"), 3,
		func(int) []Added { return nil }, nil)
	if converged.Open != 0 || converged.Escalated {
		t.Fatalf("precondition: want Open=0, Escalated=false; got Open=%d Escalated=%v",
			converged.Open, converged.Escalated)
	}
	if !converged.Ready() {
		t.Error("Open=0 and not escalated: Ready() must be true")
	}

	calls := 0
	escalated := Converge(write(t, intentWithOneOpen), 2, forever(t, 2, 1, &calls), nil)
	if escalated.Open == 0 || !escalated.Escalated {
		t.Fatalf("precondition: want Open>0, Escalated=true; got Open=%d Escalated=%v",
			escalated.Open, escalated.Escalated)
	}
	if escalated.Ready() {
		t.Error("Open>0 and escalated: Ready() must be false")
	}
}
