package supervisor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The unit tests in internal/discovery and the assessIntent helper in this
// package both exercise discovery.Assess directly, on a path the test
// builds by hand. Neither one goes through Run's own wiring at
// supervisor.go: cfg.Paths.Intent joined with ws.Task.Slug, gated only for
// stages where stageNeedsIntent is true. A bug in that join -- the wrong
// separator, the wrong config field, gating the wrong stage -- would pass
// every existing test and still let a real run design from an unanswered
// question. These two tests close that gap by driving the gate through
// Run() itself, on the exact path a real ticket would use.

// fakeClaudeThatFinishes puts a `claude` on PATH that emits one valid
// stream-json result frame and touches a canary file, so Run can be driven
// all the way to a clean exit rather than stopping at "never started".
func fakeClaudeThatFinishes(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	canary := filepath.Join(dir, "was-launched")
	script := "#!/bin/sh\ntouch " + shPath(canary) + "\n" +
		`echo '{"type":"result","session_id":"abc","result":"done","total_cost_usd":0.1,"is_error":false}'` + "\n"
	writeFakeBinIn(t, dir, "claude", script)
	return canary
}

func writeIntentCapture(t *testing.T, w interface{ RepoDir() string }, body string) {
	t.Helper()
	dir := filepath.Join(w.RepoDir(), "docs", "intent")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "thing.md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// A stage that designs from intent must never launch the agent while the
// capture still has an unanswered question -- not "should report an error"
// in isolation, but the actual side effect that matters: the CLI never runs.
func TestRunBlocksAtTheDiscoveryGateWithOpenQuestions(t *testing.T) {
	w := ws(t, "")
	writeIntentCapture(t, w, "# Intent\n\n## Open questions\n- Do adjusters need access?\n")
	canary := fakeClaude(t)

	res, err := Run(w, Options{Stage: "spec", Prompt: "do a thing", MaxMinutes: 1, MaxTurns: 1})
	if err == nil {
		t.Fatal("Run proceeded past an intent capture with an open question")
	}
	if !strings.Contains(err.Error(), "discovery gate") {
		t.Errorf("error should be the discovery gate message, got: %v", err)
	}
	if res == nil || !strings.Contains(res.Reason, "discovery gate") {
		t.Errorf("result = %+v", res)
	}
	if _, statErr := os.Stat(canary); statErr == nil {
		t.Fatal("the agent was launched despite an open question in the intent capture")
	}
}

// The flip side: once every question is settled the same stage must proceed
// through the same code path, on the same file layout.
func TestRunProceedsPastTheDiscoveryGateWhenQuestionsAreSettled(t *testing.T) {
	w := ws(t, "")
	writeIntentCapture(t, w, "# Intent\n\n## Open questions\n- [x] Do adjusters need access?\n")
	canary := fakeClaudeThatFinishes(t)

	if _, err := Run(w, Options{Stage: "spec", Prompt: "do a thing", MaxMinutes: 1, MaxTurns: 1}); err != nil {
		t.Fatalf("Run stopped at the discovery gate with every question settled: %v", err)
	}
	if _, statErr := os.Stat(canary); statErr != nil {
		t.Fatal("the agent was never launched even though every question was settled")
	}
}

// A stage the gate does not cover -- intent itself -- must run even while
// its own capture (from a prior attempt) still has an open question, or a
// re-run could never reach the point of answering it.
func TestRunSkipsTheDiscoveryGateForTheIntentStageItself(t *testing.T) {
	w := ws(t, "")
	writeIntentCapture(t, w, "# Intent\n\n## Open questions\n- Do adjusters need access?\n")
	canary := fakeClaudeThatFinishes(t)

	if _, err := Run(w, Options{Stage: "intent", Prompt: "do a thing", MaxMinutes: 1, MaxTurns: 1}); err != nil {
		t.Fatalf("Run blocked the intent stage on its own in-progress capture: %v", err)
	}
	if _, statErr := os.Stat(canary); statErr != nil {
		t.Fatal("the intent stage itself must not be gated by discovery")
	}
}

func writeSpecFile(t *testing.T, w interface{ RepoDir() string }, body string) {
	t.Helper()
	dir := filepath.Join(w.RepoDir(), "specs")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "thing.spec.md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// A stage that designs from the spec is blocked by a [NEEDS CLARIFICATION]
// marker in it, with the intent clean -- the marker is spec-kit's spelling
// of an open question, and a plan built on one inherits the guess.
func TestRunBlocksAtTheGateOnASpecMarkerWithTheIntentClean(t *testing.T) {
	w := ws(t, "")
	w.Task.Slug = "thing"
	writeIntentCapture(t, w, "# Intent\n\n## Open questions\n- None\n")
	writeSpecFile(t, w, "# Spec\n\n- **FR-001**: auth via [NEEDS CLARIFICATION: SSO or password?]\n\n## Open questions\n- SSO or password?\n")
	canary := fakeClaude(t)

	_, err := Run(w, Options{Stage: "plan", Prompt: "do a thing", MaxMinutes: 1, MaxTurns: 1})
	if err == nil || !strings.Contains(err.Error(), "discovery gate") || !strings.Contains(err.Error(), "SSO or password") {
		t.Fatalf("plan was not blocked by the spec's marker, or the gate does not name it: %v", err)
	}
	if _, statErr := os.Stat(canary); statErr == nil {
		t.Fatal("the agent was launched despite a marker in the spec")
	}
}

// The spec stage itself is not blocked by markers in the spec: it is the
// stage that writes them, and a re-run is how they get resolved.
func TestTheSpecStageIsNotBlockedByItsOwnMarkers(t *testing.T) {
	w := ws(t, "")
	w.Task.Slug = "thing"
	writeIntentCapture(t, w, "# Intent\n\n## Open questions\n- None\n")
	writeSpecFile(t, w, "# Spec\n\n[NEEDS CLARIFICATION: anything]\n")
	canary := fakeClaudeThatFinishes(t)

	if _, err := Run(w, Options{Stage: "spec", Prompt: "do a thing", MaxMinutes: 1, MaxTurns: 1}); err != nil {
		t.Fatalf("the spec stage was blocked by its own marker: %v", err)
	}
	if _, statErr := os.Stat(canary); statErr != nil {
		t.Fatal("the agent was never launched")
	}
}
