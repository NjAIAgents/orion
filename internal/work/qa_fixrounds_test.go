package work

import (
	"strings"
	"testing"

	"github.com/orion-sdlc/orion/internal/config"
)

// OR-226 raised the shipped ceiling from two to three, but every existing
// round-ceiling test in qa_test.go pins an explicit qa.max_rounds (1 or 2) and
// so proves the loop obeys A configured number, not that the number a project
// which never mentions qa.max_rounds actually runs on is three.
//
// A regression here would be config.FixRounds or config.QA.Rounds changing
// without this stage's own loop moving with it -- the two are read
// separately (config.QA.Rounds() is called once, as cfg.QA.Rounds(), at the
// top of the round loop in qa.go), so nothing except an integration test at
// this level would catch the loop and the constant drifting apart.
func TestQAStopsAfterThreeRoundsWhenTheCeilingIsUnset(t *testing.T) {
	home := project(t, qaCfg) // qaCfg states nothing about qa: the shipped default applies
	j := &fakeJira{}
	// Findings every round, forever: the fix never clears the case, so only
	// the round ceiling -- not a clean verdict -- can stop this loop.
	f := &qaFake{t: t, qaReplies: []string{
		"round 0: the boundary case still fails.",
		"round 1: the boundary case still fails.",
		"round 2: the boundary case still fails.",
		"round 3: the boundary case still fails.",
	}}
	var out strings.Builder

	res := Run(Options{Keys: []string{"FCIA-6"}, Out: &out, Home: home},
		Deps{
			Jira: j, Supervise: f.run,
			Push: func(string, string) error { return nil },
			OpenPR: func(string, string, string, string, string) (string, error) {
				return "https://pr/1", nil
			},
		})

	if config.FixRounds != 3 {
		t.Fatalf("this test assumes the shipped ceiling is 3, but config.FixRounds = %d",
			config.FixRounds)
	}

	// Three full fix-then-reverify exchanges, on top of the initial
	// implement/derive/verify: ticket, qa-cases, qa, then (ticket, qa) x3.
	want := "ticket,qa-cases,qa,ticket,qa,ticket,qa,ticket,qa"
	if got := f.sequence(); got != want {
		t.Fatalf("run sequence = %q, want %q -- the unset ceiling did not run exactly 3 rounds",
			got, want)
	}
	if res[0].Outcome != OutcomeCIWait {
		t.Errorf("QA blocked the change on its own authority: outcome=%q", res[0].Outcome)
	}
	comments := strings.Join(j.comments, "\n")
	if !strings.Contains(comments, "still open after 3 fix round(s)") {
		t.Errorf("the ticket does not say the raised default ran out with findings open:\n%s", comments)
	}
}
