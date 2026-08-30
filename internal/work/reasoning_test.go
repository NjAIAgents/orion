package work

// OR-201. The reasoning has to reach the event log.
//
// 80% of that log is tool and say -- the mechanical trace of what commands
// ran. What a person reviewing an unattended overnight run actually needs is
// what was asked, what was answered, and what was chosen and why, and that is
// precisely the part that was absent: over 5,839 real events, "answer" and
// "decision" had never once been emitted.
//
// These tests fail if any of that goes back to the terminal only.

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/orion-sdlc/orion/internal/advise"
	"github.com/orion-sdlc/orion/internal/events"
	"github.com/orion-sdlc/orion/internal/supervisor"
	"github.com/orion-sdlc/orion/internal/tracker"
	"github.com/orion-sdlc/orion/internal/workspace"
)

// stopsToAsk is the shape every test below needs: a first run that produces
// nothing and asks, then a run that commits.
func stopsToAsk(t *testing.T, question string) func(*workspace.Workspace, supervisor.Options) (*supervisor.Result, error) {
	t.Helper()
	runs := 0
	return func(ws *workspace.Workspace, o supervisor.Options) (*supervisor.Result, error) {
		runs++
		if runs == 1 {
			return &supervisor.Result{ExitCode: 0, SessionID: "s", Final: question}, nil
		}
		if err := os.WriteFile(filepath.Join(ws.RepoDir(), "impl.go"), []byte("package x\n"), 0o644); err != nil {
			return nil, err
		}
		git(t, ws.RepoDir(), "add", ".")
		git(t, ws.RepoDir(), "commit", "-q", "-m", "feat: done")
		return &supervisor.Result{ExitCode: 0, SessionID: "s", Final: "done"}, nil
	}
}

func eventsOfKind(evs []events.Event, kind string) []events.Event {
	var out []events.Event
	for _, e := range evs {
		if e.Kind == kind {
			out = append(out, e)
		}
	}
	return out
}

// The answer's TEXT is the point. An answer event reading "the advisor
// responded" -- or carrying only the decision's first line -- is worth
// nothing: it records that a reply arrived and leaves out the only thing that
// explains what the implementer did next.
func TestAnAnsweredAskRecordsWhatTheAdvisorActuallySaid(t *testing.T) {
	home := project(t, cfg)
	var out strings.Builder

	// Deliberately multi-line. A headline would survive firstLine; the
	// qualification on the second line is what a reviewer needs and is
	// exactly what truncation drops.
	const decision = "Key segments by issuer.\nMCC is a presentation concern and must not reach the store."
	const question = "Are segments keyed by MCC or by issuer?\nBoth appear in the fixtures and they disagree."
	run, _ := advisor("TECHNICAL",
		`{"verdict":"derived","decision":`+quoteJSON(decision)+`,"grounding":"spec.md section 4"}`)

	Run(Options{Keys: []string{"FCIA-6"}, Out: &out, Home: home},
		Deps{
			Jira: &fakeJira{}, Advise: run,
			Supervise: stopsToAsk(t, question),
			Push:      func(string, string) error { return nil },
			OpenPR:    func(string, string, string, string, string) (string, error) { return "https://pr/1", nil },
		})

	evs, err := events.Read(findEventLog(t, home))
	if err != nil {
		t.Fatal(err)
	}
	asks := eventsOfKind(evs, events.KindAsk)
	answers := eventsOfKind(evs, events.KindAnswer)
	if len(asks) != 1 || len(answers) != 1 {
		t.Fatalf("%d ask(s) and %d answer(s); an ask and its answer are a pair", len(asks), len(answers))
	}
	if asks[0].Msg != question {
		t.Errorf("the ask event carries %q, want the whole question:\n%s", asks[0].Msg, question)
	}
	if answers[0].Msg != decision {
		t.Errorf("the answer event carries %q, want the decision unedited:\n%s", answers[0].Msg, decision)
	}
	if answers[0].Detail["grounding"] != "spec.md section 4" {
		t.Errorf("the answer did not record what it was grounded in: %+v", answers[0].Detail)
	}
	if answers[0].Actor != string(advise.RoleArchitect) {
		t.Errorf("the answer is attributed to %q, want the advisor that gave it", answers[0].Actor)
	}

	// And the choice itself, recorded as a decision rather than as the name
	// of the file it was written to.
	decisions := eventsOfKind(evs, events.KindDecision)
	var recorded string
	for _, d := range decisions {
		if strings.Contains(d.Msg, "recorded in") {
			recorded = d.Msg
		}
	}
	if recorded == "" {
		t.Fatalf("no decision event for the recorded decision: %+v", decisions)
	}
	if !strings.Contains(recorded, "Key segments by issuer.") ||
		!strings.Contains(recorded, "spec.md section 4") {
		t.Errorf("the decision event names neither the choice nor its grounding: %q", recorded)
	}
}

