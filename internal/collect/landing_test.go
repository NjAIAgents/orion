package collect

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/orion-sdlc/orion/internal/registry"
	"github.com/orion-sdlc/orion/internal/workspace"
)

// The landing queue, against real git (OR-206).
//
// The failure this guards is not a wrong answer, it is a right answer given
// too many times: every branch behind the base rebased on the same pass, each
// force-push invalidated by the next merge before its checks finished, and the
// two branches that had been open longest spent their whole rebase allowance
// losing a race and were handed to a person. Nothing in rebase.go was wrong --
// which is why only a test that runs SEVERAL branches through one pass can
// fail on it.

// twoStaleBranches builds one origin, two ticket branches cut from it, and
// then lands somebody else's commit on develop. That last step is the event
// under test: one merge, and every other open branch is behind at once.
func twoStaleBranches(t *testing.T) (home string, ws *workspace.Workspace, origin string, heads map[string]string) {
	t.Helper()
	home, _ = bound(t)

	entry, err := registry.Lookup(home, "FCIA-6")
	if err != nil {
		t.Fatal(err)
	}
	ws, err = workspace.Open(entry.Workspace)
	if err != nil {
		t.Fatal(err)
	}

	origin, _ = repos(t)
	heads = map[string]string{}
	for _, key := range []string{"FCIA-6", "FCIA-7"} {
		branch := "orion/" + strings.ToLower(key)
		// Exactly where worktreeOrRepo looks: the job's worktree for this
		// branch, one per ticket, as two concurrent jobs would leave them.
		wt := filepath.Join(ws.Dir, "worktrees", strings.ReplaceAll(branch, "/", "-"))
		if err := os.MkdirAll(filepath.Dir(wt), 0o755); err != nil {
			t.Fatal(err)
		}
		gitRun(t, t.TempDir(), "clone", "--quiet", origin, wt)
		gitRun(t, wt, "checkout", "--quiet", "-b", branch, "origin/develop")
		writeCommit(t, wt, strings.ToLower(key)+".txt", key, "work for "+key)
		gitRun(t, wt, "push", "--quiet", "-u", "origin", branch)
		heads[key] = head(t, wt, "HEAD")
	}

	landOnDevelop(t, origin, "other.txt", "theirs")
	return home, ws, origin, heads
}

// pass runs one poll over several tickets, each with its own pull request.
// The shared run() helper answers with one PR for every branch, which cannot
// express two branches at different commits.
func pass(t *testing.T, home string, jira *fakeTracker, prs map[string]PR, keys []string) string {
	t.Helper()
	var buf bytes.Buffer
	Run(Options{Keys: keys, Home: home, Out: &buf}, Deps{
		Jira: jira,
		Status: func(_, branch string) (PR, error) {
			for key, pr := range prs {
				if branch == "orion/"+strings.ToLower(key) {
					return pr, nil
				}
			}
			return PR{Verdict: VerdictUnknown}, nil
		},
		Refresh: func(string, string) (string, error) { return "", nil },
		Prune:   func(*workspace.Workspace, string) error { return nil },
	})
	return buf.String()
}

func passing(heads map[string]string) map[string]PR {
	prs := map[string]PR{}
	for key, sha := range heads {
		prs[key] = PR{Verdict: VerdictPassing, Head: sha, URL: "https://example/pr/" + key}
	}
	return prs
}

// moved counts the branches whose remote tip is no longer where the forge
// reported it -- which is to say, the ones that were rebased and force-pushed.
func moved(t *testing.T, origin string, heads map[string]string) []string {
	t.Helper()
	var out []string
	for key, sha := range heads {
		if head(t, origin, "refs/heads/orion/"+strings.ToLower(key)) != sha {
			out = append(out, key)
		}
	}
	return out
}

