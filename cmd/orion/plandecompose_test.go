package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/orion-sdlc/orion/internal/decompose"
	"github.com/orion-sdlc/orion/internal/tracker"
	"github.com/orion-sdlc/orion/internal/workspace"
)

// fakeBackend is a tracker that remembers what it created, standing in for
// Jira so the chain's decompose step can run without a network.
type fakeBackend struct {
	have    map[string]string
	created []decompose.CreateRequest
	links   []string
	n       int
}

func (f *fakeBackend) Name() string { return "fake" }
func (f *fakeBackend) Existing(string, string) (map[string]string, error) {
	out := map[string]string{}
	for k, v := range f.have {
		out[k] = v
	}
	return out, nil
}
func (f *fakeBackend) Link(blocker, blocked string) error {
	f.links = append(f.links, blocker+">"+blocked)
	return nil
}

func (f *fakeBackend) Create(r decompose.CreateRequest) (string, error) {
	f.n++
	key := fmt.Sprintf("%s-%d", r.Project, f.n)
	f.have[r.Summary] = key
	f.created = append(f.created, r)
	return key, nil
}

func swapBackend(t *testing.T) *fakeBackend {
	t.Helper()
	f := &fakeBackend{have: map[string]string{}}
	orig := decomposeBackend
	decomposeBackend = func() (decompose.Backend, error) { return f, nil }
	t.Cleanup(func() { decomposeBackend = orig })
	return f
}

// fakeRelease records the version and fix-version writes.
type fakeRelease struct {
	versions map[string]tracker.Version
	attached map[string]string
	creates  int
}

func (f *fakeRelease) FindVersion(_, name string) (tracker.Version, bool, error) {
	v, ok := f.versions[name]
	return v, ok, nil
}
func (f *fakeRelease) ListVersions(string) ([]tracker.Version, error) { return nil, nil }
func (f *fakeRelease) GetIssue(key string) (*tracker.Issue, error) {
	return &tracker.Issue{Key: key}, nil
}
func (f *fakeRelease) SetFixVersion(key, id string) error {
	f.attached[key] = id
	return nil
}
func (f *fakeRelease) CreateVersion(_, name, _ string) (tracker.Version, bool, error) {
	f.creates++
	v := tracker.Version{ID: "100", Name: name}
	f.versions[name] = v
	return v, true, nil
}

func swapRelease(t *testing.T) *fakeRelease {
	t.Helper()
	f := &fakeRelease{versions: map[string]tracker.Version{}, attached: map[string]string{}}
	orig := releaseTracker
	releaseTracker = func() (releaseAPI, error) { return f, nil }
	t.Cleanup(func() { releaseTracker = orig })
	return f
}

