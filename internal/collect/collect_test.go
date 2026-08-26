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
	issues      []tracker.Issue
	added       map[string][]string
	removed     map[string][]string
	transitions map[string]string
	comments    map[string][]string
	searchErr   error
	labelErr    error
}

func newTracker() *fakeTracker {
	return &fakeTracker{
		added: map[string][]string{}, removed: map[string][]string{},
		transitions: map[string]string{}, comments: map[string][]string{},
	}
}

func (f *fakeTracker) Search(string, int) ([]tracker.Issue, error) {
	return f.issues, f.searchErr
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
	f.transitions[key] = status
	return nil
}
func (f *fakeTracker) Comment(key, text string) error {
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
	if got := jira.removed["FCIA-6"]; len(got) != 1 || got[0] != tracker.LabelCIWait {
		t.Errorf("ci-wait label was not cleared: %v", got)
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
	for _, v := range []Verdict{VerdictMerged, VerdictFailing, VerdictClosed} {
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
