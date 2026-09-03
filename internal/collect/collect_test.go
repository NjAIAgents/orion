package collect

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/orion-sdlc/orion/internal/registry"
	"github.com/orion-sdlc/orion/internal/tracker"
	"github.com/orion-sdlc/orion/internal/workspace"
)

// fakeTracker records what was done rather than asserting inside itself, so
// each test states its own expectation in its own terms.
type fakeTracker struct {
	issues []tracker.Issue
	// children maps a parent key to its sub-tasks. Empty by default, so
	// every existing test describes a flat ticket exactly as before.
	children    map[string][]tracker.Issue
	childErr    error
	added       map[string][]string
	removed     map[string][]string
	transitions map[string]string
	comments    map[string][]string
	searchErr   error
	labelErr    error
	commentErr  error
	// transitionErr is the workflow with no Done transition -- the tracker
	// that cannot close a ticket however correct the request is. A merge must
	// survive it (OR-314).
	transitionErr error
}

func newTracker() *fakeTracker {
	return &fakeTracker{
		added: map[string][]string{}, removed: map[string][]string{},
		transitions: map[string]string{}, comments: map[string][]string{},
		children: map[string][]tracker.Issue{},
	}
}

func (f *fakeTracker) Search(string, int) ([]tracker.Issue, error) {
	return f.issues, f.searchErr
}
func (f *fakeTracker) Children(key string) ([]tracker.Issue, error) {
	return f.children[key], f.childErr
}
func (f *fakeTracker) SetLabels(key string, add, remove []string) error {
	if f.labelErr != nil {
		return f.labelErr
	}
	f.added[key] = append(f.added[key], add...)
	f.removed[key] = append(f.removed[key], remove...)
	return nil
}
func (f *fakeTracker) TransitionTo(key, status string) error {
	if f.transitionErr != nil {
		return f.transitionErr
	}
	f.transitions[key] = status
	return nil
}
func (f *fakeTracker) Comment(key, text string) error {
	if f.commentErr != nil {
		return f.commentErr
	}
	f.comments[key] = append(f.comments[key], text)
	return nil
}

// bound builds a home with one registered project and a usable sandbox, so
// the code under test walks its real lookup path rather than a stub of it.
func bound(t *testing.T) (home, source string) {
	t.Helper()
	home = t.TempDir()
	t.Setenv("ORION_HOME", home)

	source = filepath.Join(t.TempDir(), "fcia")
	if err := os.MkdirAll(source, 0o755); err != nil {
		t.Fatal(err)
	}
	ws, err := workspace.New(workspace.NewOptions{Idea: "fcia"})
	if err != nil {
		t.Fatalf("creating workspace: %v", err)
	}
	if err := registry.Bind(home, registry.Entry{
		Key: "FCIA", Source: source, Workspace: ws.ID,
	}); err != nil {
		t.Fatalf("binding: %v", err)
	}
	return home, source
}

func run(t *testing.T, home string, jira *fakeTracker, pr PR, opts Options) ([]Result, string, *counters) {
	t.Helper()
	var buf bytes.Buffer
	c := &counters{}
	opts.Home = home
	opts.Out = &buf
	if len(opts.Keys) == 0 {
		opts.Keys = []string{"FCIA-6"}
	}
	res := Run(opts, Deps{
		Jira:   jira,
		Status: func(string, string) (PR, error) { return pr, nil },
		Refresh: func(src, branch string) (string, error) {
			c.refreshed++
			return "fetched and fast-forwarded " + branch, nil
		},
		Prune: func(*workspace.Workspace, string) error { c.pruned++; return nil },
	})
	return res, buf.String(), c
}

type counters struct{ refreshed, pruned int }

