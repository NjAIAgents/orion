package collect

// Fixtures for this package's tests, built once per test binary and COPIED
// thereafter rather than rebuilt with git.
//
// Every fixture here used to spawn git afresh for each test. Two shapes
// dominated: a provisioned workspace (workspace.New -- `git init` plus
// provision.InitBranches' rev-parse, rev-parse HEAD, checkout -B, commit,
// branch and checkout: eight subprocesses) at about eighty call sites, and an
// origin-plus-clone repository pair (nine subprocesses) at about thirty-five.
// Roughly nine hundred git processes for two trees whose content is identical
// every time.
//
// That is close to free on Linux and it is not on Windows, where process
// creation and file I/O cost around twenty times more and Defender's real-time
// scanner watches exactly this pattern. The measured effect: internal/collect
// took 23s on the Ubuntu CI leg and 559s on the Windows one, and the Windows
// leg -- which runs neither the race detector nor coverage -- was the critical
// path on every run (OR-292).
//
// git is deterministic given the same inputs, so each tree is built once and
// copied. A copy is a file walk of a few kilobytes; the build is eight or nine
// processes. Same treatment internal/work's project() got in v0.8.10.

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/orion-sdlc/orion/internal/workspace"
)

// copyTree copies src to dst, preserving the executable bit. Enough for a git
// repository and a workspace tree: no symlinks, no devices, no sparse files.
func copyTree(t *testing.T, src, dst string) {
	t.Helper()
	if err := copyTreeErr(src, dst); err != nil {
		t.Fatal(err)
	}
}

// copyTreeErr is copyTree for callers that cannot fail a test -- a seed build
// inside sync.Once, where t.Fatal would mark the Once done and leave every
// later caller with an empty fixture and no explanation.
func copyTreeErr(src, dst string) error {
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, workspace.HomeDirMode)
		}
		info, err := d.Info()
		if err != nil {
			// A file that was listed and is gone by the time it is read was
			// never part of the fixture: git's background maintenance writes
			// transient files under .git/objects and removes them again, and
			// the Ubuntu runner's git does exactly that mid-copy (OR-293).
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		data, err := os.ReadFile(p)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		return os.WriteFile(target, data, info.Mode().Perm())
	})
}

// built is one lazily-built fixture tree, shared by every test that wants it.
type built struct {
	once sync.Once
	dir  string
	err  error
}

// seedTempDir makes a directory that outlives the test which happens to
// trigger the build.
//
// Deliberately NOT t.TempDir(): that is removed when the first test to ask for
// the fixture ends, and every later test would then copy from a directory that
// no longer exists. This one lives for the whole binary and is cleaned by the
// OS.
func seedTempDir(prefix string) (string, error) { return os.MkdirTemp("", prefix) }

// --- a provisioned workspace ------------------------------------------------

var (
	workspaceSeeds sync.Map // idea -> *built
	workspaceNo    atomic.Int64
)

// seedWorkspace provisions one workspace per idea, for the life of the binary.
//
// The first caller provisions for real, in ITS OWN home -- ORION_HOME already
// holds the right value at that moment, so nothing has to be swapped and no
// other test can observe a change to it. The tree is then copied somewhere
// that outlives the calling test and the original removed, leaving that home
// exactly as it would have been. Building it under a temporary ORION_HOME
// instead would work today and become a race the moment these tests are made
// parallel, which is OR-264's whole subject.
func seedWorkspace(t *testing.T, idea string) string {
	t.Helper()
	v, _ := workspaceSeeds.LoadOrStore(idea, &built{})
	b := v.(*built)
	b.once.Do(func() {
		root, err := seedTempDir("orion-collect-ws-")
		if err != nil {
			b.err = err
			return
		}
		ws, err := workspace.New(workspace.NewOptions{Idea: idea})
		if err != nil {
			b.err = err
			return
		}
		seed := filepath.Join(root, "workspace")
		if err := copyTreeErr(ws.Dir, seed); err != nil {
			b.err = err
			return
		}
		if err := os.RemoveAll(ws.Dir); err != nil {
			b.err = err
			return
		}
		b.dir = seed
	})
	if b.err != nil {
		t.Fatal(b.err)
	}
	if b.dir == "" {
		t.Fatal("the workspace fixture was never built; see the first failure in this package")
	}
	return b.dir
}

// newWorkspace provisions a workspace under the current ORION_HOME, exactly as
// workspace.New would, by copying the seed for this idea.
//
// The id carries a counter rather than workspace.New's random suffix so that
// two workspaces made from one idea within a single test still get separate
// directories, as they would have before.
func newWorkspace(t *testing.T, idea string) *workspace.Workspace {
	t.Helper()
	seed := seedWorkspace(t, idea)

	id := workspace.Slugify(idea) + "-t" + strconv.FormatInt(workspaceNo.Add(1), 10)
	dir := filepath.Join(workspace.Home(), "projects", id)
	if _, err := os.Stat(dir); err == nil {
		t.Fatalf("workspace %s already exists at %s", id, dir)
	}
	if err := os.MkdirAll(dir, workspace.HomeDirMode); err != nil {
		t.Fatal(err)
	}
	copyTree(t, seed, dir)

	ws, err := workspace.Open(id)
	if err != nil {
		t.Fatalf("opening the copied workspace: %v", err)
	}
	// Open takes the id from the directory name; the copied task.json still
	// carries the seed's. Put them back in step, or a test reading one and
	// the code under test reading the other get two answers.
	ws.Task.ID = id
	if err := ws.SaveTask(); err != nil {
		t.Fatal(err)
	}
	return ws
}

