package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// OR-304. Cases owned by this file: CI/automation environment (no global git
// config, GPG signing disabled, missing committer identity), error-message
// quality, and argument-count validation. Tagging-behavior cases (moving
// tags, re-runs, channel validation, commit resolution) live in
// tagsource_test.go; this file reuses its helpers rather than duplicating
// them.

// A runner has no global gitconfig at all, and a developer's global config
// may set things (signing, a different user) that would make the script
// behave differently for reasons that have nothing to do with tagging. Both
// GIT_CONFIG_GLOBAL and HOME are pointed at an empty directory so no real
// global or ~/.gitconfig can leak into the run; only the repo's own local
// config (set by newSourceRepo) supplies identity.
func envWithNoGlobalConfig(t *testing.T, extra ...string) []string {
	t.Helper()
	emptyHome := t.TempDir()
	env := append([]string{}, os.Environ()...)
	env = append(env,
		"HOME="+emptyHome,
		"GIT_CONFIG_GLOBAL="+filepath.Join(emptyHome, "nonexistent-gitconfig"),
		"GIT_CONFIG_SYSTEM="+filepath.Join(emptyHome, "nonexistent-gitconfig"),
	)
	env = append(env, extra...)
	return env
}

func runTagSourceWithEnv(t *testing.T, work string, env []string, tag, commit string) (string, error) {
	t.Helper()
	args := []string{}
	if tag != "" {
		args = append(args, tag)
	}
	if commit != "" {
		args = append(args, commit)
	}
	cmd := exec.Command(shellFor(t), append([]string{tagSourceScript(t)}, args...)...)
	cmd.Dir = work
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// No real global gitconfig is present at all -- not merely one that happens
// to agree with the test. The repo's own local identity (set by
// newSourceRepo) is enough on its own for the tag to succeed and land.
func TestScriptSucceedsWithoutPreExistingGlobalGitConfig(t *testing.T) {
	work, released, _ := newSourceRepo(t)

	out, err := runTagSourceWithEnv(t, work, envWithNoGlobalConfig(t, tagSourceEnv()...), "v9.9.1", released)
	if err != nil {
		t.Fatalf("tag-source.sh failed with no global git config present: %v\n%s", err, out)
	}
	if got := publishedTag(t, work, "v9.9.1"); got != released {
		t.Errorf("v9.9.1 names %q, want the released commit %s -- the script must not need a "+
			"pre-existing global gitconfig to tag successfully", got, released)
	}
}

// A developer's global config may set commit.gpgsign/tag.gpgsign true, which
// would make an annotated tag fail on a machine with no signing key
// configured. The repo's local config (tag.gpgsign=false, set by
// newSourceRepo) must be what governs the tag, not whatever a global config
// says -- signing off must not require a working GPG setup for the script to
// succeed.
func TestScriptWorksWithGPGSigningDisabled(t *testing.T) {
	work, released, _ := newSourceRepo(t)

	globalDir := t.TempDir()
	globalConfig := filepath.Join(globalDir, "gitconfig-with-signing-on")
	if err := os.WriteFile(globalConfig, []byte("[commit]\n\tgpgsign = true\n[tag]\n\tgpgsign = true\n[user]\n\tsigningkey = deadbeef\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	env := append(append([]string{}, os.Environ()...), "GIT_CONFIG_GLOBAL="+globalConfig)
	out, err := runTagSourceWithEnv(t, work, env, "v9.9.2", released)
	if err != nil {
		t.Fatalf("tag-source.sh failed with GPG signing disabled locally, even though a global "+
			"config turned it on: %v\n%s", err, out)
	}
	if got := publishedTag(t, work, "v9.9.2"); got != released {
		t.Errorf("v9.9.2 names %q, want the released commit %s", got, released)
	}
	if kind := git(t, work, "cat-file", "-t", "v9.9.2"); kind != "tag" {
		t.Errorf("v9.9.2 is a %s, want an annotated (unsigned) tag object", kind)
	}
}

// Neither the environment nor any git config supplies a committer name or
// email. `git tag -a` cannot construct a tag object without one, and the
// script must surface git's own actionable refusal rather than crash
// obscurely or silently tag as some default identity.
func TestScriptHandlesMissingCommitterIdentityFromEnvironment(t *testing.T) {
	root := t.TempDir()
	origin := filepath.Join(root, "origin.git")
	git(t, root, "init", "--quiet", "--bare", origin)

	work := filepath.Join(root, "work")
	git(t, root, "clone", "--quiet", origin, work)
	// Deliberately no user.name / user.email / gpgsign config on this clone.

	noIdentityEnv := envWithNoGlobalConfig(t)
	filtered := make([]string, 0, len(noIdentityEnv))
	for _, kv := range noIdentityEnv {
		switch {
		case strings.HasPrefix(kv, "GIT_AUTHOR_"), strings.HasPrefix(kv, "GIT_COMMITTER_"):
			continue
		}
		filtered = append(filtered, kv)
	}

	commitCmd := exec.Command("git", "commit", "--quiet", "--allow-empty", "-m", "released",
		"--author", "CI <ci@example.com>")
	commitCmd.Dir = work
	commitCmd.Env = append(filtered, "GIT_AUTHOR_NAME=CI", "GIT_AUTHOR_EMAIL=ci@example.com",
		"GIT_COMMITTER_NAME=CI", "GIT_COMMITTER_EMAIL=ci@example.com")
	if out, err := commitCmd.CombinedOutput(); err != nil {
		t.Fatalf("seeding a commit to tag failed: %v\n%s", err, out)
	}
	revCmd := exec.Command("git", "rev-parse", "HEAD")
	revCmd.Dir = work
	revCmd.Env = filtered
	revOut, err := revCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("rev-parse HEAD failed: %v\n%s", err, revOut)
	}
	released := strings.TrimSpace(string(revOut))
	pushCmd := exec.Command("git", "push", "--quiet", "origin", "HEAD:refs/heads/main")
	pushCmd.Dir = work
	pushCmd.Env = filtered
	if out, err := pushCmd.CombinedOutput(); err != nil {
		t.Fatalf("push failed: %v\n%s", err, out)
	}

	out, err := runTagSourceWithEnv(t, work, filtered, "v9.9.3", released)
	if err == nil {
		t.Fatalf("tag-source.sh tagged a release with no committer name/email available from "+
			"config or environment; it must refuse rather than guess an identity. Output:\n%s", out)
	}
	if strings.TrimSpace(out) == "" {
		t.Error("tag-source.sh failed silently with no committer identity available -- an " +
			"operator gets nothing to act on")
	}
	if got := publishedTag(t, work, "v9.9.3"); got != "" {
		t.Errorf("tag-source.sh pushed v9.9.3 anyway (%s) despite having no committer identity", got)
	}
}

// Every distinct refusal must say something specific enough to act on, and
// no two failure modes should read the same -- an operator staring at
// "error" cannot tell a bad tag format from a missing commit from a tag
// collision.
func TestErrorMessagesAreActionableAndDistinguishFailureModes(t *testing.T) {
	work, released, tip := newSourceRepo(t)
	if out, err := runTagSource(t, work, "v9.9.4", released); err != nil {
		t.Fatalf("seeding a published tag failed: %v\n%s", err, out)
	}

	cases := map[string]string{}

	out, err := runTagSource(t, work, "not-a-release-tag", released)
	if err == nil {
		t.Fatalf("an invalid tag format was accepted: %s", out)
	}
	cases["invalid format"] = out

	out, err = runTagSource(t, work, "v9.9.4", tip)
	if err == nil {
		t.Fatalf("moving a published tag was accepted: %s", out)
	}
	cases["tag exists on a different commit"] = out

	bogus := "0123456789abcdef0123456789abcdef01234567"
	out, err = runTagSource(t, work, "v9.9.5", bogus)
	if err == nil {
		t.Fatalf("a non-existent commit was accepted: %s", out)
	}
	cases["commit not found"] = out

	seen := map[string]string{}
	for label, msg := range cases {
		trimmed := strings.ToLower(strings.TrimSpace(msg))
		if trimmed == "" {
			t.Errorf("%s: produced no output at all -- not actionable", label)
			continue
		}
		if trimmed == "error" || trimmed == "error\n" {
			t.Errorf("%s: message is just \"error\", explains nothing", label)
		}
		for otherLabel, otherMsg := range seen {
			if otherMsg == trimmed {
				t.Errorf("%s and %s produced identical output; an operator cannot tell them "+
					"apart:\n%s", label, otherLabel, msg)
			}
		}
		seen[label] = trimmed
	}

	if !strings.Contains(cases["commit not found"], bogus) {
		t.Errorf("commit-not-found message doesn't name the unresolvable commit %s; got:\n%s",
			bogus, cases["commit not found"])
	}
	if !strings.Contains(cases["tag exists on a different commit"], "refusing to move") {
		t.Errorf("tag-collision message doesn't explain the refusal; got:\n%s",
			cases["tag exists on a different commit"])
	}
}

// No tag argument, and no tag or commit argument: both must print usage and
// exit non-zero rather than proceed with an empty string as the tag or crash
// deeper into the script (e.g. on an unset $2 under `set -u`).
func TestMissingArgumentsShowUsageAndExitWithError(t *testing.T) {
	work, released, _ := newSourceRepo(t)

	tests := []struct {
		name   string
		tag    string
		commit string
	}{
		{"no arguments at all", "", ""},
		{"tag only, missing commit", "v9.9.6", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, err := runTagSourceWithEnv(t, work, tagSourceEnv(), tt.tag, tt.commit)
			if err == nil {
				t.Fatalf("tag-source.sh accepted an invalid argument count (%s). Output:\n%s",
					tt.name, out)
			}
			exitErr, ok := err.(*exec.ExitError)
			if !ok {
				t.Fatalf("expected an ExitError, got %T: %v", err, err)
			}
			if code := exitErr.ExitCode(); code != 2 {
				t.Errorf("exit code = %d, want 2 for a usage error", code)
			}
			if !strings.Contains(strings.ToLower(out), "usage") {
				t.Errorf("missing-argument output doesn't show usage; got:\n%s", out)
			}
			if got := publishedTag(t, work, "v9.9.6"); got != "" {
				t.Errorf("tag-source.sh pushed a tag despite invalid arguments (%s)", got)
			}
		})
	}
	_ = released
}
