package collect

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/orion-sdlc/orion/internal/config"
	"github.com/orion-sdlc/orion/internal/events"
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

// upToDateTicket wires a registered project whose job worktree holds a branch
// that already contains its base's tip -- the ordinary case, not behind. The
// counterpart to staleTicket, which lands a commit on develop afterwards.
func upToDateTicket(t *testing.T) (home string, ws *workspace.Workspace, origin, sha string) {
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
	wt := filepath.Join(ws.Dir, "worktrees", "orion-fcia-6")
	if err := os.MkdirAll(filepath.Dir(wt), 0o755); err != nil {
		t.Fatal(err)
	}
	gitRun(t, t.TempDir(), "clone", "--quiet", origin, wt)
	gitRun(t, wt, "checkout", "--quiet", "orion/x-1")
	gitRun(t, wt, "branch", "--quiet", "-m", "orion/fcia-6")
	gitRun(t, wt, "push", "--quiet", "-u", "origin", "orion/fcia-6")
	sha = head(t, wt, "HEAD")
	return home, ws, origin, sha
}

// seedWaiting puts a key straight into the landing queue file, as if an
// earlier pass had already found it behind -- without going through a whole
// pass to get there.
func seedWaiting(t *testing.T, ws *workspace.Workspace, key string, at time.Time) {
	t.Helper()
	reqs := loadRequests(ws.Dir)
	reqs.Waiting[key] = at
	if err := writeRequests(ws.Dir, reqs); err != nil {
		t.Fatal(err)
	}
}

// A branch that has caught up on its own -- a human rebased it, or it was
// never really behind -- must not keep the place a previous pass gave it, and
// must not be told to hold for a turn it no longer needs.
func TestABranchWhoseBaseHasNotMovedLeavesTheQueueAndDoesNotHold(t *testing.T) {
	home, ws, _, sha := upToDateTicket(t)
	seedWaiting(t, ws, "FCIA-6", time.Now().Add(-time.Hour))
	jira := newTracker()

	_, out, _ := run(t, home, jira, PR{
		Verdict: VerdictPending, Head: sha, URL: "https://example/pr/1",
	}, Options{})

	if _, ok := loadRequests(ws.Dir).Waiting["FCIA-6"]; ok {
		t.Error("a branch whose base has not moved kept its place in the queue")
	}
	if strings.Contains(out, "holding its turn") {
		t.Errorf("a branch that is not behind must not be told to hold a turn:\n%s", out)
	}
}

// A manual lock hands the branch to the person already standing in it (OR-130).
// It must give up its queue place the same as any other hand-over -- a place
// held for a branch that will never take its turn blocks everything behind it.
func TestAManuallyLockedBranchLeavesTheQueue(t *testing.T) {
	home, _, _, wtDir, sha := staleTicket(t)
	entry, err := registry.Lookup(home, "FCIA-6")
	if err != nil {
		t.Fatal(err)
	}
	ws, err := workspace.Open(entry.Workspace)
	if err != nil {
		t.Fatal(err)
	}
	seedWaiting(t, ws, "FCIA-6", time.Now().Add(-time.Hour))
	if err := os.WriteFile(filepath.Join(wtDir, manualLockName), []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}
	jira := newTracker()

	run(t, home, jira, PR{
		Verdict: VerdictPassing, Head: sha, URL: "https://example/pr/1",
	}, Options{})

	if _, ok := loadRequests(ws.Dir).Waiting["FCIA-6"]; ok {
		t.Error("a manually locked branch kept its place in the landing queue")
	}
}

// A conflict is a person's, and a ticket that cannot take its turn must not
// be the one everything else waits for.
func TestAConflictedBranchLeavesTheQueue(t *testing.T) {
	home, _ := bound(t)
	entry, err := registry.Lookup(home, "FCIA-6")
	if err != nil {
		t.Fatal(err)
	}
	ws, err := workspace.Open(entry.Workspace)
	if err != nil {
		t.Fatal(err)
	}
	seedWaiting(t, ws, "FCIA-6", time.Now().Add(-time.Hour))
	jira := newTracker()

	run(t, home, jira, PR{
		Verdict: VerdictPassing, Conflicted: true, URL: "https://example/pr/1",
	}, Options{})

	if _, ok := loadRequests(ws.Dir).Waiting["FCIA-6"]; ok {
		t.Error("a conflicted branch kept its place in the landing queue")
	}
}