// Pending is the case this runs into on almost every poll, and the one where
// doing anything at all would be wrong. A collector that transitioned or
// relabelled while checks were still running would close tickets on evidence
// that had not arrived.
func TestPendingChecksChangeNothing(t *testing.T) {
	home, _ := bound(t)
	jira := newTracker()
	res, out, c := run(t, home, jira, PR{Verdict: VerdictPending, Detail: "2 running"}, Options{})

	if res[0].Changed {
		t.Error("a pending verdict must not change anything")
	}
	if len(jira.added) != 0 || len(jira.removed) != 0 || len(jira.transitions) != 0 {
		t.Errorf("tracker was written to while CI was still running: %+v", jira)
	}
	if c.refreshed != 0 || c.pruned != 0 {
		t.Error("nothing outside the tracker should move either")
	}
	if !strings.Contains(out, "still running") {
		t.Errorf("the user should be told why nothing happened, got: %s", out)
	}
}

// The rollup the verdict was decided from is CARRIED OUT, so the watcher can
// name the checks without asking the forge again (OR-310).
//
// This is the only thing that makes the per-check row free on an ordinary
// watch: drop Checks here and the display can only get them with a second
// `gh pr view` per redraw, which is the pull this design refuses.
func TestAPendingResultCarriesTheChecksItWasDecidedFrom(t *testing.T) {
	home, _ := bound(t)
	res, _, _ := run(t, home, newTracker(), PR{
		Verdict: VerdictPending, Detail: "1 running",
		Checks: []Check{
			{Name: "go (ubuntu)", State: CheckPassed},
			{Name: "go (windows)", State: CheckRunning},
		},
	}, Options{})

	if len(res[0].Checks) != 2 {
		t.Fatalf("the result must carry the rollup, got %+v", res[0].Checks)
	}
	if res[0].Checks[1].Name != "go (windows)" || res[0].Checks[1].State != CheckRunning {
		t.Errorf("the checks must arrive as read: %+v", res[0].Checks)
	}
}

// Result.Checks is a field on the struct, not something derived from the
// verdict -- so a failing read carries the rollup exactly as a pending one
// does (OR-310). The prior test only proved this for VerdictPending; a
// watcher naming the check that actually failed needs the same field
// populated on the verdict that ends the wait.
func TestResultCarriesChecksWhateverTheVerdict(t *testing.T) {
	home, _ := bound(t)
	res, _, _ := run(t, home, newTracker(), PR{
		Verdict: VerdictFailing, Detail: "1 failed",
		Checks: []Check{
			{Name: "go (ubuntu)", State: CheckPassed},
			{Name: "go (windows)", State: CheckFailed},
		},
	}, Options{})

	if len(res[0].Checks) != 2 {
		t.Fatalf("the Checks field must be populated regardless of verdict, got %+v", res[0].Checks)
	}
	if res[0].Checks[1].Name != "go (windows)" || res[0].Checks[1].State != CheckFailed {
		t.Errorf("the checks must arrive as read: %+v", res[0].Checks)
	}
}

// Green but unmerged must NOT merge. Approving is the one step in this
// pipeline that is deliberately a person's, and a tool that merges its own
// work has removed the review it exists to produce.
func TestPassingChecksWaitForAHuman(t *testing.T) {
	home, _ := bound(t)
	jira := newTracker()
	res, out, c := run(t, home, jira,
		PR{Verdict: VerdictPassing, URL: "https://example/pr/1", Detail: "3 passed"}, Options{})

	if res[0].Changed || len(jira.removed) != 0 || c.pruned != 0 {
		t.Error("passing checks alone must not close, prune or refresh anything")
	}
	if !strings.Contains(out, "waiting for you to merge") {
		t.Errorf("the message must name what is required of the reader, got: %s", out)
	}
}

// The full merged path: the tracker first, then the conveniences.
func TestAMergedPullRequestClosesRefreshesAndPrunes(t *testing.T) {
	home, _ := bound(t)
	jira := newTracker()
	res, _, c := run(t, home, jira,
		PR{Verdict: VerdictMerged, URL: "https://example/pr/1"}, Options{})

	if !res[0].Changed {
		t.Fatal("a merge must be recorded as a change")
	}
	// EVERY label Orion owns, not just the one that brought us here. A ticket
	// that failed earlier, was fixed and then merged kept orion-failed
	// forever -- so `orion queue` printed "failed" on the same line as its
	// status, "Done". Orion contradicting itself in one line is worse than
	// either state alone: the reader cannot tell which half to believe.
	removed := strings.Join(jira.removed["FCIA-6"], " ")
	for _, label := range []string{tracker.LabelCIWait, tracker.LabelWorking, tracker.LabelFailed} {
		if !strings.Contains(removed, label) {
			t.Errorf("%s survived the merge; the ticket is finished and nothing Orion "+
				"tracked about it is still true (removed: %v)", label, jira.removed["FCIA-6"])
		}
	}
	if jira.transitions["FCIA-6"] != "Done" {
		t.Errorf("transition = %q, want Done", jira.transitions["FCIA-6"])
	}
	if c.refreshed != 1 {
		t.Error("the user's own checkout must be fast-forwarded; nothing else ever does it")
	}
	if c.pruned != 1 {
		t.Error("a merged branch's worktree holds nothing the repository does not")
	}
}

