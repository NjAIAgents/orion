package work

// The tracker side of a stage boundary, end to end through a real run.
//
// What OR-225 asked for is that a claimed ticket say WHICH actor holds it.
// What it must not cost is the claim lock: orion-working is matched exactly
// by the in-flight query and by the queue's NOT IN, so it stays untouched
// and a SECOND, inert label carries the stage. These tests assert both
// halves -- the stage moves, and the lock's own writes are byte for byte
// what they were.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/orion-sdlc/orion/internal/actors"
	"github.com/orion-sdlc/orion/internal/events"
	"github.com/orion-sdlc/orion/internal/supervisor"
	"github.com/orion-sdlc/orion/internal/tracker"
	"github.com/orion-sdlc/orion/internal/workspace"
)

// stageRun is the happy path of TestSuccessfulRunClaimsRunsPushesAndHandsOffToCI,
// reduced to the label log it produces.
func stageRun(t *testing.T) *fakeJira {
	t.Helper()
	home := project(t, cfg)
	j := &fakeJira{}
	var out strings.Builder

	res := Run(Options{Keys: []string{"FCIA-6"}, Out: &out, Home: home},
		Deps{
			Jira: j,
			Supervise: func(ws *workspace.Workspace, o supervisor.Options) (*supervisor.Result, error) {
				if err := os.WriteFile(filepath.Join(ws.RepoDir(), "impl.go"),
					[]byte("package x\n"), 0o644); err != nil {
					return nil, err
				}
				git(t, ws.RepoDir(), "add", ".")
				git(t, ws.RepoDir(), "commit", "-q", "-m", "feat: implement")
				return &supervisor.Result{ExitCode: 0, Reason: "completed"}, nil
			},
			Push: func(dir, branch string) error { return nil },
			OpenPR: func(dir, branch, title, body, base string) (string, error) {
				return "https://github.com/x/y/pull/4", nil
			},
		})
	if len(res) != 1 || res[0].Outcome != OutcomeCIWait {
		t.Fatalf("result = %+v", res)
	}
	return j
}

// A CLAIMED TICKET CARRIES BOTH LABELS. The lock says somebody is on it; the
// stage says who. Before this, the board could not tell implementation from
// QA from an advisory pass.
func TestAClaimedTicketSaysWhichActorHoldsIt(t *testing.T) {
	log := stageRun(t).labelLog()

	// The claim is its own write, unchanged, and the stage follows in a
	// separate one. Bundled together, a stage label the tracker refused
	// would take the claim down with it.
	if !strings.Contains(log, "add:"+tracker.LabelWorking+" remove:ORION") {
		t.Fatalf("the claim's own write changed shape: %s", log)
	}
	claimed := strings.Index(log, "add:"+tracker.LabelWorking+" remove:ORION")
	stage := strings.Index(log, "add:"+actors.StageLabel(events.ActorOrion))
	if stage < 0 {
		t.Fatalf("a claimed ticket carried no stage label at all: %s", log)
	}
	if stage < claimed {
		t.Errorf("the stage label was set before the claim it describes: %s", log)
	}
	// And the actor that actually takes the run replaces it, so the board
	// names the agent that is spending rather than the one that dispatched.
	if !strings.Contains(log, "add:"+actors.StageLabel(events.ActorImplementer)+
		" remove:"+actors.StageLabel(events.ActorOrion)) {
		t.Errorf("the implementer taking the run did not move the stage label: %s", log)
	}
}

// A HANDOFF REPLACES THE STAGE AND NEVER TOUCHES THE LOCK. One write, so the
// ticket is never briefly wearing two stages or none, and the lock label
// appears in no part of it.
func TestAStageHandoffNeverTouchesTheLockLabel(t *testing.T) {
	for _, call := range stageRun(t).labelCalls {
		if !strings.Contains(call, actors.StageLabelPrefix) {
			continue
		}
		if strings.Contains(call, tracker.LabelWorking) ||
			strings.Contains(call, tracker.LabelCIWait) ||
			strings.Contains(call, tracker.LabelFailed) {
			// Except the release, which is deliberately one atomic write.
			if strings.HasPrefix(call, "add:"+tracker.LabelCIWait) {
				continue
			}
			t.Errorf("a stage write also moved a lock label: %s", call)
		}
		add, _, _ := strings.Cut(strings.TrimPrefix(call, "add:"), " remove:")
		if strings.Contains(add, ",") {
			t.Errorf("a handoff added more than one stage label: %s", call)
		}
	}
}