// A base git could not determine is a base Orion cannot rebase onto, and
// behind() must not enter the branch into a queue it can never take a turn
// in for that reason.
func TestABranchWhoseBaseCannotBeDeterminedDoesNotEnterTheQueue(t *testing.T) {
	home, _ := bound(t)
	entry, err := registry.Lookup(home, "FCIA-6")
	if err != nil {
		t.Fatal(err)
	}
	ws, err := workspace.Open(entry.Workspace)
	if err != nil {
		t.Fatal(err)
	}
	log, err := events.Open(events.Path(ws.Dir), events.Event{
		Project: "FCIA", Key: "FCIA-6", Run: "1",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()
	var buf bytes.Buffer

	// Neither the PR nor the config names a base: baseOf returns "", false.
	cfg := config.Config{}
	pr := PR{Head: "deadbeef", URL: "https://example/pr/1"}
	behind(Result{Key: "FCIA-6"}, "FCIA-6", []string{"FCIA-6"}, pr, "orion/fcia-6",
		cfg, Options{Out: &buf}, Deps{Now: time.Now, Jira: newTracker()}, ws, log, &buf)

	if _, ok := loadRequests(ws.Dir).Waiting["FCIA-6"]; ok {
		t.Error("a branch with no determinable base entered the landing queue")
	}
}

// The escape hatch (auto_rebase: false) must not put a branch in a queue for
// a turn Orion will never act on.
func TestAutoRebaseDisabledDoesNotEnterTheQueue(t *testing.T) {
	home, source, _, _, sha := staleTicket(t)
	if err := os.WriteFile(filepath.Join(source, "orion.json"),
		[]byte(`{"collect":{"auto_rebase":false}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	entry, err := registry.Lookup(home, "FCIA-6")
	if err != nil {
		t.Fatal(err)
	}
	ws, err := workspace.Open(entry.Workspace)
	if err != nil {
		t.Fatal(err)
	}
	jira := newTracker()

	run(t, home, jira, PR{
		Verdict: VerdictPassing, Head: sha, URL: "https://example/pr/1",
	}, Options{})

	if _, ok := loadRequests(ws.Dir).Waiting["FCIA-6"]; ok {
		t.Error("a branch entered the queue although auto_rebase is off")
	}
}

// Without the forge's commit there is no lease to push under (OR-206's
// rebase.go), and no turn to give the branch either.
func TestABranchWithNoHeadReportedDoesNotEnterTheQueue(t *testing.T) {
	home, _, _, _, _ := staleTicket(t)
	entry, err := registry.Lookup(home, "FCIA-6")
	if err != nil {
		t.Fatal(err)
	}
	ws, err := workspace.Open(entry.Workspace)
	if err != nil {
		t.Fatal(err)
	}
	jira := newTracker()

	run(t, home, jira, PR{
		Verdict: VerdictPassing, Head: "", URL: "https://example/pr/1",
	}, Options{})

	if _, ok := loadRequests(ws.Dir).Waiting["FCIA-6"]; ok {
		t.Error("a branch with no reported HEAD entered the landing queue")
	}
}

// Two branches recorded in the same instant must still elect one leader, not
// alternate between them every pass -- ties break on the key.
func TestLeaderTiesBreakOnKeyOrder(t *testing.T) {
	now := time.Now()
	f := emptyRequests()
	f.Waiting["FCIA-7"] = now
	f.Waiting["FCIA-6"] = now

	if got := leader(f, []string{"FCIA-7", "FCIA-6"}); got != "FCIA-6" {
		t.Errorf("leader = %q, want FCIA-6 (lower key wins an exact tie)", got)
	}
}

// A queue entry left behind by a ticket nobody is polling this pass must not
// be able to hold the queue shut for tickets that ARE being polled.
func TestAQueueEntryNotInThisPassDoesNotBlockThePass(t *testing.T) {
	home, _, origin, _, sha := staleTicket(t)
	entry, err := registry.Lookup(home, "FCIA-6")
	if err != nil {
		t.Fatal(err)
	}
	ws, err := workspace.Open(entry.Workspace)
	if err != nil {
		t.Fatal(err)
	}
	// Far more senior than FCIA-6 could ever be, and not part of this pass.
	seedWaiting(t, ws, "FCIA-9", time.Now().Add(-24*time.Hour))
	jira := newTracker()

	_, out, _ := run(t, home, jira, PR{
		Verdict: VerdictPassing, Head: sha, URL: "https://example/pr/1",
	}, Options{})

	if got := head(t, origin, "refs/heads/orion/fcia-6"); got == sha {
		t.Errorf("FCIA-6 held for a ticket not being polled this pass:\n%s", out)
	}
}

// A corrupt or unwritable queue file must degrade the write, not the pass:
// the cost of a lost place in the queue is one branch competing on equal
// terms next time, which is a far smaller fault than a crash.
func TestWritingTheQueueFileFailsGracefully(t *testing.T) {
	dir := t.TempDir()
	// .orion exists as a plain FILE, so the directory writeRequests needs to
	// create for the queue file can never come into being.
	if err := os.WriteFile(filepath.Join(dir, ".orion"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	f := emptyRequests()
	f.Waiting["FCIA-6"] = time.Now()
	if err := writeRequests(dir, f); err == nil {
		t.Fatal("expected an error when the queue file's directory cannot be created")
	}
}

// The same failure, through a whole pass: the branch is still handed to a
// person, in words, rather than the collector crashing on a queue it could
// not record a place in.
func TestAPassSurvivesAQueueFileItCannotWrite(t *testing.T) {
	home, _, _, _, sha := staleTicket(t)
	entry, err := registry.Lookup(home, "FCIA-6")
	if err != nil {
		t.Fatal(err)
	}
	ws, err := workspace.Open(entry.Workspace)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(ws.Dir, ".orion")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws.Dir, ".orion"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	jira := newTracker()

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("the pass panicked on an unwritable queue file rather than degrading: %v", r)
		}
	}()
	run(t, home, jira, PR{
		Verdict: VerdictPassing, Head: sha, URL: "https://example/pr/1",
	}, Options{})
}

// A dry run reports verdicts; it must not be the mechanism by which a branch
// quietly gains or loses its place in the queue.
func TestADryRunPassDoesNotModifyQueueState(t *testing.T) {
	home, ws, origin, heads := twoStaleBranches(t)
	before := loadRequests(ws.Dir)

	var buf bytes.Buffer
	Run(Options{Keys: []string{"FCIA-6", "FCIA-7"}, Home: home, Out: &buf, DryRun: true}, Deps{
		Jira: newTracker(),
		Status: func(_, branch string) (PR, error) {
			for key, pr := range passing(heads) {
				if branch == "orion/"+strings.ToLower(key) {
					return pr, nil
				}
			}
			return PR{Verdict: VerdictUnknown}, nil
		},
		Refresh: func(string, string) (string, error) { return "", nil },
		Prune:   func(*workspace.Workspace, string) error { return nil },
	})

	after := loadRequests(ws.Dir)
	if len(after.Waiting) != len(before.Waiting) {
		t.Errorf("a dry run changed the queue: before %v, after %v", before.Waiting, after.Waiting)
	}
	if got := moved(t, origin, heads); len(got) != 0 {
		t.Errorf("a dry run pushed: %v", got)
	}
}

// The turn a branch won is not a debt it keeps once it lands: a merged branch
// must not still be holding a queue place behind branches waiting for a turn
// that ticket will never take again.
func TestAMergedBranchIsRemovedFromTheQueue(t *testing.T) {
	home, ws, _, sha := upToDateTicket(t)
	seedWaiting(t, ws, "FCIA-6", time.Now().Add(-time.Hour))
	jira := newTracker()

	run(t, home, jira, PR{
		Verdict: VerdictMerged, Head: sha, BaseRef: "develop", URL: "https://example/pr/1",
	}, Options{})

	if _, ok := loadRequests(ws.Dir).Waiting["FCIA-6"]; ok {
		t.Error("a merged branch kept its place in the landing queue")
	}
}

// With require_up_to_date off, staleness is never checked at all, so a
// behind branch has no business taking a place in a queue that exists only
// to order rebases require_up_to_date makes necessary.
func TestRequireUpToDateOffDoesNotQueueABehindBranch(t *testing.T) {
	home, source, _, _, sha := staleTicket(t)
	if err := os.WriteFile(filepath.Join(source, "orion.json"),
		[]byte(`{"ci":{"require_up_to_date":false}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	entry, err := registry.Lookup(home, "FCIA-6")
	if err != nil {
		t.Fatal(err)
	}
	ws, err := workspace.Open(entry.Workspace)
	if err != nil {
		t.Fatal(err)
	}
	jira := newTracker()

	run(t, home, jira, PR{
		Verdict: VerdictPassing, Head: sha, URL: "https://example/pr/1",
	}, Options{})

	if _, ok := loadRequests(ws.Dir).Waiting["FCIA-6"]; ok {
		t.Error("a behind branch entered the queue with require_up_to_date off")
	}
}

// Seniority is set once, the first time joinQueue sees the ticket, and must
// survive being asked about again -- that is the entire difference between a
// queue and the arbitrary re-examination order that starved OR-206's two
// longest-open branches.
func TestJoinQueueKeepsTheTimestampItAlreadyHad(t *testing.T) {
	f := emptyRequests()
	first := time.Now().Add(-time.Hour)
	f.Waiting["FCIA-7"] = first

	if changed := joinQueue(f, "FCIA-7", time.Now()); changed {
		t.Error("joinQueue reported a change for a ticket already holding a place")
	}
	if got := f.Waiting["FCIA-7"]; !got.Equal(first) {
		t.Errorf("seniority was reset: got %v, want %v", got, first)
	}
}

// The same property across a real pass: a branch that is still behind on its
// second look keeps the timestamp its first pass recorded.
func TestAHoldingBranchKeepsItsSeniorityAcrossPasses(t *testing.T) {
	home, ws, _, heads := twoStaleBranches(t)
	// FCIA-7 is senior, so FCIA-6 holds and is never touched by a rebase --
	// its own queue timestamp is the one this test can trust across passes.
	seedWaiting(t, ws, "FCIA-7", time.Now().Add(-time.Hour))
	jira := newTracker()

	pass(t, home, jira, passing(heads), []string{"FCIA-6", "FCIA-7"})
	first, ok := loadRequests(ws.Dir).Waiting["FCIA-6"]
	if !ok {
		t.Fatal("FCIA-6 should have joined the queue on the first pass")
	}

	pass(t, home, jira, passing(heads), []string{"FCIA-6", "FCIA-7"})
	second, ok := loadRequests(ws.Dir).Waiting["FCIA-6"]
	if !ok {
		t.Fatal("FCIA-6 left the queue despite still being behind")
	}

	if !second.Equal(first) {
		t.Errorf("re-examining the holding branch changed its seniority: %v -> %v", first, second)
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
