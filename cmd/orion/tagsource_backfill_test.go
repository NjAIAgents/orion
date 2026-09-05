package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// A single `sh -c` invocation, so a test can chain tag-source.sh ahead of a
// stand-in publish step with a plain `&&` -- the same short-circuit a
// GitHub Actions job gets for free from one step failing.
func runShell(t *testing.T, dir, script string) (string, error) {
	t.Helper()
	cmd := exec.Command("sh", "-c", script)
	cmd.Dir = dir
	cmd.Env = tagSourceEnv()
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// OR-304, continued. This file covers the backfill shape -- tagging a commit
// that is not HEAD and not the tip of any branch, the way v0.8.10 itself had
// to be tagged after the fact -- plus the tag-object properties that make an
// annotated tag different from a lightweight one, and the failure-stops-the-
// publish guarantee the workflow depends on. It reuses the helpers next door
// in tagsource_test.go (tagSourceEnv, git, newSourceRepo, runTagSource,
// publishedTag) rather than growing a second copy of the same bare-repo setup.

// A push failure is not survivable by continuing on to publish assets: the
// whole point of tagging first is that a run stops here, before anything is
// published that cannot be traced back to a commit.
func breakPushesTo(t *testing.T, origin string) {
	t.Helper()
	if err := filepath.Walk(origin, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		mode := os.FileMode(0o555)
		if !info.IsDir() {
			mode = 0o444
		}
		return os.Chmod(path, mode)
	}); err != nil {
		t.Fatalf("locking down origin for a push failure: %v", err)
	}
	t.Cleanup(func() {
		_ = filepath.Walk(origin, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return nil
			}
			if info.IsDir() {
				return os.Chmod(path, 0o755)
			}
			return os.Chmod(path, 0o644)
		})
	})
}

func originOf(t *testing.T, work string) string {
	t.Helper()
	return git(t, work, "remote", "get-url", "origin")
}

// THE WORKFLOW MUST STOP HERE. release.yml chains the tag step ahead of the
// publish steps, which only means anything because the script itself exits
// non-zero when the push fails -- a step that silently swallowed the error
// would let the job carry on to `gh release create` with an untagged commit,
// which is the exact bug OR-304 was filed about.
func TestWorkflowExitsBeforePublishingAssetsIfTagPushFails(t *testing.T) {
	work, released, _ := newSourceRepo(t)
	breakPushesTo(t, originOf(t, work))

	out, err := runTagSource(t, work, "v9.9.9", released)
	if err == nil {
		t.Fatalf("tag-source.sh exited 0 despite the push failing; a workflow chaining "+
			"this step ahead of `gh release create` would carry on to publish assets for an "+
			"untagged commit. Output:\n%s", out)
	}
}

// The same guarantee from the other side: nothing downstream of the tag step
// runs at all. A GitHub Actions job stops at the first failing step by
// default, which is what `&&` models here without needing the real workflow.
func TestNoAssetsArePublishedWhenTaggingFails(t *testing.T) {
	work, released, _ := newSourceRepo(t)
	breakPushesTo(t, originOf(t, work))

	marker := filepath.Join(work, "assets-published")
	cmd := strings.Join([]string{
		"sh", tagSourceScript(t), "v9.9.9", released, "&&", "touch", marker,
	}, " ")
	out, err := runShell(t, work, cmd)
	if err == nil {
		t.Fatalf("the chained publish step ran despite the tag push failing. Output:\n%s", out)
	}
	if _, statErr := os.Stat(marker); statErr == nil {
		t.Error("assets were published even though tagging the source repo failed")
	}
}

// A backfill is given the commit the release actually shipped from, and that
// commit has usually already been left behind by main -- HEAD moving on is
// the ordinary case a backfill exists to handle, not an edge case of it.
func TestTagCanBeCreatedOnACommitThatIsNotHEAD(t *testing.T) {
	work, released, tip := newSourceRepo(t)

	head := git(t, work, "rev-parse", "HEAD")
	if head != tip {
		t.Fatalf("test setup: HEAD %s is not the tip commit %s", head, tip)
	}
	if released == head {
		t.Fatal("test setup: the commit being backfilled must not be HEAD")
	}

	out, err := runTagSource(t, work, "v9.9.9", released)
	if err != nil {
		t.Fatalf("tagging a commit that is not HEAD failed: %v\n%s", err, out)
	}
	if got := publishedTag(t, work, "v9.9.9"); got != released {
		t.Errorf("v9.9.9 names %q, want the backfilled commit %s (HEAD was %s and must stay "+
			"untouched by the tag)", got, released, head)
	}
	if head2 := git(t, work, "rev-parse", "HEAD"); head2 != head {
		t.Errorf("HEAD moved from %s to %s while tagging a different commit", head, head2)
	}
}

