package collect

import (
	"bytes"
	"strings"
	"testing"

	"github.com/orion-sdlc/orion/internal/config"
	"github.com/orion-sdlc/orion/internal/workspace"
)

// Two tickets in flight is the normal state under `orion watch`, and it is
// the state that produces this: FCIA-9's branch is cut from origin/develop
// before FCIA-8 lands, so once FCIA-8 merges the base has moved and the two
// changes may overlap.
//
// Nothing is ever overwritten -- separate worktrees, separate branches -- and
// git refuses the merge rather than guessing. The bug was entirely in the
// response. Orion never asked gh for `mergeable`, so a conflict was
// indistinguishable from any other merge failure, and the recovery path
// ("leave the request in place, a later pass retries") re-attempted an
// impossible merge every tick, forever, while never once saying that a human
// had to rebase.

func TestAConflictedBranchIsReportedNotRetried(t *testing.T) {
	home, _ := bound(t)
	jira := newTracker()
	res, out, c := run(t, home, jira, PR{
		// The realistic shape: checks PASSED, against a base that has since
		// moved. A conflict is not a CI failure and does not look like one.
		Verdict: VerdictPassing, Conflicted: true, Head: "abc123",
		URL: "https://example/pr/2", Detail: "3 passed",
	}, Options{})

	if res[0].Verdict != VerdictConflicted {
		t.Fatalf("verdict = %q, want conflicted", res[0].Verdict)
	}
	if c.pruned != 0 || c.refreshed != 0 {
		t.Error("a conflicted branch must not be pruned or fast-forwarded")
	}
	if !strings.Contains(out, "conflicts with its base") {
		t.Errorf("the reason must be named:\n%s", out)
	}
	if !strings.Contains(out, "rebase") {
		t.Errorf("the only action that resolves this must be named:\n%s", out)
	}
	// The ticket stays in ci-wait on purpose: the moment somebody pushes a
	// rebase, the conflict clears and the normal flow resumes with no
	// re-labelling by hand.
	for _, r := range jira.removed {
		if strings.Contains(strings.Join(r, ","), "ci-wait") {
			t.Error("released from ci-wait; a rebase would then be picked up by nothing")
		}
	}
}

// Said once, not every two minutes. A warning that repeats on a timer is one
// people mute, and muting the channel loses every later message too.
func TestTheSameConflictIsAnnouncedOnce(t *testing.T) {
	home, _ := bound(t)
	jira := newTracker()
	pr := PR{Verdict: VerdictPassing, Conflicted: true, Head: "abc123",
		URL: "https://example/pr/2"}

	run(t, home, jira, pr, Options{})
	first := len(jira.comments["FCIA-6"])
	run(t, home, jira, pr, Options{}) // same HEAD: nothing has changed

	// len(jira.comments) would count KEYS, not comments, and would pass
	// whatever happened. Count the comments on the ticket itself.
	if got := len(jira.comments["FCIA-6"]); got != first {
		t.Errorf("commented %d times for one unchanged conflict, want %d", got, first)
	}
	if first == 0 {
		t.Error("the first pass must say something")
	}
}

// A pushed rebase that STILL conflicts is a new situation and must be
// reported again -- somebody tried something and it did not work, and they
// need to know that rather than assuming silence means success.
func TestARebaseThatStillConflictsIsAnnouncedAgain(t *testing.T) {
	home, _ := bound(t)
	jira := newTracker()

	run(t, home, jira, PR{Verdict: VerdictPassing, Conflicted: true,
		Head: "abc123", URL: "https://example/pr/2"}, Options{})
	first := len(jira.comments["FCIA-6"])

	run(t, home, jira, PR{Verdict: VerdictPassing, Conflicted: true,
		Head: "def456", URL: "https://example/pr/2"}, Options{}) // new commit

	if len(jira.comments["FCIA-6"]) <= first {
		t.Error("a new HEAD is a new situation and must be reported")
	}
}

// UNKNOWN is not CONFLICTING. GitHub reports UNKNOWN for a few seconds after
// a push while it computes mergeability, and announcing a rebase then would
// send someone to fix a conflict that does not exist.
func TestOnlyAConfirmedConflictCounts(t *testing.T) {
	home, _ := bound(t)
	jira := newTracker()
	res, out, _ := run(t, home, jira, PR{
		Verdict: VerdictPassing, Conflicted: false, Head: "abc123",
		URL: "https://example/pr/2", Detail: "3 passed",
	}, Options{})

	if res[0].Verdict == VerdictConflicted {
		t.Error("mergeability was not CONFLICTING; nothing should have been claimed")
	}
	if strings.Contains(out, "conflicts with its base") {
		t.Errorf("a conflict was announced without one:\n%s", out)
	}
}

