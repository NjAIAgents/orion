package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/orion-sdlc/orion/internal/slack"
	"github.com/orion-sdlc/orion/internal/workspace"
)

// OR-460. createProjectChannel used to gate on cfg.Slack.Enabled, which
// nothing in the plan chain ever sets true -- so a project scaffolded via
// orion new/orion plan never got a channel, silently, no matter what
// credentials were configured. The fix checks live Slack availability
// instead (matching orion init's provisionRemote) and only still declines
// on create_channel_per_project being explicitly false.
//
// This test cannot drive a real Slack API call (createProjectChannel builds
// its own *slack.Client via slack.FromEnv() rather than accepting one), so it
// exercises the part that was actually broken: with orion.json carrying no
// "slack" block at all -- exactly the shape found live on continuity -- the
// function must still REACH the credential check rather than bailing out on
// cfg.Slack.Enabled before ever asking. Forcing the resolver empty makes
// slack.FromEnv() fail predictably; the assertion is on stderr, which must
// now name the real reason (no token) rather than staying silent the way the
// old Enabled-gate path did with no config at all.
//
// The resolver is left at this test's value rather than restored: it is
// already the package's zero-state default (no token), and no other test in
// this package touches it.
func TestCreateProjectChannelReachesTheCredentialCheckWithNoSlackBlockAtAll(t *testing.T) {
	slack.SetResolver(func() string { return "" })

	dir := t.TempDir()
	writeOrionJSON(t, dir, `{"version":1}`) // no "slack" key at all, matching a real project

	ws := &workspace.Workspace{ID: "test", Dir: dir}
	ws.RepoPath = dir
	ws.Task.Slug = "test-project"

	stderr := captureStderr(t, func() {
		if ch := createProjectChannel(ws); ch != nil {
			t.Errorf("expected nil (no token configured), got %+v", ch)
		}
	})

	if strings.Contains(stderr, "slack enabled but not usable") {
		t.Error("stderr still uses the old wording that implied Enabled gates this")
	}
	if !strings.Contains(stderr, "ORION_SLACK_TOKEN") {
		t.Errorf("declining with no credentials must say why, got: %q", stderr)
	}
}

// The one case that must still decline: an operator who explicitly turned
// channel creation off for this project. Credentials or not, that choice is
// honoured.
func TestCreateProjectChannelDeclinesWhenCreateChannelPerProjectIsExplicitlyFalse(t *testing.T) {
	dir := t.TempDir()
	writeOrionJSON(t, dir, `{"version":1,"slack":{"create_channel_per_project":false}}`)

	ws := &workspace.Workspace{ID: "test", Dir: dir}
	ws.RepoPath = dir
	ws.Task.Slug = "test-project"

	stdout := captureStdout(t, func() {
		if ch := createProjectChannel(ws); ch != nil {
			t.Errorf("expected nil, got %+v", ch)
		}
	})
	if !strings.Contains(stdout, "create_channel_per_project is off") {
		t.Errorf("declining an explicit opt-out must say so, got: %q", stdout)
	}
}

func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	orig := os.Stderr
	os.Stderr = w
	defer func() { os.Stderr = orig }()

	fn()

	_ = w.Close()
	buf := make([]byte, 8192)
	n, _ := r.Read(buf)
	return string(buf[:n])
}

func writeOrionJSON(t *testing.T, dir, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "orion.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}