// --no-prune exists for reviewing what actually shipped. It must not affect
// the tracker: keeping a checkout is not a reason to leave a ticket open.
func TestNoPruneKeepsTheWorktreeAndStillClosesTheTicket(t *testing.T) {
	home, _ := bound(t)
	jira := newTracker()
	_, _, c := run(t, home, jira,
		PR{Verdict: VerdictMerged, URL: "u"}, Options{NoPrune: true})

	if c.pruned != 0 {
		t.Error("--no-prune must keep the worktree")
	}
	if jira.transitions["FCIA-6"] != "Done" {
		t.Error("--no-prune must not stop the ticket from closing")
	}
}

// A failing build marks the ticket failed rather than re-queueing it. The
// branch already has commits, so a fresh run would cut a second branch for
// the same ticket and compete with the first.
func TestFailingChecksMarkFailedRatherThanRequeue(t *testing.T) {
	home, _ := bound(t)
	jira := newTracker()
	res, _, c := run(t, home, jira,
		PR{Verdict: VerdictFailing, URL: "u", Detail: "test (failure)"}, Options{})

	if res[0].Verdict != VerdictFailing || !res[0].Changed {
		t.Fatalf("unexpected result: %+v", res[0])
	}
	if got := jira.added["FCIA-6"]; len(got) != 1 || got[0] != tracker.LabelFailed {
		t.Errorf("added labels = %v, want only %s", got, tracker.LabelFailed)
	}
	for _, l := range jira.added["FCIA-6"] {
		if l == "ORION" {
			t.Fatal("a failing branch must never be re-queued automatically")
		}
	}
	if c.pruned != 0 {
		t.Error("a failing branch's worktree is the evidence; it must be kept")
	}
	if !strings.Contains(strings.Join(jira.comments["FCIA-6"], " "), "test (failure)") {
		t.Error("the comment must name which check failed, not merely that CI failed")
	}
}

// A retried ticket's branch carries a suffix workspace.uniqueBranch chose to
// keep it off a prior attempt's still-open pull request. Collect must read
// that recorded branch rather than recomputing orion/<key> by convention --
// the recomputed name is the one attempt this ticket did NOT use (OR-173).
func TestCollectReadsTheRecordedBranchNotTheGuessedOne(t *testing.T) {
	home, _ := bound(t)
	entry, err := registry.Lookup(home, "FCIA-6")
	if err != nil {
		t.Fatal(err)
	}
	ws, err := workspace.Open(entry.Workspace)
	if err != nil {
		t.Fatal(err)
	}
	if err := workspace.RecordBranch(ws, "FCIA-6", "orion/fcia-6-2"); err != nil {
		t.Fatal(err)
	}

	jira := newTracker()
	var buf bytes.Buffer
	var gotBranch string
	Run(Options{Keys: []string{"FCIA-6"}, Home: home, Out: &buf}, Deps{
		Jira: jira,
		Status: func(dir, branch string) (PR, error) {
			gotBranch = branch
			return PR{Verdict: VerdictPending}, nil
		},
	})

	if gotBranch != "orion/fcia-6-2" {
		t.Errorf("looked up branch %q, want the recorded orion/fcia-6-2 -- "+
			"orion/fcia-6 is the FIRST attempt's name, not this ticket's", gotBranch)
	}
}