// One pass once printed two rebase commands for the same branch, seconds
// apart: "behind main, rebase onto origin/main" from the staleness path, and
// "conflicts with its base, rebase onto origin/develop" from this one. The
// repository's protected_branches were ["main", "develop"] and its
// work_branch was main, so the conflict path -- which scanned the branch list
// for one literally NAMED develop -- matched a branch no pull request had
// ever been based on (OR-112).
//
// origin/develop existed, abandoned when the work branch moved, so the wrong
// command did not fail. It rebased onto stale code and buried the change in
// an unrelated diff. That is why this asserts on both messages together: each
// one alone was individually defensible, and the fault was that they
// disagreed.
func TestBothMessagesNameTheSameBase(t *testing.T) {
	cases := []struct {
		name string
		pr   PR
		cfg  config.Config
		want string
	}{
		{
			// The forge is asked first: the base is a property of the pull
			// request, and this is correct even for a PR opened by hand
			// against something the config does not name.
			name: "the pull request's own base outranks config",
			pr:   PR{Head: "abc123", BaseRef: "release/24.3"},
			cfg:  vcs("main", "main", "develop"),
			want: "release/24.3",
		},
		{
			// The exact configuration from the report.
			name: "develop is listed but main is the work branch",
			pr:   PR{Head: "abc123"},
			cfg:  vcs("main", "main", "develop"),
			want: "main",
		},
		{
			name: "the ordinary case, develop as the work branch",
			pr:   PR{Head: "abc123"},
			cfg:  vcs("develop", "main", "develop"),
			want: "develop",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			conflictOut := sayConflicted(t, c.pr, c.cfg)
			staleOut := sayStale(t, c.pr, c.cfg)

			for _, out := range []string{conflictOut, staleOut} {
				if !strings.Contains(out, "origin/"+c.want) {
					t.Errorf("must rebase onto origin/%s:\n%s", c.want, out)
				}
			}
			// Both halves of the original bug: a branch named because of
			// what it is CALLED, and the two paths disagreeing.
			for _, other := range []string{"main", "develop", "release/24.3"} {
				if other == c.want {
					continue
				}
				for _, out := range []string{conflictOut, staleOut} {
					if strings.Contains(out, "origin/"+other) {
						t.Errorf("named origin/%s, but the base is %s:\n%s", other, c.want, out)
					}
				}
			}
		})
	}
}

// Neither the forge nor the config knows. Saying so is the whole point: the
// old fallback returned "main" here, which is a guess that reads exactly like
// an answer.
func TestAnUnknowableBaseIsReportedNotGuessed(t *testing.T) {
	if base, known := baseOf(PR{}, config.Config{}); known || base != "" {
		t.Fatalf("baseOf = %q, %v; want an admission that it cannot tell", base, known)
	}

	out := sayConflicted(t, PR{Head: "abc123"}, config.Config{})
	if strings.Contains(out, "git rebase origin/") {
		t.Errorf("printed a rebase command without knowing the base:\n%s", out)
	}
	if !strings.Contains(out, "work_branch") {
		t.Errorf("must say what to set to resolve this:\n%s", out)
	}
}

// vcs builds a config with the branch model spelled out, so each case above
// reads as the repository it describes.
func vcs(work string, protected ...string) config.Config {
	var c config.Config
	c.VCS.WorkBranch = work
	c.VCS.DefaultBranch = "main"
	c.VCS.ProtectedBranches = protected
	return c
}

// A workspace whose branch list contains develop -- the condition the old
// name-matching fallback keyed on. Without it these tests would pass against
// the code that shipped the bug.
func wsListing(t *testing.T, branches ...string) *workspace.Workspace {
	t.Helper()
	t.Setenv("ORION_HOME", t.TempDir())
	ws := newWorkspace(t, "or112")
	ws.Task.Branches = branches
	if err := ws.SaveTask(); err != nil {
		t.Fatalf("saving task: %v", err)
	}
	return ws
}

func sayConflicted(t *testing.T, pr PR, cfg config.Config) string {
	t.Helper()
	var buf bytes.Buffer
	conflicted(Result{}, "OR-89", pr, cfg, "orion/or-89", Options{},
		Deps{Jira: newTracker()}, wsListing(t, "main", "develop"), nil, &buf)
	return buf.String()
}

func sayStale(t *testing.T, pr PR, cfg config.Config) string {
	t.Helper()
	var buf bytes.Buffer
	stale(Result{}, "OR-89", pr, "orion/or-89", cfg, Options{},
		Deps{Jira: newTracker()}, wsListing(t, "main", "develop"), nil, &buf)
	return buf.String()
}
