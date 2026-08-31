package work

// OR-89. A merged ticket was re-queued, the agent found the change already
// present and correctly declined to redo it, and Orion recorded that as a
// failure. Two separable defects, and one test each way round: a merged
// ticket must not be workable, a no-op must not be a failure -- and a
// genuine question must still be one.

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/orion-sdlc/orion/internal/supervisor"
	"github.com/orion-sdlc/orion/internal/tracker"
	"github.com/orion-sdlc/orion/internal/workspace"
)

// The first half: a ticket whose pull request has merged must not be workable
// again. The assertion that matters is that NOTHING was spent -- the check
// happens before the claim, so no agent starts and no label says one did.
func TestAMergedTicketIsNotWorkedAgain(t *testing.T) {
	home := project(t, cfg)
	j := &fakeJira{}
	var out strings.Builder

	res := Run(Options{Keys: []string{"FCIA-6"}, Out: &out, Home: home},
		Deps{
			Jira: j,
			Merged: func(dir, branch string) (bool, string, error) {
				if branch != "orion/fcia-6" {
					t.Errorf("asked about the wrong branch: %q", branch)
				}
				return true, "https://gh/pr/13", nil
			},
			Supervise: func(*workspace.Workspace, supervisor.Options) (*supervisor.Result, error) {
				t.Fatal("an agent was started on a ticket that had already merged")
				return nil, nil
			},
			Push:   func(string, string) error { t.Fatal("pushed"); return nil },
			OpenPR: func(string, string, string, string, string) (string, error) { return "", nil },
		})

	if res[0].Outcome != OutcomeNoop {
		t.Fatalf("outcome = %q, want no-op", res[0].Outcome)
	}
	if strings.Contains(j.labelLog(), "add:"+tracker.LabelWorking) {
		t.Errorf("a merged ticket was claimed: %s", j.labelLog())
	}
	// Every label Orion owns, not just the one that let it through: which one
	// survived the merge is the thing we do not know.
	for _, l := range tracker.Managed("ORION") {
		if !strings.Contains(j.labelLog(), l) {
			t.Errorf("%s was not cleared: %s", l, j.labelLog())
		}
	}
	// In Progress is where the incident left the ticket. It must not go there
	// at all, and must end on Done.
	for _, s := range j.transitions {
		if s == "In Progress" {
			t.Errorf("a merged ticket was moved to In Progress: %v", j.transitions)
		}
	}
	if len(j.transitions) == 0 || j.transitions[len(j.transitions)-1] != "Done" {
		t.Errorf("transitions = %v, want it moved to Done", j.transitions)
	}
	if c := strings.Join(j.comments, " "); !strings.Contains(c, "already merged") {
		t.Errorf("the reason was not recorded on the ticket: %v", j.comments)
	}
}

// A check that could not be made is not a merged branch. gh missing, or the
// network down, must not refuse the work.
func TestAnUnreadableMergeCheckDoesNotStopTheRun(t *testing.T) {
	home := project(t, cfg)
	j := &fakeJira{}
	pushed := false
	var out strings.Builder

	res := Run(Options{Keys: []string{"FCIA-6"}, Out: &out, Home: home},
		Deps{
			Jira: j,
			Merged: func(string, string) (bool, string, error) {
				return false, "", errors.New("gh is not installed")
			},
			Supervise: func(ws *workspace.Workspace, o supervisor.Options) (*supervisor.Result, error) {
				if err := os.WriteFile(filepath.Join(ws.RepoDir(), "impl.go"),
					[]byte("package x\n"), 0o644); err != nil {
					return nil, err
				}
				git(t, ws.RepoDir(), "add", ".")
				git(t, ws.RepoDir(), "commit", "-q", "-m", "feat: implement")
				return &supervisor.Result{ExitCode: 0, Reason: "completed"}, nil
			},
			Push:   func(string, string) error { pushed = true; return nil },
			OpenPR: func(string, string, string, string, string) (string, error) { return "https://gh/pr/1", nil },
		})

	if res[0].Outcome != OutcomeCIWait || !pushed {
		t.Fatalf("outcome = %q, pushed = %v; an unreadable check refused the work",
			res[0].Outcome, pushed)
	}
}

