package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// OR-304. The release workflow named things after a tag it never created:
// v0.8.10's assets were published while the source repo's newest tag stayed
// v0.8.9, so `git log v0.8.10` failed and every `make build` on develop
// reported itself as commits-past-v0.8.9. scripts/tag-source.sh is the fix,
// and unlike the beta guards next door it can be exercised for real -- a bare
// repository on disk is a complete remote, so these run the actual pushes.

func tagSourceScript(t *testing.T) string {
	t.Helper()
	p, err := filepath.Abs(filepath.Join("..", "..", "scripts", "tag-source.sh"))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// A committer identity and no signing: a runner has neither, and a developer's
// global gitconfig may well turn signing on, which would make these fail for a
// reason that has nothing to do with tagging.
func tagSourceEnv() []string {
	return append(os.Environ(),
		"GIT_AUTHOR_NAME=CI", "GIT_AUTHOR_EMAIL=ci@example.com",
		"GIT_COMMITTER_NAME=CI", "GIT_COMMITTER_EMAIL=ci@example.com",
	)
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = tagSourceEnv()
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s in %s: %v\n%s", strings.Join(args, " "), dir, err, out)
	}
	return strings.TrimSpace(string(out))
}

// A clone with a real remote and two commits, so a test can tag the OLDER one
// and prove the tag landed where the release was cut rather than on the tip.
// That distinction is the whole of the v0.8.10 backfill: main happened to
// still point at the released commit on the day the bug was filed, and would
// not have a week later.
func newSourceRepo(t *testing.T) (work, first, second string) {
	t.Helper()
	root := t.TempDir()

	origin := filepath.Join(root, "origin.git")
	git(t, root, "init", "--quiet", "--bare", origin)

	work = filepath.Join(root, "work")
	git(t, root, "clone", "--quiet", origin, work)
	for _, kv := range [][2]string{
		{"user.name", "CI"},
		{"user.email", "ci@example.com"},
		{"commit.gpgsign", "false"},
		{"tag.gpgsign", "false"},
	} {
		git(t, work, "config", kv[0], kv[1])
	}

	commit := func(body string) string {
		if err := os.WriteFile(filepath.Join(work, "f.txt"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		git(t, work, "add", "f.txt")
		git(t, work, "commit", "--quiet", "-m", body)
		return git(t, work, "rev-parse", "HEAD")
	}
	first = commit("released")
	second = commit("after the release")
	git(t, work, "push", "--quiet", "origin", "HEAD:refs/heads/main")

	return work, first, second
}

func runTagSource(t *testing.T, work, tag, commit string) (string, error) {
	t.Helper()
	cmd := exec.Command("sh", tagSourceScript(t), tag, commit)
	cmd.Dir = work
	cmd.Env = tagSourceEnv()
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// The remote's own view of the tag, peeled to the commit it names. Empty if
// the remote has no such tag. This is literally the check the issue's DONE
// WHEN asks for -- `git ls-remote --tags origin` showing the released version
// -- run without a refspec on purpose: given a pattern, ls-remote stops
// advertising the peeled `^{}` line and an annotated tag reads as its own tag
// object rather than the commit it names.
func publishedTag(t *testing.T, work, tag string) string {
	t.Helper()
	for _, line := range strings.Split(git(t, work, "ls-remote", "--tags", "origin"), "\n") {
		if strings.HasSuffix(line, "refs/tags/"+tag+"^{}") {
			return strings.Fields(line)[0]
		}
	}
	return ""
}

func TestTaggingTheSourceLandsOnTheCommitThatWasReleased(t *testing.T) {
	work, released, tip := newSourceRepo(t)

	out, err := runTagSource(t, work, "v9.9.9", released)
	if err != nil {
		t.Fatalf("tagging a fresh release failed: %v\n%s", err, out)
	}

	if got := publishedTag(t, work, "v9.9.9"); got != released {
		t.Errorf("git ls-remote --tags origin names %q for v9.9.9, want the released "+
			"commit %s. An untagged release is one nothing in the source repo can be "+
			"traced back to -- v0.8.10's only record was a release object in another "+
			"repository.", got, released)
	}

	// Annotated, not lightweight: `git describe` prefers annotated tags, and a
	// lightweight one carries no record of who cut the release or when.
	if kind := git(t, work, "cat-file", "-t", "v9.9.9"); kind != "tag" {
		t.Errorf("v9.9.9 is a %s, want an annotated tag object", kind)
	}

	// The consequence that actually bit: `make build` derives VERSION from
	// `git describe --tags`, so an untagged release makes every later build
	// claim to be older than something already shipped.
	if desc := git(t, work, "describe", "--tags"); !strings.HasPrefix(desc, "v9.9.9-") {
		t.Errorf("git describe --tags at %s = %q, want it to name v9.9.9; a build after "+
			"the release must not report the version before it", tip, desc)
	}
}

// A tag is not scoped to the run that created it -- ordinary fetch/pull/push
// traffic against the same remote afterwards must not touch it. If tagging
// only "stuck" until the next network operation, the v0.8.10 defect would
// resurface the moment anyone else interacted with the repo.
func TestTagPersistsAfterSubsequentGitOperations(t *testing.T) {
	work, released, _ := newSourceRepo(t)

	if out, err := runTagSource(t, work, "v9.9.9", released); err != nil {
		t.Fatalf("tagging failed: %v\n%s", err, out)
	}

	git(t, work, "fetch", "--quiet", "origin")
	git(t, work, "pull", "--quiet", "origin", "main")
	git(t, work, "push", "--quiet", "origin", "HEAD:refs/heads/main")

	if got := publishedTag(t, work, "v9.9.9"); got != released {
		t.Errorf("v9.9.9 names %q after fetch/pull/push, want it to still name the "+
			"released commit %s -- a tag must not be undone by ordinary git traffic "+
			"against the same remote", got, released)
	}
	if kind := git(t, work, "cat-file", "-t", "v9.9.9"); kind != "tag" {
		t.Errorf("v9.9.9 is a %s after fetch/pull/push, want it to remain an annotated tag object", kind)
	}
}

// Re-running a release for an already-existing tag is proven safe here, which
// is the DONE WHEN clause a dry run would otherwise have to cover. A workflow
// that could only ever be dispatched once would make every transient publish
// failure permanent.
func TestRerunningAReleaseForTheSameTagAndCommitSucceeds(t *testing.T) {
	work, released, _ := newSourceRepo(t)

	if out, err := runTagSource(t, work, "v9.9.9", released); err != nil {
		t.Fatalf("first run failed: %v\n%s", err, out)
	}
	out, err := runTagSource(t, work, "v9.9.9", released)
	if err != nil {
		t.Fatalf("re-running a release for an existing tag must not fail the run: %v\n%s", err, out)
	}
	if !strings.Contains(out, "nothing to do") {
		t.Errorf("a re-run should say the tag is already published; got:\n%s", out)
	}
	if got := publishedTag(t, work, "v9.9.9"); got != released {
		t.Errorf("a re-run moved v9.9.9 to %s, want it left on %s", got, released)
	}
}

// Moving a published tag is worse than refusing to: the version is already
// released under the old meaning, and re-pointing it rewrites what every
// checkout and every archived build referred to.
func TestTaggingRefusesToMoveAPublishedTag(t *testing.T) {
	work, released, tip := newSourceRepo(t)

	if out, err := runTagSource(t, work, "v9.9.9", released); err != nil {
		t.Fatalf("first run failed: %v\n%s", err, out)
	}
	out, err := runTagSource(t, work, "v9.9.9", tip)
	if err == nil {
		t.Errorf("tagging v9.9.9 at a second commit succeeded; a published tag must "+
			"never be moved silently. Output:\n%s", out)
	}
	if !strings.Contains(out, "refusing to move") {
		t.Errorf("the refusal does not say what it refused; got:\n%s", out)
	}
	// An operator reading the refusal needs both SHAs to know what happened and
	// what was attempted -- "refusing to move" alone tells them nothing to act on.
	if !strings.Contains(out, released) {
		t.Errorf("the refusal does not name the published commit %s it refused to move; got:\n%s", released, out)
	}
	if !strings.Contains(out, tip) {
		t.Errorf("the refusal does not name the attempted commit %s; got:\n%s", tip, out)
	}
	if got := publishedTag(t, work, "v9.9.9"); got != released {
		t.Errorf("v9.9.9 now names %s; the refusal did not leave the published tag "+
			"on %s", got, released)
	}
	// The local tag object the first run created must be equally untouched --
	// a refusal that moved the local ref while leaving the remote alone would
	// just delay the corruption to the next push.
	if local := git(t, work, "rev-parse", "refs/tags/v9.9.9^{commit}"); local != released {
		t.Errorf("local tag v9.9.9 now points at %s, want it left on %s", local, released)
	}
}

// The same refusal for a stale local tag from an abandoned attempt. It is the
// likelier case for the operator running a backfill by hand, and overwriting
// it quietly would publish a tag nobody chose.
func TestTaggingRefusesToMoveAnUnpushedLocalTag(t *testing.T) {
	work, released, tip := newSourceRepo(t)
	git(t, work, "tag", "-a", "v9.9.9", "-m", "stale", tip)

	out, err := runTagSource(t, work, "v9.9.9", released)
	if err == nil {
		t.Errorf("an unpushed v9.9.9 on another commit was overwritten. Output:\n%s", out)
	}
	if !strings.Contains(out, tip) {
		t.Errorf("the refusal does not name the stale local commit %s; got:\n%s", tip, out)
	}
	if !strings.Contains(out, released) {
		t.Errorf("the refusal does not name the attempted commit %s; got:\n%s", released, out)
	}
	if got := publishedTag(t, work, "v9.9.9"); got != "" {
		t.Errorf("the refusal still pushed v9.9.9 (now %s)", got)
	}
	if local := git(t, work, "rev-parse", "refs/tags/v9.9.9^{commit}"); local != tip {
		t.Errorf("local tag v9.9.9 now points at %s, want it left on the stale commit %s", local, tip)
	}
}

// A commit that does not exist must fail loudly, not tag whatever HEAD
// happens to be. `git rev-parse --verify` is the only thing standing between
// a typo'd SHA and a release tagged on the wrong commit.
func TestTaggingRejectsANonExistentCommit(t *testing.T) {
	work, _, _ := newSourceRepo(t)

	bogus := "0123456789abcdef0123456789abcdef01234567"
	out, err := runTagSource(t, work, "v9.9.9", bogus)
	if err == nil {
		t.Fatalf("tag-source.sh accepted a commit that does not exist in the repo. Output:\n%s", out)
	}
	if !strings.Contains(out, bogus) {
		t.Errorf("the error does not name the unresolvable commit %s so an operator can tell "+
			"what was passed; got:\n%s", bogus, out)
	}
	if got := publishedTag(t, work, "v9.9.9"); got != "" {
		t.Errorf("tag-source.sh pushed v9.9.9 anyway (%s), naming a commit it could not verify", got)
	}
}

// A short reference that resolves to nothing must be rejected the same way --
// it must never fall back to HEAD or otherwise guess which commit was meant.
// Guessing here is exactly the failure mode this script exists to close off:
// a release naming the wrong commit is indistinguishable from one naming none.
func TestTaggingRejectsAnUnresolvableAbbreviatedCommit(t *testing.T) {
	work, _, tip := newSourceRepo(t)

	out, err := runTagSource(t, work, "v9.9.9", "deadbee")
	if err == nil {
		t.Fatalf("tag-source.sh accepted an abbreviated commit reference that matches nothing "+
			"in the repo. Output:\n%s", out)
	}
	if got := publishedTag(t, work, "v9.9.9"); got != "" {
		t.Errorf("tag-source.sh pushed v9.9.9 anyway (%s) instead of refusing the unresolvable "+
			"reference", got)
	}
	// Confirms it did not silently fall back to the tip commit rather than
	// simply failing to resolve anything.
	if got := publishedTag(t, work, "v9.9.9"); got == tip {
		t.Errorf("tag-source.sh guessed the tip commit %s for an unresolvable reference "+
			"instead of refusing it", tip)
	}
}

// The shapes stay in tag-channel.sh. A string that names no channel is not a
// release tag, and putting one into the source repo's history creates a name
// nothing else will ever resolve.
func TestTaggingRefusesATagThatNamesNoChannel(t *testing.T) {
	work, released, _ := newSourceRepo(t)

	for _, tag := range []string{"v1.2.3-rc.1", "1.2.3", "main"} {
		out, err := runTagSource(t, work, tag, released)
		if err == nil {
			t.Errorf("tag-source.sh accepted %q, which names no release channel. Output:\n%s", tag, out)
		}
		if got := publishedTag(t, work, tag); got != "" {
			t.Errorf("tag-source.sh pushed %q anyway (%s)", tag, got)
		}
	}
}

// THE WORKFLOW MUST TAG BEFORE IT PUBLISHES, and the ordering is the decision
// this ticket had to make and state: a release REFUSES to publish when the tag
// cannot be pushed. Assets nobody can trace back to a commit are the defect
// itself, and a run that stops before `gh release create` leaves nothing to
// un-publish -- whereas a tag push that fails afterwards leaves a release that
// only a human can reconcile.
func TestTheReleaseWorkflowTagsTheSourceBeforePublishingAssets(t *testing.T) {
	wf := repoFile(t, ".github", "workflows", "release.yml")

	tagStep := strings.Index(wf, "tag-source.sh")
	if tagStep < 0 {
		t.Fatal("the release workflow never tags the source repo. It uses the dispatched " +
			"tag only to NAME things -- the channel, the package-manager version, the dist " +
			"archive names -- so the released commit is unidentifiable from the source repo " +
			"and every later `make build` reports the version before it (OR-304).")
	}
	publish := strings.Index(wf, "gh release create")
	if publish < 0 {
		t.Fatal("the release workflow no longer publishes a release")
	}
	if tagStep > publish {
		t.Error("the source repo is tagged AFTER the assets are published, so a failed tag " +
			"push leaves a published release that nothing can be traced back to -- the exact " +
			"state OR-304 was filed about. Tag first and let the run fail before publishing.")
	}

	// The commit it BUILT, not whatever the branch points at when the step
	// runs. They are the same only by luck.
	if !strings.Contains(wf, "GITHUB_SHA") {
		t.Error("the tag step does not name the commit being built, so it would tag " +
			"whatever HEAD resolves to at that moment")
	}
}