// An ask the artifacts cannot ground produces a refusal carrying the whole
// reason. "the artifacts are silent" is the START of a refusal; what it goes
// on to say is which document a person now has to amend.
func TestAnUngroundableAskIsClosedByARefusalCarryingItsReason(t *testing.T) {
	home := project(t, cfg)
	var out strings.Builder

	const reason = "spec.md is silent on segment keys.\nA person decides, and spec.md section 4 should then say so."
	run, _ := advisor("TECHNICAL", `{"verdict":"refused","reason":`+quoteJSON(reason)+`}`)

	Run(Options{Keys: []string{"FCIA-6"}, Out: &out, Home: home},
		Deps{
			Jira: &fakeJira{}, Advise: run,
			Supervise: stopsToAsk(t, "Are segments keyed by MCC or by issuer?"),
			Push:      func(string, string) error { return nil },
			OpenPR:    func(string, string, string, string, string) (string, error) { return "https://pr/1", nil },
		})

	evs, err := events.Read(findEventLog(t, home))
	if err != nil {
		t.Fatal(err)
	}
	refusals := eventsOfKind(evs, events.KindRefuse)
	if len(eventsOfKind(evs, events.KindAsk)) != 1 || len(refusals) != 1 {
		t.Fatalf("want one ask closed by one refusal, got %d and %d",
			len(eventsOfKind(evs, events.KindAsk)), len(refusals))
	}
	if refusals[0].Msg != reason {
		t.Errorf("the refusal carries %q, want the whole reason:\n%s", refusals[0].Msg, reason)
	}
	if n := len(eventsOfKind(evs, events.KindAnswer)); n != 0 {
		t.Errorf("%d answer event(s) for a refused question", n)
	}
}

// THE BUG. An advisor that cannot be reached used to return a refusal to the
// caller and emit nothing, so the log said a question was asked and never
// said what became of it -- a dangling ask, on the one path where a person
// most needs to know why the run stopped.
func TestAnUnreachableAdvisorStillClosesTheAsk(t *testing.T) {
	home := project(t, cfg)
	var out strings.Builder

	run := advise.Runner(func(dir, model, prompt string) (string, error) {
		if model == advise.ModelRouter {
			return "TECHNICAL", nil
		}
		return "", errors.New("the api is down")
	})

	Run(Options{Keys: []string{"FCIA-6"}, Out: &out, Home: home},
		Deps{
			Jira: &fakeJira{}, Advise: run,
			Supervise: stopsToAsk(t, "Are segments keyed by MCC or by issuer?"),
			Push:      func(string, string) error { return nil },
			OpenPR:    func(string, string, string, string, string) (string, error) { return "https://pr/1", nil },
		})

	evs, err := events.Read(findEventLog(t, home))
	if err != nil {
		t.Fatal(err)
	}
	refusals := eventsOfKind(evs, events.KindRefuse)
	if len(refusals) != 1 {
		t.Fatalf("%d refusal(s); an ask that never reached an advisor is still an ask "+
			"that has to be closed", len(refusals))
	}
	if !strings.Contains(refusals[0].Msg, "the api is down") {
		t.Errorf("the refusal does not say why the advisor was unreachable: %q", refusals[0].Msg)
	}
}