// The quadratic, gone. Two branches are behind for the same reason at the same
// instant; rebasing both is the behaviour that starved OR-194 and OR-199.
func TestOnlyOneBranchIsRebasedWhenSeveralAreBehindAtOnce(t *testing.T) {
	home, _, origin, heads := twoStaleBranches(t)

	out := pass(t, home, newTracker(), passing(heads), []string{"FCIA-6", "FCIA-7"})

	if got := moved(t, origin, heads); len(got) != 1 {
		t.Fatalf("%d of 2 branches were rebased in one pass (%v); each force-push "+
			"is invalidated by the next merge, which is how the cost grew with the "+
			"square of the queue\n%s", len(got), got, out)
	}
	if !strings.Contains(out, "holding its turn") {
		t.Errorf("the branch that was not helped must say why it is waiting:\n%s", out)
	}
}

// Holding costs the branch nothing. A branch that spent an allowance while
// waiting would be starved by the queue rather than protected by it.
func TestHoldingSpendsNoRebaseAllowanceAndKeepsTheTicketWaiting(t *testing.T) {
	home, ws, _, heads := twoStaleBranches(t)
	jira := newTracker()

	pass(t, home, jira, passing(heads), []string{"FCIA-6", "FCIA-7"})

	reqs := loadRequests(ws.Dir)
	// FCIA-6 is elected first (nothing has waited longer, ties break on the
	// key), so FCIA-7 is the one that held.
	if n := reqs.Rebases["FCIA-7"]; n != 0 {
		t.Errorf("the holding branch spent %d of its %d rebases without being pushed", n, maxAutoRebases)
	}
	if _, ok := reqs.Waiting["FCIA-7"]; !ok {
		t.Error("the holding branch lost its place in the queue, so waiting earned it nothing")
	}
	if strings.Contains(strings.Join(jira.removed["FCIA-7"], ","), "ci-wait") {
		t.Error("released from ci-wait while merely holding; the next pass would never look at it")
	}
	if got := jira.comments["FCIA-7"]; len(got) != 0 {
		t.Errorf("holding is not news for the ticket: %v", got)
	}
}

// Seniority, not luck and not list order. The pass reaches FCIA-6 first, and
// FCIA-7 still takes the turn because it has been behind longer -- the exact
// property whose absence meant the two longest-open branches starved.
func TestTheBranchThatHasBeenBehindLongestTakesTheTurn(t *testing.T) {
	home, ws, origin, heads := twoStaleBranches(t)

	reqs := loadRequests(ws.Dir)
	reqs.Waiting["FCIA-7"] = time.Now().Add(-time.Hour)
	if err := writeRequests(ws.Dir, reqs); err != nil {
		t.Fatal(err)
	}

	out := pass(t, home, newTracker(), passing(heads), []string{"FCIA-6", "FCIA-7"})

	got := moved(t, origin, heads)
	if len(got) != 1 || got[0] != "FCIA-7" {
		t.Fatalf("rebased %v, want only FCIA-7 -- it has been behind an hour longer, and "+
			"the branch waiting longest is the one starvation reaches first\n%s", got, out)
	}
}

// A ticket that has left the queue must not still be the one holding it shut.
// Being handed to a person is exactly that case: it will never take its turn.
func TestAHandedOverBranchGivesUpItsPlaceSoTheNextOneMoves(t *testing.T) {
	home, ws, origin, heads := twoStaleBranches(t)

	// FCIA-6 is the most senior AND out of rebases: without releasing the
	// place, every branch behind it waits on a turn that never comes.
	reqs := loadRequests(ws.Dir)
	reqs.Waiting["FCIA-6"] = time.Now().Add(-time.Hour)
	reqs.Rebases["FCIA-6"] = maxAutoRebases
	if err := writeRequests(ws.Dir, reqs); err != nil {
		t.Fatal(err)
	}

	out := pass(t, home, newTracker(), passing(heads), []string{"FCIA-6", "FCIA-7"})

	got := moved(t, origin, heads)
	if len(got) != 1 || got[0] != "FCIA-7" {
		t.Fatalf("rebased %v, want only FCIA-7: the queue is held shut by a ticket "+
			"already handed to a person\n%s", got, out)
	}
	if _, ok := loadRequests(ws.Dir).Waiting["FCIA-6"]; ok {
		t.Error("the handed-over ticket kept its place in the queue")
	}
}