// --- an origin holding develop and orion/x-1, plus a clone of it ------------

var reposSeed built

// seedRepos builds the origin/clone pair once. Only origin.git and repo/ end
// up under the returned root, because everything under it is copied per test.
func seedRepos(t *testing.T) string {
	t.Helper()
	reposSeed.once.Do(func() {
		root, err := seedTempDir("orion-collect-repos-")
		if err != nil {
			reposSeed.err = err
			return
		}
		// The working copy used to build the history sits OUTSIDE root: it is
		// scaffolding, and copying it into every test would be waste.
		work, err := seedTempDir("orion-collect-repowork-")
		if err != nil {
			reposSeed.err = err
			return
		}
		origin := filepath.Join(root, "origin.git")
		gitRun(t, root, "init", "--quiet", "--bare", "--initial-branch=develop", origin)

		gitRun(t, work, "init", "--quiet", "--initial-branch=develop")
		commit(t, work, "", "base")
		gitRun(t, work, "remote", "add", "origin", origin)
		gitRun(t, work, "push", "--quiet", "-u", "origin", "develop")

		// A feature branch cut from develop, as AddWorktree would.
		gitRun(t, work, "checkout", "--quiet", "-b", "orion/x-1")
		commit(t, work, "", "the ticket's work")
		gitRun(t, work, "push", "--quiet", "-u", "origin", "orion/x-1")

		gitRun(t, root, "clone", "--quiet", origin, filepath.Join(root, "repo"))
		reposSeed.dir = root
	})
	if reposSeed.err != nil {
		t.Fatal(reposSeed.err)
	}
	if reposSeed.dir == "" {
		t.Fatal("the repository fixture was never built; see the first failure in this package")
	}
	return reposSeed.dir
}

// repos returns a bare origin holding develop and a branch, and a clone of it
// for collect to inspect. The bare origin is returned too: a second clone must
// come from IT, because git refuses a push to a branch checked out in a
// non-bare repository.
func repos(t *testing.T) (origin, clone string) {
	t.Helper()
	root := t.TempDir()
	copyTree(t, seedRepos(t), root)
	origin = filepath.Join(root, "origin.git")
	clone = filepath.Join(root, "repo")
	// git absolutises a local clone URL, so the copy still points at the seed.
	// Repoint it: tests here push, and a push through a stale URL would write
	// into the tree every other test copies from.
	gitRun(t, clone, "remote", "set-url", "origin", origin)
	return origin, clone
}

// --- what the fixtures themselves promise -----------------------------------
//
// These guard the two properties the rest of the package now depends on and
// which nothing else would notice breaking: that the trees really are built
// once, and that a copy is independent of the tree it was copied from.

// The point of the exercise. If a fixture goes back to building per test the
// package gets slow again and every test still passes, so the reuse is what
// has to be asserted.
func TestTheFixtureTreesAreBuiltOnceForTheBinary(t *testing.T) {
	t.Setenv("ORION_HOME", t.TempDir())

	first, second := seedWorkspace(t, "seedreuse"), seedWorkspace(t, "seedreuse")
	if first != second {
		t.Errorf("a second call rebuilt the workspace seed:\n  %s\n  %s", first, second)
	}
	if before, after := seedRepos(t), seedRepos(t); before != after {
		t.Errorf("a second call rebuilt the repository seed:\n  %s\n  %s", before, after)
	}
	// Under the test's own home it would go with that test, and the next one
	// would copy from a directory that no longer exists.
	if strings.HasPrefix(first, workspace.Home()) {
		t.Errorf("the seed lives under ORION_HOME (%s) and will not outlive this test", first)
	}
}

// A copy that still pushes to the seed's origin contaminates every later test
// with this one's commits, and the failure surfaces somewhere else entirely.
func TestACopiedRepositoryPushesToItsOwnOrigin(t *testing.T) {
	origin, clone := repos(t)
	seedOrigin := filepath.Join(seedRepos(t), "origin.git")
	before := head(t, seedOrigin, "refs/heads/develop")

	commit(t, clone, "", "a commit belonging to this test alone")
	gitRun(t, clone, "push", "--quiet", "origin", "develop")
	want := head(t, clone, "HEAD")

	if got := head(t, origin, "refs/heads/develop"); got != want {
		t.Errorf("develop in this test's own origin is %s, want the pushed %s", got, want)
	}
	if got := head(t, seedOrigin, "refs/heads/develop"); got != before {
		t.Errorf("the push reached the shared seed, which every later test copies: %s -> %s",
			before, got)
	}
}

// Two workspaces built from one idea within a single test used to get separate
// directories from workspace.New's random suffix. The counter replaces it, and
// a counter that does not advance would silently give them one directory.
func TestTwoWorkspacesFromOneIdeaStaySeparate(t *testing.T) {
	t.Setenv("ORION_HOME", t.TempDir())

	first, second := newWorkspace(t, "fcia"), newWorkspace(t, "fcia")
	if first.Dir == second.Dir {
		t.Fatalf("both workspaces were provisioned at %s", first.Dir)
	}
	for _, ws := range []*workspace.Workspace{first, second} {
		reopened, err := workspace.Open(ws.ID)
		if err != nil {
			t.Fatalf("opening %s: %v", ws.ID, err)
		}
		// Open reads the id from the directory name and the rest from
		// task.json; a copied fixture that kept the seed's id would disagree
		// with itself here.
		if reopened.Task.ID != ws.ID {
			t.Errorf("workspace %s carries task id %q", ws.ID, reopened.Task.ID)
		}
	}
}
