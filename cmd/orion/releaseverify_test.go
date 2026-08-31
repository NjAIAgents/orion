package main

// OR-238. `release verify`'s attribution check reported 188 unkeyed commits on
// a 30-commit promotion where every commit was keyed. Two faults compounded:
// it walked <last tag>..develop, which includes commits already on main, and
// it asked whether each commit named a ticket in THIS version rather than any
// ticket at all.
//
// These tests drive unattributedCommits against a real repository because
// both faults are in what git is ASKED, which a fixture of log lines cannot
// catch: a test that hands the function its input has already made the
// range-selection decision for it.

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// promotionRepo builds a repo shaped like the one the bug was found on: a
// main with its own history, and a develop ahead of it by the commits being
// promoted. Subjects are given as "branch commit subjects"; the remote-tracking
// refs are written directly since there is no real origin to fetch from.
func promotionRepo(t *testing.T, mainSubjects, developSubjects []string) string {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=qa", "GIT_AUTHOR_EMAIL=qa@example.com",
			"GIT_COMMITTER_NAME=qa", "GIT_COMMITTER_EMAIL=qa@example.com",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	commit := func(n int, subject string) {
		f := filepath.Join(dir, "f.txt")
		if err := os.WriteFile(f, []byte(strings.Repeat("x", n)), 0o644); err != nil {
			t.Fatal(err)
		}
		run("add", "f.txt")
		run("commit", "-q", "-m", subject)
	}

	run("init", "-q", "-b", "main")
	commit(1, "chore: seed")
	for i, s := range mainSubjects {
		commit(i+2, s)
	}
	run("update-ref", "refs/remotes/origin/main", "refs/heads/main")

	run("checkout", "-q", "-b", "develop")
	for i, s := range developSubjects {
		commit(i+100, s)
	}
	run("update-ref", "refs/remotes/origin/develop", "refs/heads/develop")
	return dir
}

// The reported bug, in miniature: main's history is full of unkeyed and
// earlier-milestone commits, the promotion range is entirely keyed, and the
// check must have nothing to say. A clean promotion verifies with no warning.
func TestKeyedPromotionRangeWarnsAboutNothingWhateverMainContains(t *testing.T) {
	dir := promotionRepo(t,
		[]string{
			"docs: hand-pushed with no key at all",
			"fix(OR-179): shipped two releases ago",
			"chore: another unkeyed one on main",
		},
		[]string{
			"feat(OR-238): scope the range to the promotion",
			"test(OR-238): cover it",
		},
	)

	if got := unattributedCommits(dir, "main", "develop"); len(got) != 0 {
		t.Fatalf("a fully keyed promotion reported %d unattributed commit(s): %v", len(got), got)
	}
}

// The tell that the range was wrong was arithmetic, not judgement: 188 is
// larger than the whole promotion. Whatever the criterion decides, the count
// can never exceed the number of commits being promoted.
func TestCountNeverExceedsThePromotionRange(t *testing.T) {
	mainHistory := []string{
		"docs: unkeyed",
		"chore: unkeyed",
		"fix(OR-1): earlier milestone",
		"feat(OR-2): earlier milestone",
	}
	promoted := []string{
		"feat(OR-238): keyed",
		"chore: not keyed",
	}
	dir := promotionRepo(t, mainHistory, promoted)

	got := unattributedCommits(dir, "main", "develop")
	if len(got) > len(promoted) {
		t.Fatalf("reported %d unattributed commit(s) on a %d-commit promotion: %v",
			len(got), len(promoted), got)
	}
	if len(got) != 1 || !strings.Contains(got[0], "chore: not keyed") {
		t.Fatalf("expected only the unkeyed promoted commit, got %v", got)
	}
	for _, line := range got {
		for _, m := range mainHistory {
			if strings.Contains(line, m) {
				t.Errorf("reported %q, which is already on main and not part of this promotion", line)
			}
		}
	}
}

// The second fault, isolated: a commit inside the promotion range keyed to a
// ticket that shipped in an earlier version is attributed work. Asking for a
// key from THIS milestone made every such commit a finding.
func TestCommitKeyedToAnEarlierTicketIsNotAFinding(t *testing.T) {
	dir := promotionRepo(t, []string{"chore: seed main"}, []string{
		"docs(OR-190): follow-up to a ticket that shipped in v0.8.0",
	})

	if got := unattributedCommits(dir, "main", "develop"); len(got) != 0 {
		t.Fatalf("a commit keyed to a previously shipped ticket was reported: %v", got)
	}
}