// The invariant behind all three: NO PATH LEAVES AN ASK OPEN. Stated once,
// over every advisor behaviour, so a fourth path added later is covered
// without anyone remembering to write a fourth test.
func TestNoPathProducesAnAskWithNeitherAnAnswerNorARefusal(t *testing.T) {
	cases := []struct {
		name  string
		route string
		run   advise.Runner
	}{
		{name: "derived", run: mustAdvisor("TECHNICAL",
			`{"verdict":"derived","decision":"By issuer.","grounding":"spec.md section 4"}`)},
		{name: "refused", run: mustAdvisor("TECHNICAL",
			`{"verdict":"refused","reason":"the spec is silent"}`)},
		{name: "unparseable", run: mustAdvisor("TECHNICAL", "I think probably by issuer?")},
		{name: "derived without grounding", run: mustAdvisor("TECHNICAL",
			`{"verdict":"derived","decision":"By issuer."}`)},
		{name: "escalated by both roles", run: mustAdvisor("TECHNICAL",
			`{"verdict":"escalate","reason":"that is a product call"}`,
			`{"verdict":"escalate","reason":"that is a technical call"}`)},
		{name: "advisor unreachable", run: func(dir, model, prompt string) (string, error) {
			if model == advise.ModelRouter {
				return "TECHNICAL", nil
			}
			return "", errors.New("the api is down")
		}},
		{name: "router unreachable too", run: func(dir, model, prompt string) (string, error) {
			return "", errors.New("the api is down")
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home := project(t, cfg)
			var out strings.Builder
			Run(Options{Keys: []string{"FCIA-6"}, Out: &out, Home: home},
				Deps{
					Jira: &fakeJira{}, Advise: tc.run,
					Supervise: stopsToAsk(t, "Are segments keyed by MCC or by issuer?"),
					Push:      func(string, string) error { return nil },
					OpenPR:    func(string, string, string, string, string) (string, error) { return "https://pr/1", nil },
				})

			evs, err := events.Read(findEventLog(t, home))
			if err != nil {
				t.Fatal(err)
			}
			asks := len(eventsOfKind(evs, events.KindAsk))
			closed := len(eventsOfKind(evs, events.KindAnswer)) + len(eventsOfKind(evs, events.KindRefuse))
			if asks == 0 {
				t.Fatal("the run never asked, so this case proves nothing")
			}
			if closed != asks {
				t.Errorf("%d ask(s) closed by %d answer(s)+refusal(s): the log records a question "+
					"and never what became of it", asks, closed)
			}
		})
	}
}

// An escalation is not the end of the story: the first advisor declines and
// the retry to the other role is the one that actually answers. The ask has
// to close on THAT answer, not get lost in the handoff between the two calls.
func TestAnEscalatedAskIsClosedByTheOtherRolesAnswer(t *testing.T) {
	home := project(t, cfg)
	var out strings.Builder

	run, _ := advisor("TECHNICAL",
		`{"verdict":"escalate","reason":"that is a product call"}`,
		`{"verdict":"derived","decision":"Ship it behind a flag.","grounding":"roadmap.md"}`)

	Run(Options{Keys: []string{"FCIA-6"}, Out: &out, Home: home},
		Deps{
			Jira: &fakeJira{}, Advise: run,
			Supervise: stopsToAsk(t, "Should this ship now or wait for the redesign?"),
			Push:      func(string, string) error { return nil },
			OpenPR:    func(string, string, string, string, string) (string, error) { return "https://pr/1", nil },
		})

	evs, err := events.Read(findEventLog(t, home))
	if err != nil {
		t.Fatal(err)
	}
	asks := eventsOfKind(evs, events.KindAsk)
	answers := eventsOfKind(evs, events.KindAnswer)
	escalations := eventsOfKind(evs, events.KindEscalate)
	if len(asks) != 1 || len(answers) != 1 {
		t.Fatalf("%d ask(s) and %d answer(s): the ask must close on the second role's answer",
			len(asks), len(answers))
	}
	if len(escalations) != 1 {
		t.Errorf("%d escalate event(s); the handoff between roles should itself be on record", len(escalations))
	}
	if answers[0].Msg != "Ship it behind a flag." {
		t.Errorf("the answer event carries %q, want the second role's decision", answers[0].Msg)
	}
	if n := len(eventsOfKind(evs, events.KindRefuse)); n != 0 {
		t.Errorf("%d refusal(s) for an ask that the second role answered", n)
	}
}

