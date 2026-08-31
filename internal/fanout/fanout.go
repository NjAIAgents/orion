// Package fanout decides whether an implementation run may be split across
// concurrent subagents, and refuses when it may not.
//
// The implementer works files serially, which is wasted wall time when a
// ticket touches genuinely independent code. Splitting by FILE looks like the
// obvious fix and is not: per-file ownership solves the write race and
// neither of the two failures that actually bite.
//
//	builds are not isolated   the compiler compiles the PACKAGE. A subagent
//	                          running tests to check its own work builds
//	                          against its peers' half-written files and sees
//	                          failures that are not its own.
//	signature coupling        file A defines a function, file B calls it. Agent
//	                          B writes the call site against a signature agent A
//	                          is still changing -- the Idea-redeclared and
//	                          duplicate-CreateProject collisions seen at merge
//	                          time, moved to write time, where there is no
//	                          conflict marker to warn anyone.
//
// So the unit is the Go package: it is the compilation unit, therefore the
// real isolation boundary. Two files in one package are coupled by
// construction; two files in different packages each build on their own, and
// the remaining coupling is the import edge, which is visible and enumerable.
//
// The implementer PROPOSES and this package DISPOSES. Nothing here asks a
// model anything: the checks are set membership and a dependency graph, so a
// wrong split fails visibly rather than corrupting a tree silently. Any
// failure means serial, with no negotiation and no retry with a better
// argument -- the same shape as the require_plan_before_edit gate.
//
// See docs/decisions/0016-fan-implementation-by-go-package.md.
package fanout

import (
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"sort"
	"strings"
)

// Assignment is one subagent's whole share of the work.
type Assignment struct {
	// Package names the Go package this subagent owns, in any form `go list`
	// accepts: an import path, or a ./relative directory.
	Package string `json:"package"`
	// Task is what to change there, in full. A task that says "as described
	// above" is unusable: the subagent gets this text and nothing else.
	Task string `json:"task"`
}

// Plan is what the implementer emits.
type Plan struct {
	Assignments []Assignment `json:"assignments"`
}

// ParsePlan reads a plan the implementer wrote.
func ParsePlan(b []byte) (Plan, error) {
	var p Plan
	if err := json.Unmarshal(b, &p); err != nil {
		return p, fmt.Errorf("the plan is not the expected JSON: %w", err)
	}
	return p, nil
}

// Package is what the toolchain says about one assigned package: its
// canonical import path, and every import path it depends on transitively.
type Package struct {
	ImportPath string
	Deps       []string
}

// Resolve answers for one package spec. Injected rather than called directly
// so the validator is a pure function of a graph and can be tested without a
// module on disk -- and so a repository with no Go toolchain degrades to
// serial rather than failing.
type Resolve func(pkg string) (Package, error)

// Verdict is the answer. Serial is the safe outcome and therefore the
// default: every rejection path produces one, and Reason is the sentence the
// implementer is shown.
type Verdict struct {
	Serial bool
	Reason string
	// Packages are the canonical import paths of the admitted assignments,
	// in the order they were proposed. Empty when Serial.
	Packages []string
}

func serial(format string, args ...any) Verdict {
	return Verdict{Serial: true, Reason: fmt.Sprintf(format, args...)}
}