// The no-pull-request case used to fall through to a bare warning and leave
// ci-wait in place, so a ticket in that state was polled again on the next
// tick, and every tick after that, forever. It must terminate instead: out
// of ci-wait and marked for a human (OR-173).
func TestNoPullRequestReleasesFromCIWaitInsteadOfPollingForever(t *testing.T) {
	home, _ := bound(t)
	jira := newTracker()
	res, out, _ := run(t, home, jira,
		PR{Verdict: VerdictUnknown, Detail: "no pull requests found"}, Options{})

	if res[0].Verdict != VerdictUnknown || !res[0].Changed {
		t.Fatalf("unexpected result: %+v", res[0])
	}
	if got := jira.removed["FCIA-6"]; len(got) != 1 || got[0] != tracker.LabelCIWait {
		t.Errorf("must clear ci-wait so nothing polls it again: %v", got)
	}
	if got := jira.added["FCIA-6"]; len(got) != 1 || got[0] != tracker.LabelFailed {
		t.Errorf("must mark it for a human rather than leave it silently stuck: %v", got)
	}
	if !strings.Contains(out, "guess") {
		t.Errorf("must say the branch name searched was only a guess: %s", out)
	}
}

// When the branch searched was a RECORDED one, not a guess, the message must
// not say "guess" -- that caveat is only true for the convention fallback,
// and claiming it for a name the run actually used would send a human
// looking for a suffix that was never applied.
func TestNoPullRequestWithARecordedBranchDoesNotClaimItWasGuessed(t *testing.T) {
	home, _ := bound(t)
	entry, err := registry.Lookup(home, "FCIA-6")
	if err != nil {
		t.Fatal(err)
	}
	ws, err := workspace.Open(entry.Workspace)
	if err != nil {
		t.Fatal(err)
	}
	if err := workspace.RecordBranch(ws, "FCIA-6", "orion/fcia-6"); err != nil {
		t.Fatal(err)
	}

	jira := newTracker()
	res, out, _ := run(t, home, jira,
		PR{Verdict: VerdictUnknown, Detail: "no pull requests found"}, Options{})

	if res[0].Verdict != VerdictUnknown || !res[0].Changed {
		t.Fatalf("unexpected result: %+v", res[0])
	}
	if got := jira.removed["FCIA-6"]; len(got) != 1 || got[0] != tracker.LabelCIWait {
		t.Errorf("must clear ci-wait so nothing polls it again: %v", got)
	}
	if got := jira.added["FCIA-6"]; len(got) != 1 || got[0] != tracker.LabelFailed {
		t.Errorf("must mark it for a human rather than leave it silently stuck: %v", got)
	}
	if strings.Contains(out, "guess") {
		t.Errorf("a recorded branch is not a guess, but the output claims it is: %s", out)
	}
	if !strings.Contains(out, "orion/fcia-6") {
		t.Errorf("must still name the branch that was searched: %s", out)
	}
}

// Closed without merging is a human decision, not a fault to retry. Release
// it from the queue and leave the status for a person.
func TestAClosedPullRequestIsReleasedNotFailed(t *testing.T) {
	home, _ := bound(t)
	jira := newTracker()
	run(t, home, jira, PR{Verdict: VerdictClosed, URL: "u"}, Options{})

	if len(jira.added["FCIA-6"]) != 0 {
		t.Errorf("a deliberate close must not be labelled failed: %v", jira.added["FCIA-6"])
	}
	if got := jira.removed["FCIA-6"]; len(got) != 1 || got[0] != tracker.LabelCIWait {
		t.Errorf("it must still leave ci-wait so nothing polls it forever: %v", got)
	}
	if _, ok := jira.transitions["FCIA-6"]; ok {
		t.Error("the status is a person's to set here")
	}
}

