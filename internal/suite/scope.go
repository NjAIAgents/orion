package suite

// Scoping a run to the change (OR-425).
//
// WHY. Every QA stage ran scripts/test.sh end to end -- gofmt, build, vet,
// all 54 packages, the race detector and the coverage floor -- whatever the
// ticket touched. Then the batch pushed and CI ran the identical script
// again. collect runs no tests locally, so CI is the real gate and the
// per-ticket local run was duplicated work on the machine least suited to it:
// `go test ./internal/web/` is 1.2s against minutes for the script, and for a
// ticket that only declared three structs the other 53 packages could not
// have been affected.
//
// It also caused false reds. Three of six tickets in one evening went red and
// then passed with ZERO fix rounds -- nothing repaired, a minute apart. That
// is the contention scripts/test.sh already documents under OR-332: process
// heavy packages starving each other, "the tests that lose are the ones with
// a clock in them". Its fix bounds packages WITHIN a run; nothing bounded
// four agents each starting their own.
//
// WHAT THIS IS NOT. It is not a narrower gate. The full suite stays mandatory
// in CI on every batch push -- this moves where the whole repository check
// happens, never whether it happens. And it is not a guess: the package set
// comes from `go list`, so the same diff yields the same set every time.

import (
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// alwaysRun are packages whose tests assert over the WHOLE tree, so a change
// anywhere can break them and a scoped run must include them regardless.
//
// This list is the reason scoping is safe. The one genuine defect found in
// the evening that produced OR-425 was a shipped agent name in an
// internal/web doc comment, caught by TestNoDefaultNameAppearsOutsideTheRegistry
// in internal/actors -- a test in a package the ticket never touched, failing
// on a file it did. A scope without these would have shipped it.
//
// Found by looking for tests that walk the source tree rather than by
// intuition: each of these contains a filepath.Walk or WalkDir over the
// repository. Adding a package here is cheap; leaving one out is a defect
// that reaches CI, so the bias is to include.
var alwaysRun = []string{
	"./internal/actors/",  // no shipped agent name outside the registry
	"./internal/toolkit/", // comment references resolve (OR-295)
	"./internal/work/",    // residue, QA and work-tree invariants
	"./internal/collect/", // fixture invariants
	"./internal/tracker/", // JQL invariants
}

// errNoPackages says the changed directories held no Go package -- a new
// directory with only fixtures, say. Not a failure: the caller widens to the
// whole suite, which is the safe direction.
var errNoPackages = errNoPkgs{}

type errNoPkgs struct{}

func (errNoPkgs) Error() string { return "the changed directories hold no Go package" }

// Scope is the package set a QA run should cover, and how it was decided.
type Scope struct {
	// Packages are `go test` arguments -- import paths or ./dir/ patterns.
	// Empty means the caller should run everything.
	Packages []string
	// Why is one line for the log. A scoped run that cannot say what it
	// scoped to is a narrower check nobody can audit.
	Why string
	// Full is set when scoping was not possible and the caller must run the
	// whole suite. Degrading is always allowed; degrading silently is not,
	// which is why this is a field rather than an empty Packages.
	Full bool
}

// ScopeFor works out which packages a change can possibly have broken: the
// packages holding changed files, everything that imports them transitively,
// and the always-run invariants.
//
// dir is a git worktree. base is what to diff against -- the branch point,
// usually "develop".
//
// ANY doubt returns Full. A scope that silently narrowed on a bad diff, an
// unparsable import graph or a changed file outside a package would turn a
// gate into a formality, and the failure would be invisible: the suite still
// says green.
func ScopeFor(dir, base string) Scope {
	changed, err := changedFiles(dir, base)
	if err != nil || len(changed) == 0 {
		return Scope{Full: true, Why: "could not read the changed files, so the whole suite runs"}
	}

	// A change the import graph cannot describe widens to everything:
	// scripts/test.sh itself, a CI workflow, a go.mod requirement, an
	// embedded asset. None of those are expressible as an import edge.
	//
	// Prose is the exception, and it has to be, or the scope would be full
	// on every documentation change forever -- which is the same as not
	// having scoped at all. A .md file cannot fail a Go test.
	//
	// The list is deliberately of what is SAFE to ignore rather than of what
	// is dangerous: a file type nobody has thought about lands on the
	// widening side, where being wrong costs time instead of correctness.
	for _, f := range changed {
		if strings.HasSuffix(f, ".go") || ignorableForBuild(f) {
			continue
		}
		return Scope{Full: true, Why: "a build-affecting file changed (" + f + "), whose blast radius the import graph cannot describe"}
	}

	dirs := packageDirs(changed)
	if len(dirs) == 0 {
		return Scope{Full: true, Why: "no package directory could be derived from the changed files"}
	}

	pkgs, err := dependents(dir, dirs)
	if err != nil {
		return Scope{Full: true, Why: "the import graph could not be read (" + err.Error() + "), so the whole suite runs"}
	}

	set := map[string]bool{}
	for _, p := range pkgs {
		set[p] = true
	}
	for _, p := range alwaysRun {
		set[p] = true
	}
	out := make([]string, 0, len(set))
	for p := range set {
		out = append(out, p)
	}
	sort.Strings(out)

	return Scope{
		Packages: out,
		Why: "scoped to " + strings.Join(dirs, ", ") + " plus importers and the tree-wide invariants; " +
			"the full suite still runs in CI",
	}
}

// ignorableForBuild reports whether a non-Go file provably cannot change what
// a Go test does.
//
// Prose only, and only prose. A .json or .yml can be a fixture or a workflow;
// a .txt can be testdata. Markdown is the one extension in this repository
// that is read by people rather than by code -- and even that has an
// exception, because docs/decisions has a test that reads the decision
// records themselves.
func ignorableForBuild(path string) bool {
	if !strings.HasSuffix(path, ".md") {
		return false
	}
	// docs/decisions is compiled: a test there parses the ADRs, so an ADR
	// edit really can turn a suite red. Found by grep rather than by
	// assumption -- `go list` reports docs/decisions as a package.
	return !strings.HasPrefix(filepath.ToSlash(path), "docs/decisions/")
}

// changedFiles lists what this worktree changed against base, committed or
// not. Uncommitted work counts: QA runs before the commit is pushed, and a
// scope that ignored the working tree would miss the very edit under test.
func changedFiles(dir, base string) ([]string, error) {
	merge, err := git(dir, "merge-base", "HEAD", base)
	if err != nil {
		// No merge base -- an unrelated history, or base does not exist here.
		return nil, err
	}
	tracked, err := git(dir, "diff", "--name-only", strings.TrimSpace(merge))
	if err != nil {
		return nil, err
	}
	untracked, err := git(dir, "ls-files", "--others", "--exclude-standard")
	if err != nil {
		return nil, err
	}
	return lines(tracked + "\n" + untracked), nil
}

// packageDirs turns changed file paths into ./dir/ patterns, deduplicated.
func packageDirs(files []string) []string {
	set := map[string]bool{}
	for _, f := range files {
		d := filepath.ToSlash(filepath.Dir(f))
		if d == "." {
			// A file at the repository root belongs to the root package,
			// which `go list ./...` names as the module path rather than a
			// directory pattern. "./" is the pattern for it.
			set["./"] = true
			continue
		}
		set["./"+d+"/"] = true
	}
	out := make([]string, 0, len(set))
	for d := range set {
		out = append(out, d)
	}
	sort.Strings(out)
	return out
}

// dependents returns the changed packages plus every package that imports
// them, transitively.
//
// `go list` computes this, rather than Orion parsing imports: the toolchain
// already owns the import graph, and a second implementation would disagree
// with it on build tags, test-only imports and vendoring -- disagreements
// that would show up as a package quietly missing from the scope.
func dependents(dir string, changedDirs []string) ([]string, error) {
	// The full graph, one line per package: its path then its dependencies.
	// -deps=false keeps it to direct imports; transitivity is closed below,
	// which is cheaper than asking `go list` for every package's full dep
	// set and gives the same answer.
	out, err := goList(dir, "-f", "{{.ImportPath}} {{join .Imports \" \"}} {{join .TestImports \" \"}}", "./...")
	if err != nil {
		return nil, err
	}
	imports := map[string][]string{}
	for _, line := range lines(out) {
		parts := strings.Fields(line)
		if len(parts) == 0 {
			continue
		}
		imports[parts[0]] = parts[1:]
	}

	// The changed directories, as import paths.
	//
	// ONE AT A TIME, and a failure on one is not a failure of the set. A
	// directory can hold changed files and no Go package at all -- the
	// repository root when only a README moved, a directory of fixtures, a
	// new directory whose .go file is not written yet. `go list` exits
	// non-zero for those ("no Go files in ..."), and asking for every
	// directory in one call would lose the packages that DID resolve
	// alongside the one that did not.
	var seeds []string
	for _, d := range changedDirs {
		out, err := goList(dir, "-f", "{{.ImportPath}}", d)
		if err != nil {
			continue // not a package; nothing to test there
		}
		seeds = append(seeds, lines(out)...)
	}
	if len(seeds) == 0 {
		return nil, errNoPackages
	}

	want := map[string]bool{}
	for _, s := range seeds {
		want[s] = true
	}
	// Close over importers until nothing new is added. The graph is ~54
	// packages, so this is a handful of passes over a small map.
	for changedThisPass := true; changedThisPass; {
		changedThisPass = false
		for pkg, deps := range imports {
			if want[pkg] {
				continue
			}
			for _, d := range deps {
				if want[d] {
					want[pkg] = true
					changedThisPass = true
					break
				}
			}
		}
	}

	out2 := make([]string, 0, len(want))
	for p := range want {
		out2 = append(out2, p)
	}
	return out2, nil
}

func goList(dir string, args ...string) (string, error) {
	full := append([]string{"list"}, args...)
	cmd := exec.Command("go", full...)
	cmd.Dir = dir
	b, err := cmd.Output()
	return string(b), err
}

func git(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	b, err := cmd.Output()
	return string(b), err
}

func lines(s string) []string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			out = append(out, l)
		}
	}
	return out
}
