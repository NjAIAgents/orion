package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/orion-sdlc/orion/internal/fanout"
)

// These drive `orion fan` as a real subprocess against this repository's own
// import graph, the same way TestTheRealImportGraphDecidesTheVerdict in
// internal/fanout exercises the validator directly. What is not covered
// there is the CLI wrapper: exploreWorkspace's requirement, fanGiveUp's and
// fanRefuse's actual stderr text, and the process exit code -- none of which
// a caller of fanout.Validate ever sees, because cmd/orion/fan.go is where
// those words are written.

// fanWorkspace lays out a minimal ORION_HOME workspace the same way
// TestExploreWorkspaceOpensARealWorkspace does, so `orion fan` (which opens
// its workspace via ORION_WORKSPACE, exactly as `orion explore` does) has
// somewhere real to open.
func fanWorkspace(t *testing.T) (home, id string) {
	t.Helper()
	home = t.TempDir()
	id = "fan-cli-1"
	dir := filepath.Join(home, "projects", id)
	if err := os.MkdirAll(filepath.Join(dir, ".orion"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".orion", "task.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	return home, id
}

func writePlan(t *testing.T, dir string, p fanout.Plan) string {
	t.Helper()
	b, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "plan.json")
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func assignment(pkg, task string) fanout.Assignment {
	return fanout.Assignment{Package: pkg, Task: task}
}

// `orion fan` is for an agent inside a run Orion started, exactly like
// `orion explore` -- see exploreWorkspace. Without ORION_WORKSPACE there is
// nothing to dispatch children into, and that must fail loudly rather than
// silently picking a workspace.
func TestCLIFanRequiresAWorkspace(t *testing.T) {
	bin := orionBinary(t)
	planPath := writePlan(t, t.TempDir(), fanout.Plan{Assignments: []fanout.Assignment{
		assignment("./internal/a", "x"), assignment("./internal/b", "y"),
	}})

	cmd := exec.Command(bin, "fan", planPath)
	cmd.Env = append(os.Environ(), "ORION_WORKSPACE=", "ORION_HOME="+t.TempDir())
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("expected a non-zero exit with no workspace, got success:\n%s", out)
	}
	if ee, ok := err.(*exec.ExitError); !ok || ee.ExitCode() != 1 {
		t.Errorf("exit code = %v, want 1 (fanGiveUp's contract)", err)
	}
	if !strings.Contains(string(out), "Work the packages yourself") {
		t.Errorf("refusal does not tell the caller to fall back to doing it itself:\n%s", out)
	}
}

// A plan file that is not JSON at all must fail the same fanGiveUp way as a
// missing workspace, not crash the process.
func TestCLIFanRefusesAPlanThatIsNotJSON(t *testing.T) {
	bin := orionBinary(t)
	home, id := fanWorkspace(t)
	planPath := filepath.Join(t.TempDir(), "plan.json")
	if err := os.WriteFile(planPath, []byte("not json at all"), 0o600); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(bin, "fan", planPath)
	cmd.Env = append(os.Environ(), "ORION_HOME="+home, "ORION_WORKSPACE="+id)
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("expected a non-zero exit for an unparseable plan, got success:\n%s", out)
	}
	if !strings.Contains(string(out), "not the expected JSON") {
		t.Errorf("refusal does not say the plan could not be parsed:\n%s", out)
	}
}

// The acceptance criterion at the CLI boundary: a plan whose packages import
// one another is refused, told to work serially, and told not to re-propose
// -- because the check is deterministic and a retried plan gets the same
// answer. cmd/orion imports internal/supervisor in this repository, so this
// exercises the real `go list`, not a stand-in graph.
func TestCLIFanRefusesCoupledPackagesAndSaysWorkSerially(t *testing.T) {
	bin := orionBinary(t)
	home, id := fanWorkspace(t)
	repoRoot, err := topLevel(".")
	if err != nil {
		t.Skipf("not inside a git repository: %v", err)
	}
	planPath := writePlan(t, t.TempDir(), fanout.Plan{Assignments: []fanout.Assignment{
		assignment("./cmd/orion", "x"), assignment("./internal/supervisor", "y"),
	}})

	cmd := exec.Command(bin, "fan", "--repo", repoRoot, planPath)
	cmd.Env = append(os.Environ(), "ORION_HOME="+home, "ORION_WORKSPACE="+id)
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("expected a non-zero exit for coupled packages, got success:\n%s", out)
	}
	if ee, ok := err.(*exec.ExitError); !ok || ee.ExitCode() != 1 {
		t.Errorf("exit code = %v, want 1 (fanRefuse's contract)", err)
	}
	text := string(out)
	for _, want := range []string{
		"runs serially, not concurrently",
		"depends on",
		"Do not re-propose",
		"the check is deterministic",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("refusal missing %q:\n%s", want, text)
		}
	}
}

// A package spec that resolves to nothing -- a typo, a directory with no Go
// files -- must be refused the same deterministic-serial way as an import
// edge, not crash or hang the process.
func TestCLIFanRefusesAnUnresolvablePackageWithoutCrashing(t *testing.T) {
	bin := orionBinary(t)
	home, id := fanWorkspace(t)
	repoRoot, err := topLevel(".")
	if err != nil {
		t.Skipf("not inside a git repository: %v", err)
	}
	planPath := writePlan(t, t.TempDir(), fanout.Plan{Assignments: []fanout.Assignment{
		assignment("./internal/fanout", "x"),
		assignment("./internal/this-package-does-not-exist", "y"),
	}})

	cmd := exec.Command(bin, "fan", "--repo", repoRoot, planPath)
	cmd.Env = append(os.Environ(), "ORION_HOME="+home, "ORION_WORKSPACE="+id)
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("expected a non-zero exit for an unresolvable package, got success:\n%s", out)
	}
	if !strings.Contains(string(out), "could not resolve") {
		t.Errorf("refusal does not say the package could not be resolved:\n%s", out)
	}
}