// Dry run is what someone reaches for before trusting this on a timer. If it
// writes anything, it is worse than useless: it is a lie.
func TestDryRunTouchesNothing(t *testing.T) {
	for _, v := range []Verdict{VerdictMerged, VerdictFailing, VerdictClosed, VerdictUnknown} {
		home, _ := bound(t)
		jira := newTracker()
		_, out, c := run(t, home, jira, PR{Verdict: v, URL: "u"}, Options{DryRun: true})

		if len(jira.added) != 0 || len(jira.removed) != 0 || len(jira.transitions) != 0 {
			t.Errorf("%s: dry run wrote to the tracker: %+v", v, jira)
		}
		if c.refreshed != 0 || c.pruned != 0 {
			t.Errorf("%s: dry run touched the filesystem", v)
		}
		if !strings.Contains(out, "would") {
			t.Errorf("%s: dry run must say what it would do, got: %s", v, out)
		}
	}
}

// If the label cannot be cleared, the ticket will be picked up again on the
// next pass -- so the run must NOT go on to prune the worktree, or the
// second pass would find the evidence gone.
func TestAFailedRelabelStopsBeforeTheIrreversibleSteps(t *testing.T) {
	home, _ := bound(t)
	jira := newTracker()
	jira.labelErr = os.ErrPermission

	res, _, c := run(t, home, jira, PR{Verdict: VerdictMerged, URL: "u"}, Options{})

	if res[0].Err == nil {
		t.Fatal("the error must be reported, not swallowed")
	}
	if c.pruned != 0 {
		t.Error("nothing may be deleted once the tracker is known to be out of sync")
	}
}

// An empty registry must not produce an unscoped JQL. The first thing this
// does with a match is transition its status, so matching a label somebody
// applied by hand in an unrelated project would be destructive.
func TestNoRegisteredProjectsMeansNoSearch(t *testing.T) {
	home := t.TempDir()
	t.Setenv("ORION_HOME", home)
	jira := newTracker()
	var buf bytes.Buffer

	res := Run(Options{Home: home, Out: &buf}, Deps{Jira: jira})

	if len(res) != 0 {
		t.Errorf("expected no work, got %+v", res)
	}
	if !strings.Contains(buf.String(), "nothing is waiting") {
		t.Errorf("got: %s", buf.String())
	}
}

// bindTo registers a project, shared by the approval tests.
func bindTo(home, wsID, source string) error {
	return registry.Bind(home, registry.Entry{Key: "FCIA", Source: source, Workspace: wsID})
}

// OR-128: a line must appear before the Jira search, not only after it.
// `orion watch`'s very first network call of every tick is this one, and a
// person watching a freshly started run has no way to tell "about to check"
// from "hung" until something appears on the console.
func TestPrintsBeforeSearchingForWaitingTickets(t *testing.T) {
	home := t.TempDir()
	t.Setenv("ORION_HOME", home)
	if err := bindTo(home, "ws-1", t.TempDir()); err != nil {
		t.Fatal(err)
	}
	jira := newTracker()
	var buf bytes.Buffer

	Run(Options{Home: home, Out: &buf}, Deps{Jira: jira})

	out := buf.String()
	checkingAt := strings.Index(out, "checking for tickets awaiting CI")
	doneAt := strings.Index(out, "nothing is waiting on CI")
	if checkingAt < 0 {
		t.Fatalf("no pre-search line printed, got: %s", out)
	}
	if doneAt < 0 || checkingAt > doneAt {
		t.Errorf("the checking line must come before the result, got: %s", out)
	}
}

// OR-240: a watcher tick with nothing waiting on CI prints NOTHING.
//
// The pair OR-128 added above is right for a person who typed `orion
// collect`, and wrong once a minute forever. On a tick it stated that the
// system was idle -- "checking for tickets awaiting CI..." then "nothing is
// waiting on CI." -- while two agents were working hard on two tickets, which
// is worse than silence: it is a false claim about what the system is doing.
func TestAnUnattendedPassWithNothingWaitingIsSilent(t *testing.T) {
	home := t.TempDir()
	t.Setenv("ORION_HOME", home)
	if err := bindTo(home, "ws-1", t.TempDir()); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer

	res := Run(Options{Home: home, Out: &buf, Unattended: true}, Deps{Jira: newTracker()})

	if len(res) != 0 {
		t.Errorf("expected no work, got %+v", res)
	}
	if buf.Len() != 0 {
		t.Errorf("a tick with nothing waiting must print nothing at all, got: %q", buf.String())
	}
}
