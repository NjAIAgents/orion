package supervisor

import (
	"strings"
	"sync"
	"testing"

	"github.com/orion-sdlc/orion/internal/notify"
)

// captureDesktopNotify swaps in a recorder for notify's desktop channel,
// which fires unconditionally regardless of a workspace's Slack channel, and
// restores the real sender on cleanup. It is the only hook that hands back
// the whole Event -- the terminal echo (notify.Out) prints the title alone.
func captureDesktopNotify(t *testing.T) *[]notify.Event {
	t.Helper()
	var mu sync.Mutex
	var events []notify.Event
	prev := notify.SetDesktopSender(func(e notify.Event) error {
		mu.Lock()
		defer mu.Unlock()
		events = append(events, e)
		return nil
	})
	t.Cleanup(func() { notify.SetDesktopSender(prev) })
	return &events
}

// A dry run is a promise that nothing was asked to write anything, so the
// artifact gate -- which exists to catch a command that ran and produced
// nothing -- must not fire even though, as in every dry run, no artifact was
// ever written.
func TestDryRunBypassesArtifactCheck(t *testing.T) {
	w := gitWorkspace(t, "")
	fakeClaude(t)

	res, err := Run(w, Options{Stage: "spec", DryRun: true, MaxMinutes: 1, MaxTurns: 1})
	if err != nil {
		t.Fatalf("a dry run must not be held to the artifact it never tried to write: %v", err)
	}
	if res == nil || res.Reason != "dry run" {
		t.Errorf("result = %+v", res)
	}
	if w.Task.Status == "failed" {
		t.Errorf("a dry run must not fail the task, status = %q", w.Task.Status)
	}
}

// The task status flip is the record downstream commands read; a stage
// left no artifact must not be handed on as though it finished.
func TestTaskStatusUpdatedOnArtifactFailure(t *testing.T) {
	w := gitWorkspace(t, `{"toolkit": {"stages": {"spec": "/wrong-skill-name"}}}`)
	claudeWriting(t, w.RepoDir(), "true")

	if _, err := Run(w, Options{Stage: "spec", MaxMinutes: 1, MaxTurns: 1}); err == nil {
		t.Fatal("a stage that left no artifact must fail the run")
	}
	if w.Task.Status != "failed" {
		t.Errorf("Task.Status = %q, want %q", w.Task.Status, "failed")
	}
}

// The notification is the only place this reaches anyone who is not staring
// at the terminal, so it must carry the same three facts the returned error
// does: which artifact is missing, which stage, and what command ran.
func TestNotificationSentOnArtifactFailure(t *testing.T) {
	events := captureDesktopNotify(t)
	w := gitWorkspace(t, `{"toolkit": {"stages": {"spec": "/wrong-skill-name"}}}`)
	claudeWriting(t, w.RepoDir(), "true")

	if _, err := Run(w, Options{Stage: "spec", MaxMinutes: 1, MaxTurns: 1}); err == nil {
		t.Fatal("a stage that left no artifact must fail the run")
	}

	var blocked *notify.Event
	for i, e := range *events {
		if e.Level == notify.Blocked && strings.Contains(e.Title, "left no artifact") {
			blocked = &(*events)[i]
		}
	}
	if blocked == nil {
		t.Fatalf("no notify.Blocked event named the missing artifact, got: %+v", *events)
	}
	for _, want := range []string{"specs/thing.spec.md", "spec", "/wrong-skill-name"} {
		if !strings.Contains(blocked.Title, want) && !strings.Contains(blocked.Body, want) {
			t.Errorf("notification must name %q, got title=%q body=%q", want, blocked.Title, blocked.Body)
		}
	}
}

// A notification with no log path sends an operator chasing a failure with
// no way to read what actually happened.
func TestNotificationBodyIncludesLogPath(t *testing.T) {
	events := captureDesktopNotify(t)
	w := gitWorkspace(t, `{"toolkit": {"stages": {"spec": "/wrong-skill-name"}}}`)
	claudeWriting(t, w.RepoDir(), "true")

	res, err := Run(w, Options{Stage: "spec", MaxMinutes: 1, MaxTurns: 1})
	if err == nil {
		t.Fatal("a stage that left no artifact must fail the run")
	}
	if res == nil || res.LogPath == "" {
		t.Fatalf("result must carry a log path, got: %+v", res)
	}

	var blocked *notify.Event
	for i, e := range *events {
		if e.Level == notify.Blocked && strings.Contains(e.Title, "left no artifact") {
			blocked = &(*events)[i]
		}
	}
	if blocked == nil {
		t.Fatalf("no notify.Blocked event named the missing artifact, got: %+v", *events)
	}
	if !strings.Contains(blocked.Body, res.LogPath) {
		t.Errorf("notification body must include the log path %q, got: %q", res.LogPath, blocked.Body)
	}
}
