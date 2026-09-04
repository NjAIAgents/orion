package collect

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"github.com/orion-sdlc/orion/internal/config"
	"github.com/orion-sdlc/orion/internal/events"
	"github.com/orion-sdlc/orion/internal/tracker"
)

// Observed 2026-09-03: OR-297 failed macOS in every batch it joined, the row
// said "fix round 1 of 3", and no fix round ever ran -- the batch path never
// reached failing(). A convicted culprit takes the same road a per-branch red
// build takes (OR-322).
func TestABatchCulpritIsMarkedFailedCommentedAndSentToTheFixAgent(t *testing.T) {
	t.Setenv("ORION_HOME", t.TempDir())
	ws := newWorkspace(t, "culprit")
	log, err := events.Open(filepath.Join(ws.Dir, ".orion", "events.jsonl"), events.Event{})
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()

	jira := newTracker()
	fix := &fixSpy{pushed: true}
	var buf bytes.Buffer
	rememberBatchPR("https://forge/pull/406")
	rememberBatchDetail("go (macos-latest): FAIL internal/njagents")
	t.Cleanup(func() { rememberBatchDetail("") })

	cfg := config.Config{}
	cfg.CI.AutoFix = true
	res := failCulprit(Result{Key: "OR-297", Changed: true},
		Member{Key: "OR-297", Branch: "orion/or-297"}, "orion/batch", cfg, Options{},
		Deps{Jira: jira, Fix: fix.fix}, ws, log, &buf)

	if fix.calls != 1 {
		t.Fatalf("the fix agent was dispatched %d times, want once", fix.calls)
	}
	if !strings.Contains(fix.sawAll[0], "internal/njagents") {
		t.Errorf("the agent was not given the actual failure: %q", fix.sawAll[0])
	}
	if res.Verdict != VerdictPending {
		t.Errorf("verdict = %s, want pending after a fix was pushed", res.Verdict)
	}
}

// Without auto_fix the culprit is still marked failed and told why, so it
// leaves the ready set and a person can see where it broke.
func TestABatchCulpritWithoutAutoFixIsMarkedFailedAndToldWhy(t *testing.T) {
	t.Setenv("ORION_HOME", t.TempDir())
	ws := newWorkspace(t, "culprit")
	log, err := events.Open(filepath.Join(ws.Dir, ".orion", "events.jsonl"), events.Event{})
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()

	jira := newTracker()
	var buf bytes.Buffer
	rememberBatchPR("https://forge/pull/406")
	rememberBatchDetail("go (macos-latest): FAIL internal/njagents")
	t.Cleanup(func() { rememberBatchDetail("") })

	res := failCulprit(Result{Key: "OR-297", Changed: true},
		Member{Key: "OR-297", Branch: "orion/or-297"}, "orion/batch", config.Config{}, Options{},
		Deps{Jira: jira}, ws, log, &buf)

	if res.Verdict != VerdictFailing {
		t.Errorf("verdict = %s, want failing", res.Verdict)
	}
	if !hasLabel(jira.added["OR-297"], tracker.LabelFailed) {
		t.Errorf("the culprit must carry %s so the next pass does not batch it again; added=%v",
			tracker.LabelFailed, jira.added["OR-297"])
	}
	comments := strings.Join(jira.comments["OR-297"], "\n")
	if !strings.Contains(comments, "pull/406") || !strings.Contains(comments, "internal/njagents") {
		t.Errorf("the comment must name the batch pull request and the failure:\n%s", comments)
	}
}

// The fix run is told WHERE the failure happened (OR-336).
//
// A culprit failed on the batch ref, tested alongside its siblings -- its own
// branch's last CI run is stale, green, or absent. The fix agent looks the log
// up by ref, so pointing it at the member's branch found nothing and it was
// handed the conviction sentence with no log at all. Observed twice on OR-299
// on 2026-09-04, the second time reporting "cannot see the actual CI log",
// which was exactly true: two attempts spent on a one-line signature mismatch
// that took a minute to find once the log was read.
func TestACulpritsFixRunIsPointedAtTheRefThatActuallyFailed(t *testing.T) {
	t.Setenv("ORION_HOME", t.TempDir())
	ws := newWorkspace(t, "culprit-ref")
	log, err := events.Open(filepath.Join(ws.Dir, ".orion", "events.jsonl"), events.Event{})
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()

	fix := &fixSpy{pushed: true}
	var buf bytes.Buffer
	rememberBatchPR("https://forge/pull/406")
	rememberBatchDetail("go (ubuntu-latest): FAIL internal/supervisor")
	t.Cleanup(func() { rememberBatchDetail("") })

	cfg := config.Config{}
	cfg.CI.AutoFix = true
	failCulprit(Result{Key: "OR-299", Changed: true},
		Member{Key: "OR-299", Branch: "orion/or-299"}, "orion/batch", cfg, Options{},
		Deps{Jira: newTracker(), Fix: fix.fix}, ws, log, &buf)

	if len(fix.sawRef) != 1 {
		t.Fatalf("the fix agent was dispatched %d times, want once", len(fix.sawRef))
	}
	if fix.sawRef[0] != "orion/batch" {
		t.Errorf("the fix run was told to read the log from %q; it must be the ref that "+
			"went red, not the member's own branch", fix.sawRef[0])
	}
}
