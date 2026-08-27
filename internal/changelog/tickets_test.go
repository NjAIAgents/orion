package changelog

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func gitT(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func commit(t *testing.T, dir, msg string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte(msg), 0o644); err != nil {
		t.Fatal(err)
	}
	gitT(t, dir, "add", "-A")
	gitT(t, dir, "commit", "-m", msg)
}

// The keys are what make a missing entry visible. A key written into the
// message and a key that only ever appeared in the branch name both count --
// relying on the message alone would miss every ticket whose author did not
// think to mention it.
func TestTicketKeysReadsMessagesAndBranchNames(t *testing.T) {
	d := t.TempDir()
	gitT(t, d, "init", "--initial-branch=main")
	commit(t, d, "chore: first")
	gitT(t, d, "tag", "v0.1.0")

	commit(t, d, "fix: reject a key that cannot exist (OR-86)")
	commit(t, d, "Merge pull request #4 from NjAIAgents/orion/or-113\n\nfragments")

	keys := TicketKeys(d)
	want := map[string]bool{"OR-86": false, "OR-113": false}
	for _, k := range keys {
		if _, ok := want[k]; ok {
			want[k] = true
		}
	}
	for k, found := range want {
		if !found {
			t.Errorf("%s was not reported; keys were %v", k, keys)
		}
	}
}

// Only since the last tag: a key from a shipped release is not missing from
// the one being cut.
func TestTicketKeysStopsAtTheLastTag(t *testing.T) {
	d := t.TempDir()
	gitT(t, d, "init", "--initial-branch=main")
	commit(t, d, "feat: shipped already (OR-1)")
	gitT(t, d, "tag", "v0.1.0")
	commit(t, d, "feat: not yet shipped (OR-2)")

	for _, k := range TicketKeys(d) {
		if k == "OR-1" {
			t.Errorf("a key from before the last tag was reported: %v", TicketKeys(d))
		}
	}
}

// A repository with no git history must not block a changelog.
func TestTicketKeysIsQuietWithoutGit(t *testing.T) {
	if keys := TicketKeys(t.TempDir()); len(keys) != 0 {
		t.Errorf("want none, got %v", keys)
	}
}