// The release process's own commits stay exempt: they carry no ticket by
// design, and reporting them on every release is what teaches a reader to
// skip check five.
func TestReleaseProcessCommitsStayExempt(t *testing.T) {
	dir := promotionRepo(t, []string{"chore: seed main"}, []string{
		"docs: assemble the v0.8.4 changelog",
		"chore: genuinely unattributed",
	})

	got := unattributedCommits(dir, "main", "develop")
	if len(got) != 1 || !strings.Contains(got[0], "genuinely unattributed") {
		t.Fatalf("expected only the unattributed commit, got %v", got)
	}
}

// A commit naming several keys is attributed the moment one of them is well
// formed, even if a sibling key on the same line is malformed.
func TestMultipleKeysAttributedIfAnyOneIsWellFormed(t *testing.T) {
	dir := promotionRepo(t, []string{"chore: seed main"}, []string{
		"fix(OR-1, or-2): mixed well-formed and malformed keys",
	})

	if got := unattributedCommits(dir, "main", "develop"); len(got) != 0 {
		t.Fatalf("a commit with one well-formed key among several was reported: %v", got)
	}
}

// A key that is only partially formed -- missing digits, missing the letter
// prefix, or wrong case -- does not satisfy the pattern and must be treated
// as no key at all.
func TestMalformedOnlyKeysAreUnattributed(t *testing.T) {
	cases := []string{
		"fix(OR-): trailing dash, no digits",
		"fix(-123): no letter prefix",
		"fix(or-123): lowercase key",
	}
	for _, subject := range cases {
		t.Run(subject, func(t *testing.T) {
			dir := promotionRepo(t, []string{"chore: seed main"}, []string{subject})

			got := unattributedCommits(dir, "main", "develop")
			if len(got) != 1 || !strings.Contains(got[0], subject) {
				t.Fatalf("expected the malformed-key commit to be reported, got %v", got)
			}
		})
	}
}

// Exempt-subject matching does not care about case: a hand-typed subject in
// caps or mixed case is still the release process's own commit.
func TestExemptSubjectMatchingIsCaseInsensitive(t *testing.T) {
	dir := promotionRepo(t, []string{"chore: seed main"}, []string{
		"DOCS: ASSEMBLE the v0.8.4 changelog",
		"Chore(Release): cut v0.8.4",
		"MERGE branch 'develop'",
	})

	if got := unattributedCommits(dir, "main", "develop"); len(got) != 0 {
		t.Fatalf("differently-cased exempt subjects were reported: %v", got)
	}
}

// The three exempt-subject forms named by the ticket, tested individually so
// a regression in one does not hide behind the other two passing.
func TestEachExemptSubjectFormIsExempt(t *testing.T) {
	cases := []string{
		"docs: assemble the v0.8.4 changelog",
		"chore(release): cut v0.8.4",
		"merge pull request #42 from origin/feature",
		"merge branch 'develop' into main",
	}
	for _, subject := range cases {
		t.Run(subject, func(t *testing.T) {
			dir := promotionRepo(t, []string{"chore: seed main"}, []string{subject})

			if got := unattributedCommits(dir, "main", "develop"); len(got) != 0 {
				t.Fatalf("exempt subject %q was reported: %v", subject, got)
			}
		})
	}
}

// The pattern must fire wherever the key sits in the subject, and must find
// every key on a line with more than one.
func TestTicketKeyMatchesAtAnyPositionInTheSubject(t *testing.T) {
	dir := promotionRepo(t, []string{"chore: seed main"}, []string{
		"OR-1: key at the very start",
		"a change that ends with OR-2",
		"fix: something in the middle OR-3 of the subject",
		"OR-4 and OR-5 both named",
	})

	if got := unattributedCommits(dir, "main", "develop"); len(got) != 0 {
		t.Fatalf("a keyed commit was reported regardless of where the key sits: %v", got)
	}
}

// An empty promotion range -- develop at the same commit as main -- must not
// manufacture a finding out of nothing.
func TestEmptyPromotionRangeReportsNothing(t *testing.T) {
	dir := promotionRepo(t, []string{"chore: seed main"}, nil)

	if got := unattributedCommits(dir, "main", "develop"); len(got) != 0 {
		t.Fatalf("an empty promotion range reported findings: %v", got)
	}
}

// A promotion of exactly one commit, keyed, must warn about nothing -- the
// boundary the ticket calls out by name.
func TestSingleKeyedCommitPromotionReportsNothing(t *testing.T) {
	dir := promotionRepo(t, []string{"chore: seed main"}, []string{
		"feat(OR-238): the only commit in this promotion",
	})

	if got := unattributedCommits(dir, "main", "develop"); len(got) != 0 {
		t.Fatalf("a single keyed commit was reported: %v", got)
	}
}
