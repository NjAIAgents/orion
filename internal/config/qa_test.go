package config

import (
	"os"
	"path/filepath"
	"testing"
)

// Absent and false mean different things for the QA stage. Verification a
// project never asked to switch off must not be silently missing, so absent
// runs it; an explicit false is a project declining the spend.
func TestQAIsOnUnlessAProjectSaysOtherwise(t *testing.T) {
	dir := t.TempDir()
	write := func(body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, "orion.json"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	write(`{"version":1}`)
	if cfg := Load(dir); !cfg.QA.On() {
		t.Error("a config that says nothing about QA must still run it")
	}
	write(`{"qa":{"enabled":false}}`)
	if cfg := Load(dir); cfg.QA.On() {
		t.Error("an explicit false did not switch the stage off")
	}

	// Zero rounds in JSON is indistinguishable from absent, and no rounds at
	// all would mean the first finding escalates with nobody having tried to
	// fix it -- so zero restores the default rather than removing the loop.
	write(`{"qa":{"max_rounds":0}}`)
	if got, want := Load(dir).QA.Rounds(), Defaults().QA.Rounds(); got != want {
		t.Errorf("max_rounds 0 gave %d rounds, want the default %d", got, want)
	}
	write(`{"qa":{"max_rounds":4}}`)
	if got := Load(dir).QA.Rounds(); got != 4 {
		t.Errorf("max_rounds = %d, want the configured 4", got)
	}
}

// Both fix loops read the SAME shipped ceiling, and it is three (OR-226).
//
// Pinned against the literal rather than against Defaults(), which is what the
// test above does: comparing a default to itself passes whatever the number is,
// so it proves the zero-means-default convention and says nothing about the
// value. The value is the thing this ticket changed, and it is a decision with
// a stated cost -- see FixRounds -- so a change to it should have to come here
// and read that cost rather than slipping through green.
func TestBothFixLoopsShipTheSameCeilingOfThree(t *testing.T) {
	if FixRounds != 3 {
		t.Fatalf("FixRounds = %d, want 3. Raising it costs the worst case on every "+
			"ticket that fails to converge; lowering it escalates work one more "+
			"exchange would have finished. Change the constant's own reasoning too",
			FixRounds)
	}

	dir := t.TempDir()
	write := func(body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, "orion.json"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// Unset. The value almost every repository actually runs on, because it is
	// the one nobody had to find in order to get.
	write(`{"version":1}`)
	cfg := Load(dir)
	if got := cfg.QA.Rounds(); got != 3 {
		t.Errorf("an unset qa.max_rounds resolved to %d, want 3", got)
	}
	if got := cfg.CI.Attempts(); got != 3 {
		t.Errorf("an unset ci.max_fix_attempts resolved to %d, want 3", got)
	}

	// Zero, which JSON cannot tell from absent. Never "no rounds": an absent
	// value must not widen OR remove a control.
	write(`{"qa":{"max_rounds":0},"ci":{"max_fix_attempts":0}}`)
	cfg = Load(dir)
	if got := cfg.QA.Rounds(); got != 3 {
		t.Errorf("qa.max_rounds 0 gave %d, want the default 3", got)
	}
	if got := cfg.CI.Attempts(); got != 3 {
		t.Errorf("ci.max_fix_attempts 0 gave %d, want the default 3", got)
	}

	// An explicit value still wins, in both directions. Raising the default
	// must not have turned it into a floor.
	write(`{"qa":{"max_rounds":1},"ci":{"max_fix_attempts":7}}`)
	cfg = Load(dir)
	if got := cfg.QA.Rounds(); got != 1 {
		t.Errorf("qa.max_rounds = %d, want the configured 1", got)
	}
	if got := cfg.CI.Attempts(); got != 7 {
		t.Errorf("ci.max_fix_attempts = %d, want the configured 7", got)
	}
}

// The decision on a hand-edited absurd value, stated as a test so it cannot be
// changed silently: it is HONOURED, not clamped. The same answer
// Limits.ConcurrentTickets gives, and for the same reason -- a config saying 40
// while the loop ran 5 is a file disagreeing with behaviour, with nothing in
// either place explaining the gap. The argument about a large number happens
// where it is SET; see FixRoundsWarnAbove and cmd/orion/configlimits.go.
func TestAnAbsurdFixRoundCeilingIsHonouredNotClamped(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "orion.json"),
		[]byte(`{"qa":{"max_rounds":40},"ci":{"max_fix_attempts":40}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := Load(dir)
	if got := cfg.QA.Rounds(); got != 40 {
		t.Errorf("qa.max_rounds 40 read back as %d; a configured number is honoured", got)
	}
	if got := cfg.CI.Attempts(); got != 40 {
		t.Errorf("ci.max_fix_attempts 40 read back as %d; a configured number is honoured", got)
	}
}

// The confirmation threshold has to sit ABOVE the shipped default, or setting
// a fix-round ceiling to the value it already has would prompt. Mirrors the
// same guard on ConcurrencyWarnAbove.
func TestTheFixRoundPromptDoesNotFireOnTheDefault(t *testing.T) {
	if FixRoundsWarnAbove <= FixRounds {
		t.Fatalf("FixRoundsWarnAbove (%d) must exceed the default (%d), or the default prompts",
			FixRoundsWarnAbove, FixRounds)
	}
}