// Validate admits a plan or forces serial execution.
//
// maxWidth is Limits.MaxConcurrentChildren: the fan cannot be wider than the
// number of children this project has agreed to run at once. Checking it here
// rather than letting supervisor.Fan queue the overflow is deliberate -- a fan
// of six under a cap of two is three sequential rounds of writers against one
// tree, which is the isolation argument lost while still paying the
// coordination.
//
// The checks run in a fixed order and the FIRST failure decides, so the reason
// an implementer reads is always the same for the same plan.
func Validate(p Plan, maxWidth int, resolve Resolve) Verdict {
	if len(p.Assignments) < 2 {
		return serial("a fan of %d is not a fan; there is nothing to run concurrently",
			len(p.Assignments))
	}
	if maxWidth < 2 {
		return serial("this project's limits.max_concurrent_children is %d, "+
			"so no fan-out is permitted at all", maxWidth)
	}
	if len(p.Assignments) > maxWidth {
		return serial("%d packages assigned but limits.max_concurrent_children is %d; "+
			"a fan wider than the cap is sequential rounds of writers against one tree, "+
			"which loses the isolation and keeps the coordination",
			len(p.Assignments), maxWidth)
	}

	for i, a := range p.Assignments {
		if strings.TrimSpace(a.Package) == "" {
			return serial("assignment %d names no package", i+1)
		}
		if strings.TrimSpace(a.Task) == "" {
			return serial("assignment %d (%s) carries no task; a subagent is given this text "+
				"and nothing else, so an empty one does nothing", i+1, a.Package)
		}
	}

	// Resolved before the duplicate check, because two spellings of one
	// package -- ./internal/config and github.com/.../internal/config -- are
	// a duplicate that string comparison would admit.
	resolved := make([]Package, len(p.Assignments))
	for i, a := range p.Assignments {
		pkg, err := resolve(a.Package)
		if err != nil {
			return serial("could not resolve %s: %v", a.Package, err)
		}
		if pkg.ImportPath == "" {
			return serial("%s resolved to no import path", a.Package)
		}
		resolved[i] = pkg
	}

	seen := make(map[string]int, len(resolved))
	for i, pkg := range resolved {
		if first, dup := seen[pkg.ImportPath]; dup {
			return serial("assignments %d and %d both own %s; one package has one writer",
				first+1, i+1, pkg.ImportPath)
		}
		seen[pkg.ImportPath] = i
	}

	// The import edge. Transitive, from the full dependency set rather than
	// direct imports only: A importing B through C is still A compiling
	// against B, so a signature B is mid-change still reaches A.
	//
	// Checked in both directions for every pair, and the pairs are walked in
	// order so the reported edge does not depend on map iteration.
	for i, a := range resolved {
		deps := make(map[string]bool, len(a.Deps))
		for _, d := range a.Deps {
			deps[d] = true
		}
		for j, b := range resolved {
			if i == j {
				continue
			}
			if deps[b.ImportPath] {
				return serial("%s depends on %s, and both are assigned; "+
					"an import edge between two writers is a call site written against "+
					"a signature the other is still changing", a.ImportPath, b.ImportPath)
			}
		}
	}

	paths := make([]string, len(resolved))
	for i, pkg := range resolved {
		paths[i] = pkg.ImportPath
	}
	return Verdict{Packages: paths}
}

// GoList resolves through the Go toolchain, from dir.
//
// `.Deps` is `go list`'s RECURSIVE dependency set, which is what the edge
// check wants; `.Imports` would see only the direct ones and admit a plan
// coupled one hop further out.
//
// Every failure -- no toolchain, not a module, a package that does not exist,
// a tree that does not currently parse -- comes back as an error, and the
// validator turns an error into serial. That is the right default: this can
// only ever save the implementer wall time, never be the reason a ticket
// cannot proceed.
func GoList(dir string) Resolve {
	return func(pkg string) (Package, error) {
		cmd := exec.Command("go", "list",
			"-f", `{{.ImportPath}}{{range .Deps}} {{.}}{{end}}`, "--", pkg)
		cmd.Dir = dir
		out, err := cmd.Output()
		if err != nil {
			var ee *exec.ExitError
			if errors.As(err, &ee) && len(ee.Stderr) > 0 {
				return Package{}, fmt.Errorf("go list: %s", firstLine(string(ee.Stderr)))
			}
			return Package{}, fmt.Errorf("go list: %w", err)
		}
		fields := strings.Fields(string(out))
		if len(fields) == 0 {
			return Package{}, fmt.Errorf("go list said nothing about %s", pkg)
		}
		deps := append([]string(nil), fields[1:]...)
		sort.Strings(deps)
		return Package{ImportPath: fields[0], Deps: deps}, nil
	}
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}