// RELEASING A CLAIM REMOVES BOTH. A stage label that outlived the claim
// would have the tracker naming an actor for work nobody is doing -- the
// exact confusion this label was added to remove, pointed the other way.
func TestHandingOffToCIClearsTheStageWithTheLock(t *testing.T) {
	j := stageRun(t)
	var release string
	for _, call := range j.labelCalls {
		if strings.HasPrefix(call, "add:"+tracker.LabelCIWait) {
			release = call
		}
	}
	if release == "" {
		t.Fatalf("the ticket was never handed to CI: %s", j.labelLog())
	}
	if !strings.Contains(release, "remove:"+tracker.LabelWorking) {
		t.Fatalf("the lock was not released: %s", release)
	}
	for _, l := range actors.StageLabels() {
		if !strings.Contains(release, l) {
			t.Errorf("releasing the claim left %s behind: %s", l, release)
		}
	}
	// Nothing is written after the release. The boundary into CI hands to a
	// machine, which holds no stage, so a write there would re-label a
	// ticket that is no longer claimed.
	last := j.labelCalls[len(j.labelCalls)-1]
	if last != release {
		t.Errorf("a stage label was written after the claim was released: %s", last)
	}
}

// A FAILED RUN CLEARS IT TOO. orion-failed means a person has to look; a
// stage label beside it would name an agent that stopped hours ago.
func TestAFailedRunClearsTheStageLabel(t *testing.T) {
	home := project(t, cfg)
	j := &fakeJira{}
	var out strings.Builder

	Run(Options{Keys: []string{"FCIA-6"}, Out: &out, Home: home},
		Deps{
			Jira: j,
			Supervise: func(ws *workspace.Workspace, o supervisor.Options) (*supervisor.Result, error) {
				return &supervisor.Result{ExitCode: 1, Reason: "failed"}, nil
			},
			Push:   func(dir, branch string) error { return nil },
			OpenPR: func(dir, branch, title, body, base string) (string, error) { return "", nil },
		})

	var failed string
	for _, call := range j.labelCalls {
		if strings.HasPrefix(call, "add:"+tracker.LabelFailed) {
			failed = call
		}
	}
	if failed == "" {
		t.Fatalf("the run was not marked failed: %s", j.labelLog())
	}
	if !strings.Contains(failed, "remove:"+tracker.LabelWorking) {
		t.Fatalf("the claim was not released on failure: %s", failed)
	}
	for _, l := range actors.StageLabels() {
		if !strings.Contains(failed, l) {
			t.Errorf("a failed ticket kept the stage label %s: %s", l, failed)
		}
	}
}

// A TRACKER THAT REFUSES THE STAGE LABEL COSTS NOTHING. It is decoration:
// the run finishes, the lock is taken and released as usual, and the only
// consequence is a less informative Jira view.
func TestAStageLabelThatCannotBeWrittenDoesNotFailTheRun(t *testing.T) {
	home := project(t, cfg)
	// Refuses the stage writes and only those, so the lock's own writes
	// still land and the run has to survive on them alone.
	j := &refuseStage{}
	var out strings.Builder

	res := Run(Options{Keys: []string{"FCIA-6"}, Out: &out, Home: home},
		Deps{
			Jira: j,
			Supervise: func(ws *workspace.Workspace, o supervisor.Options) (*supervisor.Result, error) {
				if err := os.WriteFile(filepath.Join(ws.RepoDir(), "impl.go"),
					[]byte("package x\n"), 0o644); err != nil {
					return nil, err
				}
				git(t, ws.RepoDir(), "add", ".")
				git(t, ws.RepoDir(), "commit", "-q", "-m", "feat: implement")
				return &supervisor.Result{ExitCode: 0, Reason: "completed"}, nil
			},
			Push: func(dir, branch string) error { return nil },
			OpenPR: func(dir, branch, title, body, base string) (string, error) {
				return "https://github.com/x/y/pull/4", nil
			},
		})
	if len(res) != 1 || res[0].Outcome != OutcomeCIWait {
		t.Fatalf("a refused stage label changed the outcome: %+v", res)
	}
	if !strings.Contains(j.labelLog(), "add:"+tracker.LabelWorking+" remove:ORION") {
		t.Errorf("the claim did not happen: %s", j.labelLog())
	}
	if !strings.Contains(j.labelLog(), "add:"+tracker.LabelCIWait) {
		t.Errorf("the claim was not released: %s", j.labelLog())
	}
}

// refuseStage is a fakeJira that rejects any write adding a stage label.
type refuseStage struct{ fakeJira }

func (f *refuseStage) SetLabels(key string, add, remove []string) error {
	for _, l := range add {
		if strings.HasPrefix(l, actors.StageLabelPrefix) {
			return errRefused
		}
	}
	return f.fakeJira.SetLabels(key, add, remove)
}

var errRefused = &refusedError{}

type refusedError struct{}

func (*refusedError) Error() string { return "labels are locked on this project" }
