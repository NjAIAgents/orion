package adopt

import (
	"os/exec"
	"strings"
	"testing"
)

func TestDeriveJiraKey(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"fcia", "FCIA"},
		{"claim-status-self-service", "CSSS"},
		{"orion", "ORIO"},
		{"my_cool_app", "MCA"},
		{"2fa", "P2FA"}, // Jira keys must start with a letter
		{"", "ORION"},
	} {
		if got := DeriveJiraKey(tc.in); got != tc.want {
			t.Errorf("DeriveJiraKey(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// The patcher must not round-trip the JSON: orion.json carries _comment_*
// keys explaining each setting, and Go marshals map keys in sorted order,
// which would scatter every comment away from what it documents.
func TestSetBlockFieldPreservesTheRestOfTheFile(t *testing.T) {
	src := `{
  "_comment_gates": "why the gates exist",
  "gates": {
    "require_plan_before_edit": false
  },

  "tracker": {
    "enabled": false,
    "provider": "jira",
    "project_key": ""
  },

  "slack": {
    "enabled": false,
    "private": true
  }
}
`
	out, ok := SetBlockField(src, "tracker", "enabled", "true")
	if !ok {
		t.Fatal("expected a change")
	}
	if !strings.Contains(out, `"_comment_gates": "why the gates exist"`) {
		t.Error("a comment key was lost or moved")
	}
	if !strings.Contains(out, "\"tracker\": {\n    \"enabled\": true,") {
		t.Errorf("tracker.enabled not set:\n%s", out)
	}
	if !strings.Contains(out, "\"slack\": {\n    \"enabled\": false,") {
		t.Error("slack.enabled must not be touched when patching tracker")
	}

	out, ok = SetBlockField(out, "tracker", "project_key", `"FCIA"`)
	if !ok || !strings.Contains(out, `"project_key": "FCIA"`) {
		t.Errorf("project_key not set:\n%s", out)
	}
	// Idempotent: setting the same value again is not a change.
	if _, ok := SetBlockField(out, "tracker", "enabled", "true"); ok {
		t.Error("re-setting an identical value should report no change")
	}
	if _, ok := SetBlockField(src, "nosuch", "enabled", "true"); ok {
		t.Error("a missing block must not report a change")
	}
}

func gitRepo(t *testing.T, withCommit bool) string {
	t.Helper()
	d := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", d}, args...)...)
		cmd.Env = append(cmd.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@e",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@e")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q", "-b", "main")
	if withCommit {
		run("commit", "-q", "--allow-empty", "-m", "first")
	}
	return d
}

func currentBranch(t *testing.T, dir string) string {
	t.Helper()
	out, err := exec.Command("git", "-C", dir, "branch", "--show-current").Output()
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(out))
}

func TestEnsureWorkBranchCreatesAndSwitches(t *testing.T) {
	d := gitRepo(t, true)
	created, _, err := EnsureWorkBranch(d, "develop")
	if err != nil {
		t.Fatal(err)
	}
	if !created {
		t.Fatal("expected develop to be created")
	}
	if b := currentBranch(t, d); b != "develop" {
		t.Errorf("on branch %q, want develop", b)
	}
}

// Re-running must not move the user off whatever branch they are on.
func TestEnsureWorkBranchIsIdempotent(t *testing.T) {
	d := gitRepo(t, true)
	if _, _, err := EnsureWorkBranch(d, "develop"); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("git", "-C", d, "checkout", "-q", "main").CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	created, _, err := EnsureWorkBranch(d, "develop")
	if err != nil {
		t.Fatal(err)
	}
	if created {
		t.Error("develop already exists; it must not be recreated")
	}
	if b := currentBranch(t, d); b != "main" {
		t.Errorf("a re-run moved the user to %q; it should leave them alone", b)
	}
}

// An unborn HEAD has nothing to branch from. Committing on the user's behalf
// to work around that is not adoption's business.
func TestEnsureWorkBranchOnEmptyRepoWarnsRatherThanCommitting(t *testing.T) {
	d := gitRepo(t, false)
	created, warns, err := EnsureWorkBranch(d, "develop")
	if err != nil {
		t.Fatal(err)
	}
	if created {
		t.Error("must not create a branch with no commit to base it on")
	}
	if len(warns) == 0 || !strings.Contains(strings.Join(warns, " "), "no commits") {
		t.Errorf("expected a warning explaining why, got %v", warns)
	}
}

func TestRemotePlanLeadsWithTheIrreversibleBit(t *testing.T) {
	p := RemotePlan{
		ProjectName: "fcia", JiraKey: "FCIA", JiraSite: "https://x.atlassian.net",
		SlackName: "orion-fcia", SlackTeam: "Lab", SlackIsPriv: true,
	}
	d := p.Describe()
	if !strings.Contains(d, "cannot be deleted") {
		t.Errorf("a confirmation that hides the one-way door is not a confirmation:\n%s", d)
	}
	if p.Nothing() {
		t.Error("this plan creates two things")
	}
	// Everything already present: nothing to confirm, and no scary warning.
	q := RemotePlan{JiraKey: "FCIA", JiraExists: true, SlackSkip: "not configured"}
	if !q.Nothing() {
		t.Error("binding an existing project creates nothing")
	}
	if strings.Contains(q.Describe(), "cannot be deleted") {
		t.Error("must not warn about deletion when nothing is being created")
	}
}
