package work

import (
	"errors"
	"strings"
	"testing"

	"github.com/orion-sdlc/orion/internal/supervisor"
	"github.com/orion-sdlc/orion/internal/workspace"
)

// worked is the agent these tests need: one that leaves a commit behind, so
// the run reaches its normal ending rather than the blocked-with-no-work
// path. prepush_test.go's commits() is the agent body; this wraps it in the
// supervisor's signature.
func worked(t *testing.T) func(*workspace.Workspace, supervisor.Options) (*supervisor.Result, error) {
	impl := commits(t, "impl.go", "package x\n")
	return func(ws *workspace.Workspace, _ supervisor.Options) (*supervisor.Result, error) {
		if err := impl(ws); err != nil {
			return nil, err
		}
		return &supervisor.Result{ExitCode: 0, Reason: "completed"}, nil
	}
}

// The claim must be visible in the field a board actually shows. Before
// OR-34 it was a label and nothing else, so anyone looking at Jira saw an
// unassigned ticket move itself to In Progress.
func TestAClaimedTicketIsAssignedToTheAccountOrionRunsAs(t *testing.T) {
	home := project(t, cfg)
	j := &fakeJira{}
	var out strings.Builder

	res := Run(Options{Keys: []string{"FCIA-6"}, Out: &out, Home: home},
		Deps{
			Jira:      j,
			Supervise: worked(t),
			Push:      func(dir, branch string) error { return nil },
			OpenPR: func(dir, branch, title, body, base string) (string, error) {
				return "https://github.com/x/y/pull/4", nil
			},
		})

	if res[0].Outcome != OutcomeCIWait {
		t.Fatalf("outcome = %q", res[0].Outcome)
	}
	if len(j.assigned) != 1 || j.assigned[0] != "FCIA-6" {
		t.Fatalf("assigned = %v, want the claimed ticket exactly once", j.assigned)
	}
}

// A ticket that is claimed but never run is the same problem: the assignment
// belongs to the CLAIM, not to a run that finished.
func TestABlockedRunStillAssignedTheTicketWhenItClaimedIt(t *testing.T) {
	home := project(t, cfg)
	j := &fakeJira{}
	var out strings.Builder

	res := Run(Options{Keys: []string{"FCIA-6"}, Out: &out, Home: home},
		Deps{
			Jira: j,
			Supervise: func(*workspace.Workspace, supervisor.Options) (*supervisor.Result, error) {
				return &supervisor.Result{ExitCode: 0, Reason: "which currency?"}, nil
			},
			Push:   func(string, string) error { return nil },
			OpenPR: func(string, string, string, string, string) (string, error) { return "", nil },
		})

	if res[0].Outcome != OutcomeBlocked {
		t.Fatalf("outcome = %q", res[0].Outcome)
	}
	if len(j.assigned) != 1 {
		t.Errorf("assigned = %v, want the ticket assigned when it was claimed", j.assigned)
	}
	// And it stays assigned on release: on a stopped ticket the assignee is
	// the person to go to, which is worth more than an empty column.
	if !strings.Contains(j.labelLog(), "add:orion-failed remove:orion-working") {
		t.Fatalf("the ticket was not released: %s", j.labelLog())
	}
}

// The assignment is decoration next to the lock. A tracker that refuses it --
// no Assign Issues permission, a deactivated account -- must cost a warning
// and nothing else: a ticket worked but not assigned beats a run refused.
func TestARefusedAssignmentWarnsAndDoesNotFailTheRun(t *testing.T) {
	home := project(t, cfg)
	j := &fakeJira{assignErr: errors.New("403 you do not have permission to assign issues")}
	var out strings.Builder

	res := Run(Options{Keys: []string{"FCIA-6"}, Out: &out, Home: home},
		Deps{
			Jira:      j,
			Supervise: worked(t),
			Push:      func(dir, branch string) error { return nil },
			OpenPR: func(dir, branch, title, body, base string) (string, error) {
				return "https://github.com/x/y/pull/4", nil
			},
		})

	if res[0].Outcome != OutcomeCIWait {
		t.Fatalf("outcome = %q, want the run to finish despite the refusal", res[0].Outcome)
	}
	if !strings.Contains(j.labelLog(), "add:orion-working remove:ORION") {
		t.Errorf("the claim was lost with the assignment: %s", j.labelLog())
	}
	if !strings.Contains(out.String(), "who holds it") {
		t.Errorf("the refusal was swallowed:\n%s", out.String())
	}
}
