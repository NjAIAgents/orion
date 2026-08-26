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

// The alias must set the AUTHOR only. Author says who wrote it, committer
// says who is answerable for it landing; overwriting both would erase the
// human from a change they approved.
func TestAgentAliasSetsAuthorNotCommitter(t *testing.T) {
	d := repoWithEmail(t, "me@example.com", `{"vcs":{"agent_author_name":"orion_agent"}}`)
	env := agentAuthorEnv(d)

	if got, _ := kv(env, "GIT_AUTHOR_NAME"); got != "orion_agent" {
		t.Errorf("GIT_AUTHOR_NAME = %q", got)
	}
	if got, _ := kv(env, "GIT_AUTHOR_EMAIL"); got != "me@example.com" {
		t.Errorf("GIT_AUTHOR_EMAIL = %q; the address must stay the human's so GitHub still links the commit", got)
	}
	if _, ok := kv(env, "GIT_COMMITTER_NAME"); ok {
		t.Error("the committer must remain the human")
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
func TestNoEmailAnywhereDisablesTheAliasRatherThanBreakingCommits(t *testing.T) {
	d := repoWithEmail(t, "", `{"vcs":{"agent_author_name":"orion_agent"}}`)
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