// boundWS is chainWS bound to a tracker project, with the plan stage's
// task list where the chain pinned it.
func boundWS(t *testing.T, withTasks bool) *workspace.Workspace {
	t.Helper()
	w := chainWS(t)
	w.Task.Tracker = json.RawMessage(`{"provider":"jira","key":"OR"}`)
	if withTasks {
		dir := filepath.Join(w.RepoDir(), "specs", "001-cloudlens")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		body := "# Tasks: Cloudlens\n\n## Phase 1: Setup\n\n- [ ] T001 Initialise the module in go.mod\n\n" +
			"## Phase 2: User Story 1 - Search (Priority: P1)\n\n- [ ] T002 [US1] Add the search handler in internal/search/handler.go\n"
		if err := os.WriteFile(filepath.Join(dir, "tasks.md"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return w
}

// With a task list the tree is created natively -- the stage runner never
// sees decompose -- and the queue label lands where the queue admits it.
func TestDecomposeIsNativeWhenTheTaskListExists(t *testing.T) {
	fakeRemote(t)
	fb := swapBackend(t)
	w := boundWS(t, true)
	var order []string
	var out bytes.Buffer

	done := runPlanChain(&out, w, okRun(&order), yes)

	if done != len(planStages) {
		t.Fatalf("completed %d of %d:\n%s", done, len(planStages), out.String())
	}
	for _, s := range order {
		if s == "decompose" {
			t.Errorf("the stage runner was asked for decompose; the native step should have run: %v", order)
		}
	}
	if len(fb.created) == 0 {
		t.Fatal("nothing was created in the tracker")
	}
	for _, r := range fb.created {
		queued := false
		for _, l := range r.Labels {
			if l == "ORION" {
				queued = true
			}
		}
		switch {
		case r.Kind == decompose.KindEpic && queued:
			t.Errorf("the epic carries the queue label: %v", r.Labels)
		case r.Kind == decompose.KindStory && !queued:
			t.Errorf("the story lacks the queue label: %v", r.Labels)
		case r.Kind == decompose.KindTask && r.ParentKind == decompose.KindEpic && !queued:
			t.Errorf("the epic-level task lacks the queue label: %v", r.Labels)
		case r.Kind == decompose.KindTask && r.ParentKind == decompose.KindStory && queued:
			t.Errorf("a story's task carries the queue label: %v", r.Labels)
		}
	}
	// And it is done now, so a resume skips it.
	if !decomposeDone(w) {
		t.Error("a created tree does not report done")
	}
}

// Without a task list the step declines and the supervised stage runs.
func TestDecomposeFallsBackToTheStageWithoutATaskList(t *testing.T) {
	fakeRemote(t)
	fb := swapBackend(t)
	w := boundWS(t, false)
	var order []string
	var out bytes.Buffer

	runPlanChain(&out, w, okRun(&order), yes)

	if !strings.Contains(strings.Join(order, ","), "decompose") {
		t.Errorf("the stage runner was never asked for decompose: %v", order)
	}
	if len(fb.created) != 0 {
		t.Errorf("the native route created %d items with no task list", len(fb.created))
	}
}

// A declined confirmation creates nothing and stops the chain, resumable.
func TestADeclinedTreeCreatesNothingAndStopsTheChain(t *testing.T) {
	fakeRemote(t)
	fb := swapBackend(t)
	w := boundWS(t, true)
	var order []string
	var out bytes.Buffer
	ask := func(p string) bool { return !strings.HasPrefix(p, "Create ") }

	runPlanChain(&out, w, okRun(&order), ask)

	if len(fb.created) != 0 {
		t.Errorf("%d items were created after a no", len(fb.created))
	}
	for _, s := range order {
		if s == "decompose" {
			t.Error("the supervised stage ran after the native step was declined")
		}
	}
	if !strings.Contains(out.String(), "nothing was created") || !strings.Contains(out.String(), "resume: orion plan OR") {
		t.Errorf("the stop must say nothing was created and how to resume:\n%s", out.String())
	}
}

// --release creates the version once and attaches every ticket in the tree.
func TestReleaseAttachesEveryTreeKey(t *testing.T) {
	fakeRemote(t)
	fb := swapBackend(t)
	fr := swapRelease(t)
	w := boundWS(t, true)
	w.Task.ReleaseVersion = "v1.0.0"
	var order []string
	var out bytes.Buffer

	done := runPlanChain(&out, w, okRun(&order), yes)

	if done != len(planStages) {
		t.Fatalf("completed %d of %d:\n%s", done, len(planStages), out.String())
	}
	if fr.creates != 1 {
		t.Errorf("CreateVersion called %d times, want 1", fr.creates)
	}
	if len(fr.attached) != len(fb.created) {
		t.Errorf("attached %d tickets, want every one of the %d created: %v", len(fr.attached), len(fb.created), fr.attached)
	}
	if w.Task.Released != "v1.0.0" || !releaseDone(w) {
		t.Errorf("the release is not recorded as done: released=%q", w.Task.Released)
	}
}

func TestReleaseIsSkippedWithoutTheFlag(t *testing.T) {
	fakeRemote(t)
	swapBackend(t)
	fr := swapRelease(t)
	w := boundWS(t, true)
	var order []string
	var out bytes.Buffer

	runPlanChain(&out, w, okRun(&order), yes)

	if fr.creates != 0 || len(fr.attached) != 0 {
		t.Error("the release step wrote to the tracker without --release")
	}
	// Nothing asked for is done before it starts: reported, never asked about.
	if !strings.Contains(out.String(), "= done") || !strings.Contains(out.String(), "release") {
		t.Errorf("the release step must be reported as done without --release:\n%s", out.String())
	}
}
