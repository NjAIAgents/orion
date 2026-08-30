package work

import (
	"strings"
	"testing"

	"github.com/orion-sdlc/orion/internal/supervisor"
	"github.com/orion-sdlc/orion/internal/workspace"
)

const notSignedIn = "claude is not authenticated: Anthropic profile login expired " +
	"- Re-authenticate your Anthropic profile. Run: claude, sign in, then restart the watcher."

func loggedOutRun() (*supervisor.Result, error) {
	// What the supervisor returns for this state: a non-zero exit AND an error,
	// which is exactly why the check has to come before the failure path.
	return &supervisor.Result{ExitCode: 1, Reason: notSignedIn, Unauthenticated: true},
		errBoom{}
}

type errBoom struct{}

func (errBoom) Error() string { return "stage ticket failed: " + notSignedIn }

// Nothing was attempted -- no turn, no token, no branch work -- so the ticket
// must go back to the queue rather than wear orion-failed. Three tickets wore
// it on 2026-08-30 and had to be hand-cleared (OR-212).
func TestALoggedOutCLIRequeuesTheTicketRatherThanFailingIt(t *testing.T) {
	home := project(t, cfg)
	j := &fakeJira{}
	var out strings.Builder

	res := Run(Options{Keys: []string{"FCIA-6"}, Out: &out, Home: home},
		Deps{
			Jira: j,
			Supervise: func(*workspace.Workspace, supervisor.Options) (*supervisor.Result, error) {
				return loggedOutRun()
			},
			Push:   func(string, string) error { t.Fatal("pushed after a run that never started"); return nil },
			OpenPR: func(string, string, string, string, string) (string, error) { return "", nil },
		})

	if res[0].Outcome != OutcomeNoAuth {
		t.Fatalf("outcome = %q, want %q", res[0].Outcome, OutcomeNoAuth)
	}
	if strings.Contains(j.labelLog(), "orion-failed") {
		t.Errorf("the ticket was labelled failed for a problem that never touched it: %s", j.labelLog())
	}
	if !strings.Contains(j.labelLog(), "add:ORION remove:orion-working") {
		t.Errorf("the claim was not handed back to the queue: %s", j.labelLog())
	}
	if got := out.String(); !strings.Contains(got, "claude is not authenticated") ||
		!strings.Contains(got, "sign in") {
		t.Errorf("the run output names neither the cause nor the fix:\n%s", got)
	}
	if strings.Contains(out.String(), "claude exited 1") {
		t.Errorf("the exit code was reported instead of the cause:\n%s", out.String())
	}
}

// The batch stop must keep working -- and now say WHY, since here the reason
// is known rather than inferred from correlation.
func TestTheBatchStopsAndNamesTheLoginAsTheReason(t *testing.T) {
	home := project(t, cfg)
	j := &fakeJira{}
	var out strings.Builder

	res := Run(Options{Keys: []string{"FCIA-6", "FCIA-7", "FCIA-8"}, Out: &out, Home: home},
		Deps{
			Jira: j,
			Supervise: func(*workspace.Workspace, supervisor.Options) (*supervisor.Result, error) {
				return loggedOutRun()
			},
			Push:   func(string, string) error { return nil },
			OpenPR: func(string, string, string, string, string) (string, error) { return "", nil },
		})

	if len(res) != 1 {
		t.Fatalf("worked %d tickets; the rest would have failed identically", len(res))
	}
	if !strings.Contains(out.String(), "stopping the batch: claude is not authenticated") {
		t.Errorf("the stop did not name the reason it knew:\n%s", out.String())
	}
}

// The counterpart: a genuine work failure, with the run reporting no auth
// problem, must still be a failed run exactly as before.
func TestAGenuineFailureIsStillAFailedRun(t *testing.T) {
	home := project(t, cfg)
	j := &fakeJira{}
	var out strings.Builder

	res := Run(Options{Keys: []string{"FCIA-6"}, Out: &out, Home: home},
		Deps{
			Jira: j,
			Supervise: func(*workspace.Workspace, supervisor.Options) (*supervisor.Result, error) {
				return &supervisor.Result{ExitCode: 1, Reason: "breaker tripped: 400 tool calls"}, nil
			},
			Push:   func(string, string) error { return nil },
			OpenPR: func(string, string, string, string, string) (string, error) { return "", nil },
		})

	if res[0].Outcome != OutcomeFailed {
		t.Fatalf("outcome = %q, want %q", res[0].Outcome, OutcomeFailed)
	}
	if !strings.Contains(j.labelLog(), "add:orion-failed remove:orion-working") {
		t.Errorf("a real failure must still be labelled: %s", j.labelLog())
	}
}
