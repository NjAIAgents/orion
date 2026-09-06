package supervisor

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/orion-sdlc/orion/internal/config"
	"github.com/orion-sdlc/orion/internal/workspace"
)

type fakePublisher struct {
	key, text string
	err       error
	calls     int
}

func (f *fakePublisher) Comment(key, text string) error {
	f.calls++
	if f.err != nil {
		return f.err
	}
	f.key, f.text = key, text
	return nil
}

// intentWS builds a workspace whose task is bound to a tracker project and
// whose repo holds an intent artifact.
func intentWS(t *testing.T, body string) (*workspace.Workspace, config.Config) {
	t.Helper()
	w := ws(t, `{}`)
	cfg := config.Load(w.RepoDir())

	w.Task.Slug = "cloudlens"
	w.Task.Tracker = json.RawMessage(`{"provider":"jira","key":"CLOUDLEN","name":"CloudLens"}`)

	dir := filepath.Join(w.RepoDir(), cfg.Paths.Intent)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if body != "" {
		if err := os.WriteFile(filepath.Join(dir, "cloudlens.md"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return w, cfg
}

// The answers given to `orion new` drive every later stage, and they lived
// only in a repository under ~/.orion where nobody looks. Publishing puts them
// where the person who typed them can read them.
func TestTheIntentIsPublishedToTheTrackerProject(t *testing.T) {
	w, cfg := intentWS(t, "# CloudLens\n\nKeep an eye on AWS cost.\n")
	p := &fakePublisher{}

	msg := publishIntent(p, w, cfg, "intent")

	if p.calls != 1 {
		t.Fatalf("commented %d times, want 1", p.calls)
	}
	if p.key != "CLOUDLEN" {
		t.Errorf("posted to %q, want the bound project key", p.key)
	}
	if !strings.Contains(p.text, "Keep an eye on AWS cost") {
		t.Errorf("the comment does not carry the intent:\n%s", p.text)
	}
	// The artifact stays the authority; the comment must say so, or someone
	// edits the copy and the stages never see it.
	if !strings.Contains(p.text, "edit it there, not here") {
		t.Errorf("the comment does not name the artifact as authoritative:\n%s", p.text)
	}
	if !strings.Contains(msg, "CLOUDLEN") {
		t.Errorf("the reported outcome does not name the project: %q", msg)
	}
}

// Only the intent stage. A spec or a plan posted as a comment would be noise,
// and the intent is the one artifact that records what the OPERATOR said.
func TestOnlyTheIntentStagePublishes(t *testing.T) {
	w, cfg := intentWS(t, "# CloudLens\n\nbody\n")
	for _, stage := range []string{"spec", "plan", "scaffold", "decompose"} {
		p := &fakePublisher{}
		if msg := publishIntent(p, w, cfg, stage); msg != "" {
			t.Errorf("%s stage published: %q", stage, msg)
		}
		if p.calls != 0 {
			t.Errorf("%s stage posted a comment", stage)
		}
	}
}

// BEST EFFORT. The artifact is committed by the time this runs, so failing
// the stage because a comment did not post would trade the work for the
// receipt.
func TestAFailedPublishIsReportedNotFatal(t *testing.T) {
	w, cfg := intentWS(t, "# CloudLens\n\nbody\n")
	p := &fakePublisher{err: fmt.Errorf("403 forbidden")}

	msg := publishIntent(p, w, cfg, "intent")

	if !strings.HasPrefix(msg, "could not") {
		t.Errorf("a failed publish must be reported as such, got %q", msg)
	}
	if !strings.Contains(msg, "CLOUDLEN") {
		t.Errorf("the failure does not name the project it could not reach: %q", msg)
	}
}

// A workspace with no tracker binding is a supported way to run Orion, and
// must not produce a warning about something nobody asked for.
func TestNoTrackerBindingPublishesNothingQuietly(t *testing.T) {
	w, cfg := intentWS(t, "# CloudLens\n\nbody\n")
	w.Task.Tracker = nil
	p := &fakePublisher{}

	if msg := publishIntent(p, w, cfg, "intent"); msg != "" {
		t.Errorf("an unbound workspace said something: %q", msg)
	}
	if p.calls != 0 {
		t.Error("an unbound workspace posted a comment")
	}
}

// No tracker configured at all takes the same quiet path.
func TestANilTrackerPublishesNothing(t *testing.T) {
	w, cfg := intentWS(t, "# CloudLens\n\nbody\n")
	if msg := publishIntent(nil, w, cfg, "intent"); msg != "" {
		t.Errorf("a nil tracker said something: %q", msg)
	}
}

// A very long intent is truncated with a pointer to the real thing rather
// than posted whole or silently cut.
func TestALongIntentSaysItWasTruncated(t *testing.T) {
	w, cfg := intentWS(t, strings.Repeat("x", maxIntentComment+500))
	p := &fakePublisher{}

	publishIntent(p, w, cfg, "intent")

	if !strings.Contains(p.text, "truncated") {
		t.Error("a truncated comment does not say it was truncated")
	}
	if !strings.Contains(p.text, "committed artifact") {
		t.Error("the truncation does not point at the full version")
	}
}