// A ticket is not limited to one question. Each ask the implementer raises
// has to close on ITS OWN answer -- a run that asks five times and only
// records one closed pair would still pass a single-question test.
func TestEachAskInARepeatedLoopClosesOnItsOwnAnswer(t *testing.T) {
	home := project(t, cfg)
	var out strings.Builder

	run := advise.Runner(func(dir, model, prompt string) (string, error) {
		if model == advise.ModelRouter {
			return "TECHNICAL", nil
		}
		return `{"verdict":"derived","decision":"Do it this way.","grounding":"spec.md 1"}`, nil
	})

	runs := 0
	Run(Options{Keys: []string{"FCIA-6"}, Out: &out, Home: home},
		Deps{
			Jira: &fakeJira{}, Advise: run,
			Supervise: func(ws *workspace.Workspace, o supervisor.Options) (*supervisor.Result, error) {
				runs++ // never commits, always asks again, until the cap stops it
				return &supervisor.Result{ExitCode: 0, SessionID: "s",
					Final: "But what about the other case?"}, nil
			},
			Push:   func(string, string) error { return nil },
			OpenPR: func(string, string, string, string, string) (string, error) { return "", nil },
		})

	evs, err := events.Read(findEventLog(t, home))
	if err != nil {
		t.Fatal(err)
	}
	asks := eventsOfKind(evs, events.KindAsk)
	answers := eventsOfKind(evs, events.KindAnswer)
	if len(asks) < 2 {
		t.Fatalf("only %d ask(s) logged; this case needs more than one to prove pairing "+
			"across a loop, not just within a single question", len(asks))
	}
	if len(answers) != len(asks) {
		t.Errorf("%d ask(s) but %d answer(s): every ask in the loop must close on its own answer, "+
			"not just the first or the last", len(asks), len(answers))
	}
}

// Routing is a CHOICE BETWEEN ALTERNATIVES with a stated reason -- another
// actor could have worked this ticket, and the label says why this one did.
// It was a note, which put it among the ninety-odd other things worth seeing
// and made it unfindable, which is the same as not having been recorded.
func TestRoutingATicketIsRecordedAsADecision(t *testing.T) {
	for _, tc := range []struct {
		name, label, actor, why string
	}{
		{"a routed ticket", "ui", events.ActorFrontend, "matched ui"},
		// The default is the case that matters most: a route falling through
		// in silence is how the frontend actor went unreached for as long as
		// it did (OR-171).
		{"the default", "", events.ActorImplementer, "defaulting to the implementer"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := project(t, cfg)
			issue := &tracker.Issue{Key: "FCIA-6", Summary: "restyle the button",
				URL: "https://x/browse/FCIA-6"}
			if tc.label != "" {
				issue.Labels = []string{tc.label}
			}
			var out strings.Builder
			Run(Options{Keys: []string{"FCIA-6"}, Out: &out, Home: home},
				Deps{
					Jira: &fakeJira{issue: issue},
					Supervise: func(ws *workspace.Workspace, o supervisor.Options) (*supervisor.Result, error) {
						if err := os.WriteFile(filepath.Join(ws.RepoDir(), "impl.go"), []byte("package x\n"), 0o644); err != nil {
							return nil, err
						}
						git(t, ws.RepoDir(), "add", ".")
						git(t, ws.RepoDir(), "commit", "-q", "-m", "feat: done")
						return &supervisor.Result{ExitCode: 0, SessionID: "s", Final: "done"}, nil
					},
					Push:   func(string, string) error { return nil },
					OpenPR: func(string, string, string, string, string) (string, error) { return "https://pr/1", nil },
				})

			evs, err := events.Read(findEventLog(t, home))
			if err != nil {
				t.Fatal(err)
			}
			var found string
			for _, e := range eventsOfKind(evs, events.KindDecision) {
				if strings.Contains(e.Msg, "routed to") {
					found = e.Msg
				}
			}
			if found == "" {
				t.Fatalf("the routing choice is not in the log as a decision: %+v", evs)
			}
			if !strings.Contains(found, tc.actor) {
				t.Errorf("the decision does not name the actor it chose: %q", found)
			}
			if !strings.Contains(found, tc.why) {
				t.Errorf("the decision does not say why: %q, want it to mention %q", found, tc.why)
			}
		})
	}
}

// mustAdvisor is advisor() without the call counter, for table cases.
func mustAdvisor(route string, replies ...string) advise.Runner {
	r, _ := advisor(route, replies...)
	return r
}

// quoteJSON embeds a multi-line string in a JSON literal.
func quoteJSON(s string) string {
	return `"` + strings.ReplaceAll(strings.ReplaceAll(s, `"`, `\"`), "\n", `\n`) + `"`
}
