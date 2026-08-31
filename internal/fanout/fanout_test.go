package fanout

import (
	"fmt"
	"strings"
	"testing"
)

// graph is a stand-in for `go list`: a package's transitive dependency set,
// keyed by import path, plus the ./relative spellings that resolve to it.
// Written out rather than derived so a test states the coupling it means to
// test instead of depending on this repository's real import graph.
type graph map[string][]string

func (g graph) resolve(pkg string) (Package, error) {
	path := strings.TrimPrefix(pkg, "./")
	path = strings.TrimPrefix(path, "example.com/m/")
	full := "example.com/m/" + path
	deps, ok := g[full]
	if !ok {
		return Package{}, fmt.Errorf("no Go files in %s", pkg)
	}
	return Package{ImportPath: full, Deps: deps}, nil
}

// independent is the shape the whole design is FOR: two packages that build
// on their own, neither reaching the other.
var independent = graph{
	"example.com/m/internal/a": {"fmt", "strings"},
	"example.com/m/internal/b": {"fmt", "os"},
	"example.com/m/internal/c": {"fmt"},
}

func plan(pairs ...string) Plan {
	var p Plan
	for _, pkg := range pairs {
		p.Assignments = append(p.Assignments, Assignment{Package: pkg, Task: "change it"})
	}
	return p
}

func TestIndependentPackagesAreAdmitted(t *testing.T) {
	v := Validate(plan("./internal/a", "./internal/b"), 4, independent.resolve)
	if v.Serial {
		t.Fatalf("independent packages forced serial: %s", v.Reason)
	}
	want := []string{"example.com/m/internal/a", "example.com/m/internal/b"}
	if len(v.Packages) != len(want) {
		t.Fatalf("packages = %v, want %v", v.Packages, want)
	}
	for i := range want {
		if v.Packages[i] != want[i] {
			t.Errorf("packages[%d] = %q, want %q", i, v.Packages[i], want[i])
		}
	}
}

// The rejection the ticket names explicitly. A depends on B and both are
// assigned: agent B is changing a signature agent A is writing a call site
// against, and there is no conflict marker at write time to warn anyone.
func TestAnImportEdgeBetweenAssignedPackagesForcesSerial(t *testing.T) {
	coupled := graph{
		"example.com/m/internal/a": {"fmt", "example.com/m/internal/b"},
		"example.com/m/internal/b": {"fmt"},
	}
	v := Validate(plan("./internal/a", "./internal/b"), 4, coupled.resolve)
	if !v.Serial {
		t.Fatal("a plan whose packages import one another was admitted")
	}
	for _, want := range []string{"internal/a", "internal/b", "depends on"} {
		if !strings.Contains(v.Reason, want) {
			t.Errorf("reason %q does not mention %q", v.Reason, want)
		}
	}
}

// The edge is rejected whichever way round it is proposed: order in the plan
// is the implementer's choice and must not decide the verdict.
func TestTheImportEdgeIsCaughtInEitherDirection(t *testing.T) {
	coupled := graph{
		"example.com/m/internal/a": {"example.com/m/internal/b"},
		"example.com/m/internal/b": {"fmt"},
	}
	if v := Validate(plan("./internal/b", "./internal/a"), 4, coupled.resolve); !v.Serial {
		t.Fatal("the importer listed second was admitted")
	}
}

// Transitive, not direct. a -> c -> b is still a compiling against b, so the
// dependency set `go list` reports is the right input and `.Imports` would
// not be.
func TestATransitiveEdgeForcesSerialToo(t *testing.T) {
	chain := graph{
		"example.com/m/internal/a": {"example.com/m/internal/c", "example.com/m/internal/b"},
		"example.com/m/internal/b": {},
	}
	if v := Validate(plan("./internal/a", "./internal/b"), 4, chain.resolve); !v.Serial {
		t.Fatal("a transitively coupled pair was admitted")
	}
}

// The acceptance criterion: a ticket whose change runs down a layer -- which
// is what most of Orion's own tickets look like -- lands on serial rather
// than fanning. Named for that case so a regression reads as what it costs.
func TestATicketSpanningCoupledPackagesFallsBackToSerial(t *testing.T) {
	// cmd -> work -> config: one change running down the stack.
	layered := graph{
		"example.com/m/cmd/orion":       {"example.com/m/internal/work", "example.com/m/internal/config"},
		"example.com/m/internal/work":   {"example.com/m/internal/config"},
		"example.com/m/internal/config": {"os"},
	}
	v := Validate(plan("./cmd/orion", "./internal/work", "./internal/config"), 4, layered.resolve)
	if !v.Serial {
		t.Fatalf("a change running down a layer was fanned out: %v", v.Packages)
	}
	if len(v.Packages) != 0 {
		t.Errorf("a serial verdict still named packages: %v", v.Packages)
	}
}

