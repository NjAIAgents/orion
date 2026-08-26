package supervisor

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func kv(env []string, key string) (string, bool) {
	for _, e := range env {
		if k, v, _ := strings.Cut(e, "="); k == key {
			return v, true
		}
	}
	return "", false
}

func repoWithEmail(t *testing.T, email, cfg string) string {
	t.Helper()
	d := t.TempDir()
	if out, err := exec.Command("git", "-C", d, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	if email != "" {
		if out, err := exec.Command("git", "-C", d, "config", "user.email", email).CombinedOutput(); err != nil {
			t.Fatalf("%v: %s", err, out)
		}
	}
	if cfg != "" {
		if err := os.WriteFile(filepath.Join(d, "orion.json"), []byte(cfg), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return d
}

// The alias must set BOTH author and committer.
//
// It once set only the author, on the reasoning that the committer should
// stay the human who was answerable for the change landing. The result was
// commits authored orionbot that GitHub displayed under the account owner's
// name and avatar: its "X committed" line reads the committer, so the alias
// existed only in `git log`. A mismatched pair also earns the "authored and
// committed by different people" badge on a change no second person touched.
func TestAgentAliasSetsBothAuthorAndCommitter(t *testing.T) {
	d := repoWithEmail(t, "me@example.com", `{"vcs":{"agent_author_name":"orionbot"}}`)
	env := agentAuthorEnv(d)

	for _, k := range []string{"GIT_AUTHOR_NAME", "GIT_COMMITTER_NAME"} {
		if got, _ := kv(env, k); got != "orionbot" {
			t.Errorf("%s = %q, want orionbot", k, got)
		}
	}
	// And the shipped noreply address, NOT the repository owner's, because
	// the email is the field that decides attribution: GitHub matches
	// commits to accounts by address and ignores the name. Falling back to
	// me@example.com here would put the alias in git log and leave GitHub
	// still crediting the human.
	for _, k := range []string{"GIT_AUTHOR_EMAIL", "GIT_COMMITTER_EMAIL"} {
		got, _ := kv(env, k)
		if got == "me@example.com" {
			t.Errorf("%s fell back to the owner's address; GitHub will credit them for the agent's work", k)
		}
		if !strings.Contains(got, "orionbot") {
			t.Errorf("%s = %q, want the bot's own address", k, got)
		}
	}
}

func TestAgentAliasHonoursAnExplicitEmail(t *testing.T) {
	d := repoWithEmail(t, "me@example.com",
		`{"vcs":{"agent_author_name":"bot","agent_author_email":"bot@example.com"}}`)
	if got, _ := kv(agentAuthorEnv(d), "GIT_AUTHOR_EMAIL"); got != "bot@example.com" {
		t.Errorf("GIT_AUTHOR_EMAIL = %q, want the configured override", got)
	}
}

// Git refuses to commit with an empty author email, so an alias with no
// address would break every commit the agent makes. No alias beats that.
//
// Reachable only by blanking the address explicitly -- an empty value in
// orion.json overwrites the shipped default rather than being read as
// absent -- and someone doing that to opt out of the bot identity must not
// get a repository where nothing can commit.
func TestNoEmailAnywhereDisablesTheAliasRatherThanBreakingCommits(t *testing.T) {
	d := repoWithEmail(t, "", `{"vcs":{"agent_author_name":"orionbot","agent_author_email":""}}`)
	t.Setenv("HOME", t.TempDir()) // no global user.email either
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	if env := agentAuthorEnv(d); len(env) != 0 {
		t.Errorf("expected no alias, got %v", env)
	}
}

func TestEmptyNameOptsOut(t *testing.T) {
	d := repoWithEmail(t, "me@example.com", `{"vcs":{"agent_author_name":""}}`)
	if env := agentAuthorEnv(d); len(env) != 0 {
		t.Errorf("an empty alias means author as the human, got %v", env)
	}
}