// The second half: a run that changes nothing because the work is already
// present is not a failure and must not be labelled as one.
func TestANoChangeRunIsNotAFailure(t *testing.T) {
	home := project(t, cfg)
	j := &fakeJira{}
	var out strings.Builder

	res := Run(Options{Keys: []string{"FCIA-6"}, Out: &out, Home: home},
		Deps{
			Jira: j,
			Supervise: func(*workspace.Workspace, supervisor.Options) (*supervisor.Result, error) {
				return &supervisor.Result{ExitCode: 0,
					Final: "I read parse.go and the guard is already there.\n\n" +
						"NOTHING TO DO: cf39261 already rejects the key at parse time."}, nil
			},
			Advise: func(dir, model, prompt string) (string, error) {
				t.Fatal("an advisor was paid to answer a run that asked nothing")
				return "", nil
			},
			Push:   func(string, string) error { t.Fatal("pushed an empty branch"); return nil },
			OpenPR: func(string, string, string, string, string) (string, error) { return "", nil },
		})

	if res[0].Outcome != OutcomeNoop {
		t.Fatalf("outcome = %q, want no-op", res[0].Outcome)
	}
	if strings.Contains(j.labelLog(), "add:"+tracker.LabelFailed) {
		t.Errorf("a no-op run was labelled failed: %s", j.labelLog())
	}
	// Every label Orion owns must come off, and the list is asked for rather
	// than spelled out: a literal here passes or fails on the ORDER and
	// SPELLING of a set that grows (orion-ready arrived with OR-253), which
	// tests the string rather than the behaviour. The property is that a
	// finished ticket carries none of Orion's state.
	for _, want := range tracker.Managed("ORION") {
		if !strings.Contains(j.labelLog(), want) {
			t.Errorf("the claim was not fully released: %q is still set: %s",
				want, j.labelLog())
		}
	}
	if len(j.transitions) == 0 || j.transitions[len(j.transitions)-1] != "Done" {
		t.Errorf("transitions = %v, want it moved off In Progress", j.transitions)
	}
	// Distinguishable from a failure where a person reads it, not merely in a
	// field only Orion sees.
	c := strings.Join(j.comments, " ")
	if !strings.Contains(c, "cf39261") || !strings.Contains(c, "NOT a failure") {
		t.Errorf("the comment does not distinguish this from a failure: %v", j.comments)
	}
	if strings.Contains(c, "Orion stopped without making a change") {
		t.Errorf("a no-op was reported as a blocked run: %v", j.comments)
	}
	if !strings.Contains(res[0].Note, "cf39261") {
		t.Errorf("Note = %q, want the agent's reason", res[0].Note)
	}
}

// The other direction, which matters just as much: an agent that stopped
// because it could not decide something is STILL blocked. Collapsing the two
// either way destroys the distinction this change exists to draw.
func TestAQuestionIsStillBlockedNotANoop(t *testing.T) {
	home := project(t, cfg)
	j := &fakeJira{}
	var out strings.Builder

	res := Run(Options{Keys: []string{"FCIA-6"}, Out: &out, Home: home},
		Deps{
			Jira: j,
			Supervise: func(*workspace.Workspace, supervisor.Options) (*supervisor.Result, error) {
				// Prose that sounds like a no-op and is not one.
				return &supervisor.Result{ExitCode: 0,
					Final: "There may be nothing to do here, but I cannot tell whether " +
						"segments are keyed by MCC without spec.md saying so."}, nil
			},
			Push:   func(string, string) error { return nil },
			OpenPR: func(string, string, string, string, string) (string, error) { return "", nil },
		})

	if res[0].Outcome != OutcomeBlocked {
		t.Fatalf("outcome = %q, want blocked", res[0].Outcome)
	}
	if !strings.Contains(j.labelLog(), "add:orion-failed") {
		t.Errorf("a blocked run stopped being visible as one: %s", j.labelLog())
	}
}