func TestOnePackageIsNotAFan(t *testing.T) {
	if v := Validate(plan("./internal/a"), 4, independent.resolve); !v.Serial {
		t.Fatal("a single assignment was admitted as a fan")
	}
	if v := Validate(Plan{}, 4, independent.resolve); !v.Serial {
		t.Fatal("an empty plan was admitted")
	}
}

// Two spellings of one package are a duplicate. String comparison on what the
// implementer wrote would admit this; comparing canonical import paths does
// not.
func TestAPackageAssignedTwiceForcesSerial(t *testing.T) {
	v := Validate(plan("./internal/a", "example.com/m/internal/a"), 4, independent.resolve)
	if !v.Serial {
		t.Fatal("one package with two writers was admitted")
	}
	if !strings.Contains(v.Reason, "one writer") {
		t.Errorf("reason %q does not say why", v.Reason)
	}
}

func TestAFanWiderThanTheCapForcesSerial(t *testing.T) {
	v := Validate(plan("./internal/a", "./internal/b", "./internal/c"), 2, independent.resolve)
	if !v.Serial {
		t.Fatal("a fan wider than max_concurrent_children was admitted")
	}
	if !strings.Contains(v.Reason, "max_concurrent_children") {
		t.Errorf("reason %q does not name the limit it broke", v.Reason)
	}
	// The same plan at the cap is fine: the check is the cap, not a fixed width.
	if v := Validate(plan("./internal/a", "./internal/b", "./internal/c"), 3, independent.resolve); v.Serial {
		t.Fatalf("a fan exactly at the cap was refused: %s", v.Reason)
	}
}

func TestACapBelowTwoRefusesEveryFan(t *testing.T) {
	if v := Validate(plan("./internal/a", "./internal/b"), 1, independent.resolve); !v.Serial {
		t.Fatal("a fan ran under a cap of one")
	}
}

// A package that will not resolve -- no toolchain, not a module, a path that
// does not exist, a tree that does not currently parse -- is serial, not an
// error. This can only ever save wall time; it must never be why a ticket
// cannot proceed.
func TestAnUnresolvablePackageForcesSerial(t *testing.T) {
	v := Validate(plan("./internal/a", "./internal/nope"), 4, independent.resolve)
	if !v.Serial {
		t.Fatal("a plan naming a package that does not exist was admitted")
	}
	if !strings.Contains(v.Reason, "internal/nope") {
		t.Errorf("reason %q does not name the package that failed", v.Reason)
	}
}

func TestAnEmptyTaskForcesSerial(t *testing.T) {
	p := plan("./internal/a", "./internal/b")
	p.Assignments[1].Task = "   "
	v := Validate(p, 4, independent.resolve)
	if !v.Serial {
		t.Fatal("an assignment with no task was admitted")
	}
	if !strings.Contains(v.Reason, "no task") {
		t.Errorf("reason %q does not say what was missing", v.Reason)
	}
}

func TestAnEmptyPackageForcesSerial(t *testing.T) {
	p := plan("./internal/a", "")
	if v := Validate(p, 4, independent.resolve); !v.Serial {
		t.Fatal("an assignment naming no package was admitted")
	}
}

func TestParsePlanReadsWhatTheImplementerWrites(t *testing.T) {
	p, err := ParsePlan([]byte(`{"assignments":[
	  {"package":"./internal/a","task":"add the validator"},
	  {"package":"./internal/b","task":"call it"}]}`))
	if err != nil {
		t.Fatalf("ParsePlan: %v", err)
	}
	if len(p.Assignments) != 2 || p.Assignments[0].Package != "./internal/a" ||
		p.Assignments[1].Task != "call it" {
		t.Fatalf("parsed %+v", p.Assignments)
	}
	if _, err := ParsePlan([]byte("not json")); err == nil {
		t.Error("ParsePlan accepted something that is not JSON")
	}
}

// GoList is the real resolver, exercised against this module so the format
// string and the meaning of .Deps are checked rather than assumed.
func TestGoListResolvesThisRepositorysOwnPackages(t *testing.T) {
	resolve := GoList("..")
	pkg, err := resolve("./fanout")
	if err != nil {
		t.Skipf("no usable Go toolchain here: %v", err)
	}
	if pkg.ImportPath != "github.com/orion-sdlc/orion/internal/fanout" {
		t.Errorf("import path = %q", pkg.ImportPath)
	}
	// encoding/json is imported directly; its own transitive deps prove the
	// set is recursive rather than the direct imports only.
	var direct, transitive bool
	for _, d := range pkg.Deps {
		switch d {
		case "encoding/json":
			direct = true
		case "unicode/utf16":
			transitive = true
		}
	}
	if !direct {
		t.Errorf("deps do not include encoding/json: %v", pkg.Deps)
	}
	if !transitive {
		t.Errorf("deps look non-transitive; encoding/json's own deps are absent: %v", pkg.Deps)
	}
	if _, err := resolve("./this-package-does-not-exist"); err == nil {
		t.Error("GoList resolved a package that does not exist")
	}
}
