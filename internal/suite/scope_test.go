package suite

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The invariant that makes scoping safe. If these ever stop being included, a
// tree-wide test can be broken by a change in a package the scope excluded --
// which is exactly the defect OR-425 was careful not to introduce: a shipped
// agent name in an internal/web doc comment, caught only by a test in
// internal/actors.
func TestTheTreeWideInvariantsAreAlwaysInScope(t *testing.T) {
	dir := newRepo(t)
	write(t, dir, "internal/thing/thing.go", "package thing\n\nfunc F() int { return 1 }\n")
	commit(t, dir, "base")
	base := head(t, dir)
	write(t, dir, "internal/thing/thing.go", "package thing\n\nfunc F() int { return 2 }\n")

	got := ScopeFor(dir, base)
	if got.Full {
		t.Fatalf("expected a scoped run, got the full suite: %s", got.Why)
	}
	for _, want := range alwaysRun {
		if !contains(got.Packages, want) {
			t.Errorf("scope omits the always-run package %s\ngot: %v", want, got.Packages)
		}
	}
}

// A package that IMPORTS the changed one has to be in scope: that is the
// whole reason the scope is computed from the import graph rather than from
// the changed directories alone.
func TestAnImporterOfTheChangedPackageIsInScope(t *testing.T) {
	dir := newRepo(t)
	write(t, dir, "internal/leaf/leaf.go", "package leaf\n\nfunc V() int { return 1 }\n")
	write(t, dir, "internal/user/user.go",
		"package user\n\nimport \"example.com/m/internal/leaf\"\n\nfunc W() int { return leaf.V() }\n")
	commit(t, dir, "base")
	base := head(t, dir)
	write(t, dir, "internal/leaf/leaf.go", "package leaf\n\nfunc V() int { return 2 }\n")

	got := ScopeFor(dir, base)
	if got.Full {
		t.Fatalf("expected a scoped run, got the full suite: %s", got.Why)
	}
	if !contains(got.Packages, "example.com/m/internal/user") {
		t.Errorf("scope omits the importer of the changed package\ngot: %v", got.Packages)
	}
}

// A non-Go change -- scripts/test.sh, a workflow, an embedded asset -- has a
// blast radius no import edge describes. The honest answer is everything.
func TestANonGoChangeWidensToTheFullSuite(t *testing.T) {
	dir := newRepo(t)
	write(t, dir, "internal/thing/thing.go", "package thing\n")
	commit(t, dir, "base")
	base := head(t, dir)
	write(t, dir, "scripts/test.sh", "#!/bin/sh\necho changed\n")

	got := ScopeFor(dir, base)
	if !got.Full {
		t.Errorf("a non-Go change must run the full suite, got scope %v", got.Packages)
	}
	if !strings.Contains(got.Why, "build-affecting") {
		t.Errorf("the reason should name the cause, got %q", got.Why)
	}
}

// Uncommitted work counts. QA runs before the commit is pushed, so a scope
// reading only committed changes would miss the edit under test.
func TestUncommittedWorkIsScoped(t *testing.T) {
	dir := newRepo(t)
	write(t, dir, "internal/thing/thing.go", "package thing\n")
	commit(t, dir, "base")
	base := head(t, dir)
	write(t, dir, "internal/fresh/fresh.go", "package fresh\n")

	got := ScopeFor(dir, base)
	if got.Full {
		t.Fatalf("expected a scoped run, got the full suite: %s", got.Why)
	}
	if !contains(got.Packages, "example.com/m/internal/fresh") {
		t.Errorf("an untracked new package must be in scope\ngot: %v", got.Packages)
	}
}

// Prose cannot fail a Go test, and widening on every documentation change
// would be the same as never having scoped at all.
func TestAMarkdownChangeDoesNotWidenTheScope(t *testing.T) {
	dir := newRepo(t)
	write(t, dir, "internal/thing/thing.go", "package thing\n")
	commit(t, dir, "base")
	base := head(t, dir)
	write(t, dir, "internal/thing/thing.go", "package thing\n\nfunc F() {}\n")
	write(t, dir, "README.md", "# changed\n")

	got := ScopeFor(dir, base)
	if got.Full {
		t.Errorf("a markdown change must not widen the scope: %s", got.Why)
	}
}

// ...with one exception, and it is a real package: docs/decisions has tests
// that parse the decision records, so an ADR edit really can turn a suite red.
func TestADecisionRecordDoesWidenTheScope(t *testing.T) {
	dir := newRepo(t)
	write(t, dir, "internal/thing/thing.go", "package thing\n")
	commit(t, dir, "base")
	base := head(t, dir)
	write(t, dir, "docs/decisions/0001-something.md", "# a decision\n")

	got := ScopeFor(dir, base)
	if !got.Full {
		t.Errorf("a decision record is read by a test, so it must widen: got %v", got.Packages)
	}
}