// OR-121, the claim-time half: a ticket resolved between the queue search
// and the claim must be skipped, and must lose the label that offered it.
//
// The queue query is the first line of defence and cannot be the only one --
// it races a person closing a ticket, and `orion work KEY` never consults it.
func TestAResolvedTicketIsSkippedAtClaimTime(t *testing.T) {
	home := project(t, cfg)
	j := &fakeJira{issue: &tracker.Issue{
		Key: "FCIA-6", Summary: "fixed by hand", Status: "Done",
		StatusCategory: "Done", URL: "https://x/browse/FCIA-6",
	}}
	var out strings.Builder

	res := Run(Options{Keys: []string{"FCIA-6"}, Out: &out, Home: home},
		Deps{
			Jira: j,
			Supervise: func(*workspace.Workspace, supervisor.Options) (*supervisor.Result, error) {
				t.Fatal("an agent was started on a ticket that was already Done")
				return nil, nil
			},
			Push:   func(string, string) error { t.Fatal("pushed"); return nil },
			OpenPR: func(string, string, string, string, string) (string, error) { return "", nil },
		})

	if res[0].Outcome != OutcomeSkipped {
		t.Fatalf("outcome = %q, want skipped", res[0].Outcome)
	}
	if strings.Contains(j.labelLog(), "add:"+tracker.LabelWorking) {
		t.Errorf("a resolved ticket was claimed: %s", j.labelLog())
	}
	// The label has to go, or the next tick offers the same ticket again and
	// this run repeats for as long as the watcher lives.
	for _, l := range tracker.Managed("ORION") {
		if !strings.Contains(j.labelLog(), "remove:") ||
			!strings.Contains(j.labelLog(), l) {
			t.Errorf("%s was not removed: %s", l, j.labelLog())
		}
	}
	if len(j.transitions) != 0 {
		t.Errorf("a resolved ticket was transitioned: %v", j.transitions)
	}
	// Visible, or an unattended run silently drops a ticket somebody queued.
	if !strings.Contains(out.String(), "already resolved") {
		t.Errorf("the skip was not reported:\n%s", out.String())
	}
}

// An unresolved ticket must still be worked. The category is "indeterminate"
// for In Progress, and empty for a tracker that did not report one -- neither
// is a reason to refuse.
func TestAnUnresolvedStatusDoesNotStopTheRun(t *testing.T) {
	for _, category := range []string{"", "new", "indeterminate"} {
		t.Run("category="+category, func(t *testing.T) {
			home := project(t, cfg)
			j := &fakeJira{issue: &tracker.Issue{
				Key: "FCIA-6", Summary: "do the thing", StatusCategory: category,
			}}
			var out strings.Builder
			ran := false

			res := Run(Options{Keys: []string{"FCIA-6"}, Out: &out, Home: home},
				Deps{
					Jira: j,
					Supervise: func(ws *workspace.Workspace, o supervisor.Options) (*supervisor.Result, error) {
						ran = true
						if err := os.WriteFile(filepath.Join(ws.RepoDir(), "impl.go"),
							[]byte("package x\n"), 0o644); err != nil {
							return nil, err
						}
						git(t, ws.RepoDir(), "add", ".")
						git(t, ws.RepoDir(), "commit", "-q", "-m", "feat: implement")
						return &supervisor.Result{ExitCode: 0, Reason: "completed"}, nil
					},
					Push:   func(string, string) error { return nil },
					OpenPR: func(string, string, string, string, string) (string, error) { return "https://gh/pr/1", nil },
				})

			if !ran || res[0].Outcome != OutcomeCIWait {
				t.Fatalf("outcome = %q, ran = %v; a live ticket was refused",
					res[0].Outcome, ran)
			}
		})
	}
}

func TestNoopDeclared(t *testing.T) {
	cases := []struct {
		name  string
		final string
		want  string
		ok    bool
	}{
		{"plain", "NOTHING TO DO: cf39261 has it", "cf39261 has it", true},
		{"bulleted and bold", "done.\n- **NOTHING TO DO** - the test already exists",
			"the test already exists", true},
		{"lower case", "nothing to do: it is there", "it is there", true},
		{"quoted mid-sentence", "I was told to write NOTHING TO DO: if the work exists.", "", false},
		{"a question", "Are segments keyed by MCC?", "", false},
		{"empty", "", "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			why, ok := noopDeclared(c.final)
			if ok != c.ok || why != c.want {
				t.Errorf("noopDeclared(%q) = %q, %v; want %q, %v",
					c.final, why, ok, c.want, c.ok)
			}
		})
	}
}
