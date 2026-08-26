package provision

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func repoAt(t *testing.T, withCommit bool) string {
	t.Helper()
	d := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = d
		cmd.Env = append(os.Environ(),
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

// Both long-lived branches must exist before any work starts. Creating them
// later is the only reliable way for a first commit to land on the wrong one.
func TestInitBranchesEstablishesBothAndLeavesYouOnTheWorkBranch(t *testing.T) {
	d := repoAt(t, true)
	created, err := InitBranches(d, "main", "develop")
	if err != nil {
		t.Fatal(err)
	}
	if len(created) == 0 {
		t.Error("nothing reported as created")
	}
	for _, b := range []string{"main", "develop"} {
		if !branchExists(d, b) {
			t.Errorf("%s was not created", b)
		}
	}
	cur, _ := git(d, "branch", "--show-current")
	if strings.TrimSpace(cur) != "develop" {
		t.Errorf("left on %q; work must begin on the work branch, not main", cur)
	}
}

// orion init is a repair command: running it twice must not fail or churn.
func TestInitBranchesIsIdempotent(t *testing.T) {
	d := repoAt(t, true)
	if _, err := InitBranches(d, "main", "develop"); err != nil {
		t.Fatal(err)
	}
	created, err := InitBranches(d, "main", "develop")
	if err != nil {
		t.Fatalf("a second run must succeed: %v", err)
	}
	if len(created) != 0 {
		t.Errorf("reported %v as newly created on a repeat run", created)
	}
}

// On an empty repo this DOES commit, deliberately: a branch cannot exist
// without one, and this path runs for a repository Orion is creating.
//
// Note the asymmetry with workspace.EnsureWorkBranch, which refuses in the
// same situation. That is not an inconsistency to fix: this owns the repo
// it is initialising, whereas EnsureWorkBranch is adopting one that belongs
// to someone else, and committing into a stranger's repository to make your
// own model tidy is a different act entirely. Both behaviours are pinned so
// neither drifts into the other.
func TestInitBranchesSeedsAnEmptyRepoWithARootCommit(t *testing.T) {
	d := repoAt(t, false)
	created, err := InitBranches(d, "main", "develop")
	if err != nil {
		t.Fatalf("an empty repo is expected to be seeded: %v", err)
	}
	if len(created) != 2 {
		t.Errorf("created = %v, want both branches", created)
	}
	out, _ := git(d, "log", "--oneline")
	if strings.TrimSpace(out) == "" {
		t.Fatal("no root commit was made, so the branches cannot exist")
	}
	msg, _ := git(d, "log", "-1", "--pretty=%B")
	if !strings.Contains(msg, "Orion") {
		t.Errorf("the root commit should say who made it and why, got: %q", msg)
	}
	for _, b := range []string{"main", "develop"} {
		if !branchExists(d, b) {
			t.Errorf("%s missing after seeding", b)
		}
	}
}

func TestInitBranchesRejectsANonRepo(t *testing.T) {
	_, err := InitBranches(t.TempDir(), "main", "develop")
	if err == nil || !strings.Contains(err.Error(), "not a git repository") {
		t.Errorf("got %v", err)
	}
}

func TestBranchExists(t *testing.T) {
	d := repoAt(t, true)
	if !branchExists(d, "main") {
		t.Error("main should exist")
	}
	if branchExists(d, "nope") {
		t.Error("a missing branch was reported as present")
	}
	// A ref that is not a branch must not count as one.
	if _, err := git(d, "tag", "v1"); err != nil {
		t.Fatal(err)
	}
	if branchExists(d, "v1") {
		t.Error("a tag was mistaken for a branch")
	}
}

func TestExistingRemote(t *testing.T) {
	d := repoAt(t, true)
	if got := existingRemote(d); got != "" {
		t.Errorf("a fresh repo has no origin, got %q", got)
	}
	if _, err := git(d, "remote", "add", "origin", "https://example.com/x.git"); err != nil {
		t.Fatal(err)
	}
	if got := existingRemote(d); got != "https://example.com/x.git" {
		t.Errorf("got %q", got)
	}
}

func TestTruncateKeepsItReadable(t *testing.T) {
	long := strings.Repeat("a", 500)
	got := truncate(long, 50)
	if len(got) > 60 {
		t.Errorf("truncate produced %d chars", len(got))
	}
	if short := truncate("abc", 50); short != "abc" {
		t.Errorf("a short string was altered: %q", short)
	}
}

func TestSortedKeysIsStable(t *testing.T) {
	in := map[string]string{"c": "1", "a": "2", "b": "3"}
	got := sortedKeys(in)
	if strings.Join(got, "") != "abc" {
		t.Errorf("got %v, want sorted output so reports do not shuffle", got)
	}
	if len(sortedKeys(map[string]string{})) != 0 {
		t.Error("empty map should give an empty slice")
	}
}

func TestGitReportsFailuresWithOutput(t *testing.T) {
	d := repoAt(t, true)
	out, err := git(d, "no-such-subcommand")
	if err == nil {
		t.Fatal("expected an error")
	}
	if strings.TrimSpace(out) == "" {
		t.Error("git's own message must be passed through, not swallowed")
	}
}

// Creating a repository in someone's account is not sandboxed work, so it
// must be behind a confirmation that actually stops it.
func TestRemoteAbortsWhenConfirmationIsDeclined(t *testing.T) {
	// Remote() needs gh INSTALLED and AUTHENTICATED, and returns before the
	// confirmation if either is missing. Guarding on presence alone made
	// this pass on a laptop with a gh login and fail on a CI runner that
	// ships gh unauthenticated -- green here, red there, for a reason that
	// has nothing to do with the code under test.
	//
	// A test whose result depends on ambient credentials teaches people to
	// re-run CI rather than read it.
	if _, err := exec.LookPath("gh"); err != nil {
		t.Skip("gh not installed")
	}
	if err := exec.Command("gh", "auth", "status").Run(); err != nil {
		t.Skip("gh is not authenticated; Remote refuses before it can ask, " +
			"so there is no confirmation to observe")
	}
	d := repoAt(t, true)
	var buf strings.Builder
	asked := false
	_, err := Remote(Options{
		Dir: d, Name: "orion-should-never-exist", Private: true,
		DefaultBranch: "main", WorkBranch: "develop", Out: &buf,
		Confirm: func(string) bool { asked = true; return false },
	})
	if !asked {
		t.Fatal("no confirmation was requested before creating a remote repository")
	}
	if err == nil && !strings.Contains(buf.String()+errStr(err), "") {
		t.Skip("unexpected shape")
	}
	if got := existingRemote(d); got != "" {
		t.Errorf("a remote was added despite the refusal: %q", got)
	}
}

func errStr(e error) string {
	if e == nil {
		return ""
	}
	return e.Error()
}

// Without gh the failure must name the missing tool rather than surfacing a
// bare exec error.
func TestRemoteExplainsAMissingGH(t *testing.T) {
	if _, err := exec.LookPath("gh"); err == nil {
		t.Skip("gh is installed; this covers the missing-gh path only")
	}
	d := repoAt(t, true)
	_, err := Remote(Options{Dir: d, Name: "x", Out: &strings.Builder{}})
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "gh") {
		t.Errorf("the error should name the missing tool, got: %v", err)
	}
	_ = filepath.Base(d)
}