// A commit reachable only by its SHA, orphaned from every branch tip on
// purpose -- the shape of a truly historical release commit once whatever
// branch pointed at it has moved past it and nothing else names it anymore.
func TestTagCanBeCreatedOnACommitThatIsNotTheTipOfAnyBranch(t *testing.T) {
	work, _, tip := newSourceRepo(t)

	git(t, work, "checkout", "--quiet", "-b", "abandoned")
	if err := os.WriteFile(filepath.Join(work, "f.txt"), []byte("an abandoned attempt"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, work, "add", "f.txt")
	git(t, work, "commit", "--quiet", "-m", "an abandoned attempt")
	orphan := git(t, work, "rev-parse", "HEAD")
	git(t, work, "checkout", "--quiet", "main")
	git(t, work, "branch", "-D", "abandoned")

	if branches := git(t, work, "branch", "--contains", orphan); branches != "" {
		t.Fatalf("test setup: %s is still the tip of a branch:\n%s", orphan, branches)
	}
	if orphan == tip {
		t.Fatal("test setup: the orphaned commit must differ from main's tip")
	}

	out, err := runTagSource(t, work, "v9.9.9", orphan)
	if err != nil {
		t.Fatalf("tagging a commit that is the tip of no branch failed: %v\n%s", err, out)
	}
	if got := publishedTag(t, work, "v9.9.9"); got != orphan {
		t.Errorf("v9.9.9 names %q, want the orphaned commit %s", got, orphan)
	}
}

// The literal DONE WHEN for the v0.8.10 backfill: after tagging the historical
// release commit, `git log v0.8.10` must resolve to it instead of failing
// with "unknown revision", which is what a reader gets from the untagged repo
// today.
func TestBackfillingV0_8_10MakesGitLogResolveToTheReleaseCommit(t *testing.T) {
	work, released, _ := newSourceRepo(t)

	if out, err := runTagSource(t, work, "v0.8.10", released); err != nil {
		t.Fatalf("backfilling v0.8.10 failed: %v\n%s", err, out)
	}

	if got := git(t, work, "log", "-1", "--format=%H", "v0.8.10"); got != released {
		t.Errorf("git log v0.8.10 resolves to %s, want the historical release commit %s", got, released)
	}
}

// Annotated, not lightweight: `git tag <name> <commit>` (no -a/-m) would make
// refs/tags/<name> point straight at the commit object, and `git cat-file -t`
// on it would answer "commit". The script must always go through `git tag -a`.
func TestCreatedTagIsAnnotatedNotLightweight(t *testing.T) {
	work, released, _ := newSourceRepo(t)

	if out, err := runTagSource(t, work, "v9.9.9", released); err != nil {
		t.Fatalf("tagging failed: %v\n%s", err, out)
	}

	// The tag ref and the commit it names must be two different objects --
	// the hallmark of an annotated tag. A lightweight tag's ref IS the
	// commit's own SHA.
	tagObject := git(t, work, "rev-parse", "v9.9.9")
	if tagObject == released {
		t.Fatalf("refs/tags/v9.9.9 resolves straight to the commit %s; the tag is lightweight, "+
			"not annotated", released)
	}
	if kind := git(t, work, "cat-file", "-t", "v9.9.9"); kind != "tag" {
		t.Errorf("git cat-file -t v9.9.9 = %q, want \"tag\"", kind)
	}
}

// The reason an annotated tag exists at all: it records who cut the release
// and when, which is exactly what a lightweight tag on the same commit would
// have thrown away.
func TestAnnotatedTagContainsCreatorAndTimestampMetadata(t *testing.T) {
	work, released, _ := newSourceRepo(t)

	if out, err := runTagSource(t, work, "v9.9.9", released); err != nil {
		t.Fatalf("tagging failed: %v\n%s", err, out)
	}

	body := git(t, work, "cat-file", "-p", "v9.9.9")
	line := ""
	for _, l := range strings.Split(body, "\n") {
		if strings.HasPrefix(l, "tagger ") {
			line = l
			break
		}
	}
	if line == "" {
		t.Fatalf("git cat-file -p v9.9.9 has no tagger line, so the tag records neither who "+
			"cut the release nor when:\n%s", body)
	}
	if !strings.Contains(line, "ci@example.com") {
		t.Errorf("the tagger line does not name the committer identity: %q", line)
	}
	fields := strings.Fields(line)
	if len(fields) < 2 {
		t.Fatalf("the tagger line is malformed: %q", line)
	}
	epoch := fields[len(fields)-2]
	if epoch == "" || strings.ContainsAny(epoch, "abcdefghijklmnopqrstuvwxyz") {
		t.Errorf("the tagger line's timestamp field does not look like a unix epoch: %q", line)
	}
}

// The same distinction TestCreatedTagIsAnnotatedNotLightweight makes, proved
// against a real lightweight tag on the identical commit rather than by
// assertion alone -- the two ref types must disagree on `cat-file -t`.
func TestGitCatFileTypeReturnsTagNotCommitForTheCreatedTag(t *testing.T) {
	work, released, _ := newSourceRepo(t)

	if out, err := runTagSource(t, work, "v9.9.9", released); err != nil {
		t.Fatalf("tagging failed: %v\n%s", err, out)
	}
	git(t, work, "tag", "lightweight-comparison", released)

	if kind := git(t, work, "cat-file", "-t", "v9.9.9"); kind != "tag" {
		t.Errorf("git cat-file -t v9.9.9 = %q, want \"tag\"", kind)
	}
	if kind := git(t, work, "cat-file", "-t", "lightweight-comparison"); kind != "commit" {
		t.Errorf("git cat-file -t lightweight-comparison = %q, want \"commit\" -- this "+
			"comparison tag was created without -a and its type must differ from the one "+
			"tag-source.sh creates, or the two are indistinguishable", kind)
	}
}