// Degrading is allowed; degrading silently is not. Every full-suite verdict
// has to say why, or a narrowed check is unauditable.
func TestAFullVerdictAlwaysSaysWhy(t *testing.T) {
	got := ScopeFor(t.TempDir(), "nope")
	if !got.Full {
		t.Fatal("a directory that is not a repository must widen to the full suite")
	}
	if strings.TrimSpace(got.Why) == "" {
		t.Error("a full-suite verdict with no reason cannot be audited")
	}
}

// --- helpers ---

func newRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go is not on PATH")
	}
	dir := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q"},
		{"config", "user.email", "t@example.com"},
		{"config", "user.name", "T"},
	} {
		if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	write(t, dir, "go.mod", "module example.com/m\n\ngo 1.21\n")
	return dir
}

func write(t *testing.T, dir, rel, content string) {
	t.Helper()
	full := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func commit(t *testing.T, dir, msg string) {
	t.Helper()
	for _, args := range [][]string{{"add", "-A"}, {"commit", "-q", "-m", msg}} {
		if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
}

func head(t *testing.T, dir string) string {
	t.Helper()
	out, err := exec.Command("git", "-C", dir, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(out))
}

func contains(hay []string, needle string) bool {
	for _, h := range hay {
		if h == needle {
			return true
		}
	}
	return false
}

// DetectScoped hands the script the packages rather than letting it run
// everything (OR-425). The script's own --scope flag, so the repository keeps
// deciding what a scoped run means.
func TestDetectScopedPassesTheScopeToTheScript(t *testing.T) {
	dir := newRepo(t)
	write(t, dir, "scripts/test.sh", "#!/bin/sh\nexit 0\n")
	if err := os.Chmod(filepath.Join(dir, "scripts", "test.sh"), 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, dir, "internal/thing/thing.go", "package thing\n")
	commit(t, dir, "base")
	base := head(t, dir)
	write(t, dir, "internal/thing/thing.go", "package thing\n\nfunc F() {}\n")

	argv, sc, err := DetectScoped(dir, base, 2)
	if err != nil {
		t.Fatal(err)
	}
	if sc.Full {
		t.Fatalf("expected a scoped run: %s", sc.Why)
	}
	last := argv[len(argv)-1]
	if !strings.HasPrefix(last, "--scope=") {
		t.Fatalf("the script was not told the scope; argv = %v", argv)
	}
	if !strings.Contains(last, "internal/actors") {
		t.Errorf("the always-run invariants must reach the script: %s", last)
	}
}

// A full-suite verdict must NOT be narrowed. Running too much is slow;
// running too little is wrong.
func TestDetectScopedFallsBackToEverythingOnAFullVerdict(t *testing.T) {
	dir := newRepo(t)
	write(t, dir, "scripts/test.sh", "#!/bin/sh\nexit 0\n")
	if err := os.Chmod(filepath.Join(dir, "scripts", "test.sh"), 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, dir, "internal/thing/thing.go", "package thing\n")
	commit(t, dir, "base")
	base := head(t, dir)
	// A build-affecting change: the scope must widen.
	write(t, dir, "scripts/test.sh", "#!/bin/sh\necho changed\nexit 0\n")

	argv, sc, err := DetectScoped(dir, base, 2)
	if err != nil {
		t.Fatal(err)
	}
	if !sc.Full {
		t.Fatal("a build-affecting change must widen to the full suite")
	}
	for _, a := range argv {
		if strings.HasPrefix(a, "--scope=") {
			t.Errorf("a full run must not be narrowed; argv = %v", argv)
		}
	}
}

// No base to diff against is not a licence to guess.
func TestDetectScopedWithNoBaseRunsEverything(t *testing.T) {
	dir := newRepo(t)
	write(t, dir, "scripts/test.sh", "#!/bin/sh\nexit 0\n")
	if err := os.Chmod(filepath.Join(dir, "scripts", "test.sh"), 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, dir, "internal/thing/thing.go", "package thing\n")
	commit(t, dir, "base")

	argv, sc, err := DetectScoped(dir, "", 2)
	if err != nil {
		t.Fatal(err)
	}
	if !sc.Full {
		t.Errorf("with no base, the honest answer is everything: %s", sc.Why)
	}
	for _, a := range argv {
		if strings.HasPrefix(a, "--scope=") {
			t.Errorf("argv must not carry a scope; got %v", argv)
		}
	}
}