// The bound is unchanged. Past the cap the branch is still handed over rather
// than pushed a third time -- the queue changes whose turn it is, not whether
// there is a limit.
func TestTheQueueDoesNotRaiseTheRebaseCeiling(t *testing.T) {
	home, _, origin, sha := staleTicketAtCap(t)

	_, out, _ := run(t, home, newTracker(), PR{
		Verdict: VerdictPassing, Head: sha, URL: "https://example/pr/1",
	}, Options{})

	if got := head(t, origin, "refs/heads/orion/fcia-6"); got != sha {
		t.Error("a lone ticket at the cap was rebased anyway; being the leader is " +
			"not permission to exceed the bound")
	}
	if !strings.Contains(out, "leaving this one to you") {
		t.Errorf("the cap must still escalate in words:\n%s", out)
	}
}

// The separate small bug. A branch handed to a person is handed over ONCE:
// three identical warning-and-commands pairs appeared in one log for branches
// nobody had touched between polls, which is how a reader learns to skim the
// block that was meant to get their attention.
func TestAHandedOverBranchIsAnnouncedOnceNotOncePerPoll(t *testing.T) {
	home, _, _, sha := staleTicketAtCap(t)
	jira := newTracker()
	pr := PR{Verdict: VerdictPassing, Head: sha, URL: "https://example/pr/1"}

	_, first, _ := run(t, home, jira, pr, Options{})
	_, second, _ := run(t, home, jira, pr, Options{})

	for _, want := range []string{"leaving this one to you", "git push --force-with-lease"} {
		if !strings.Contains(first, want) {
			t.Fatalf("the hand-over must say %q the first time:\n%s", want, first)
		}
		if strings.Contains(second, want) {
			t.Errorf("%q was repeated on the next poll, with nothing changed:\n%s", want, second)
		}
	}
	// Quiet, not silent. A ticket that vanishes from the output has been
	// forgotten as far as the reader can tell.
	if !strings.Contains(second, "still behind") {
		t.Errorf("the branch must still be accounted for on later polls:\n%s", second)
	}
	if n := len(jira.comments["FCIA-6"]); n != 1 {
		t.Errorf("the ticket was commented on %d times for one hand-over", n)
	}
}

// A pushed commit is something changing, so the hand-over is announced again:
// the person who moved the branch deserves to be told it is still behind.
func TestAMovedBranchIsAnnouncedAgain(t *testing.T) {
	home, _, _, sha := staleTicketAtCap(t)
	jira := newTracker()

	run(t, home, jira, PR{Verdict: VerdictPassing, Head: sha, URL: "u"}, Options{})
	_, out, _ := run(t, home, jira, PR{Verdict: VerdictPassing, Head: sha + "0", URL: "u"}, Options{})

	if !strings.Contains(out, "git push --force-with-lease") {
		t.Errorf("a branch that moved and is still behind must be reported afresh:\n%s", out)
	}
}

// staleTicketAtCap is one stale ticket that has already used its whole rebase
// allowance -- the state OR-194 and OR-199 were left in.
func staleTicketAtCap(t *testing.T) (home, source, origin, sha string) {
	t.Helper()
	home, source, origin, _, sha = staleTicket(t)
	entry, err := registry.Lookup(home, "FCIA-6")
	if err != nil {
		t.Fatal(err)
	}
	ws, err := workspace.Open(entry.Workspace)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < maxAutoRebases; i++ {
		if _, err := countRebase(ws.Dir, "FCIA-6"); err != nil {
			t.Fatal(err)
		}
	}
	return home, source, origin, sha
}
