package ciscaffold

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func repo(t *testing.T, files ...string) string {
	t.Helper()
	d := t.TempDir()
	for _, f := range files {
		if err := os.WriteFile(filepath.Join(d, f), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return d
}

func TestDetectPicksTheToolchainFromMarkerFiles(t *testing.T) {
	for _, tc := range []struct {
		files []string
		want  Stack
	}{
		{[]string{"go.mod"}, StackGo},
		{[]string{"pyproject.toml"}, StackPython},
		{[]string{"requirements.txt"}, StackPython},
		{[]string{"package.json"}, StackNode},
		{nil, StackUnknown},
		// A Go repository with package.json for tooling is still a Go
		// repository; running npm test in it would prove nothing.
		{[]string{"go.mod", "package.json"}, StackGo},
	} {
		if got := Detect(repo(t, tc.files...)); got != tc.want {
			t.Errorf("Detect(%v) = %s, want %s", tc.files, got, tc.want)
		}
	}
}

// The generated script must actually run. A template with a shell syntax
// error is invisible until CI, where it surfaces as a failing check on a
// branch that is fine.
func TestGeneratedScriptsAreValidShell(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	for _, s := range []Stack{StackGo, StackPython, StackNode, StackUnknown} {
		dir := t.TempDir()
		p := filepath.Join(dir, "test.sh")
		if err := os.WriteFile(p, []byte(scriptFor(s)), 0o755); err != nil {
			t.Fatal(err)
		}
		if out, err := exec.Command("bash", "-n", p).CombinedOutput(); err != nil {
			t.Errorf("%s script is not valid shell: %v\n%s", s, err, out)
		}
	}
}

// An unrecognised toolchain gets a script that FAILS. A stub exiting 0 would
// be worse than no script: CI would report green having run nothing, and
// every pull request would carry a check proving only that the check exists.
func TestAnUnknownToolchainProducesAFailingStubNotAPassingOne(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	dir := repo(t)
	res, err := Ensure(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !NeedsAttention(res) {
		t.Fatal("an unknown toolchain must be flagged for a human to finish")
	}
	cmd := exec.Command("bash", filepath.Join(dir, "scripts", "test.sh"))
	cmd.Dir = dir
	if err := cmd.Run(); err == nil {
		t.Fatal("the stub exited 0; CI would be green having run nothing")
	}
}

// Nothing is overwritten. A repository that already has a test script has
// one for reasons this package cannot see.
func TestAnExistingScriptIsNeverOverwritten(t *testing.T) {
	dir := repo(t, "go.mod")
	if err := os.MkdirAll(filepath.Join(dir, "scripts"), 0o755); err != nil {
		t.Fatal(err)
	}
	mine := "#!/bin/sh\necho mine\n"
	p := filepath.Join(dir, "scripts", "test.sh")
	if err := os.WriteFile(p, []byte(mine), 0o755); err != nil {
		t.Fatal(err)
	}

	res, err := Ensure(dir)
	if err != nil {
		t.Fatal(err)
	}
	if res.ScriptCreated {
		t.Error("reported creating a script that already existed")
	}
	b, _ := os.ReadFile(p)
	if string(b) != mine {
		t.Fatal("an existing test script was overwritten")
	}
}

// A second workflow running the same suite doubles the CI bill and makes the
// checks list ambiguous about which verdict Orion should act on.
func TestAnExistingWorkflowStopsASecondFromBeingAdded(t *testing.T) {
	dir := repo(t, "go.mod")
	flows := filepath.Join(dir, ".github", "workflows")
	if err := os.MkdirAll(flows, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(flows, "existing.yml"), []byte("name: theirs"), 0o644); err != nil {
		t.Fatal(err)
	}

	res, err := Ensure(dir)
	if err != nil {
		t.Fatal(err)
	}
	if res.FlowCreated {
		t.Fatal("added a competing workflow")
	}
	if _, err := os.Stat(filepath.Join(flows, "orion-ci.yml")); err == nil {
		t.Fatal("orion-ci.yml was written despite an existing workflow")
	}
	if !strings.Contains(strings.Join(res.Notes, " "), "scripts/test.sh") {
		t.Errorf("the note must say what to do instead: %v", res.Notes)
	}
}

// The whole contract is that CI runs the SAME script a person runs. A
// workflow that inlines its own commands can drift from the script, and the
// drift shows up as a pull request green in CI and broken locally.
func TestTheWorkflowRunsTheScriptRatherThanItsOwnCommands(t *testing.T) {
	for _, s := range []Stack{StackGo, StackPython, StackNode} {
		flow := workflowFor(s)
		if !strings.Contains(flow, "./scripts/test.sh") {
			t.Errorf("%s workflow does not call scripts/test.sh", s)
		}
		// Orion reads the checks list to decide whether a branch may merge,
		// so the job's name is load-bearing, not decoration.
		if !strings.Contains(flow, "name: test") {
			t.Errorf("%s workflow has no stable job name for Orion to read", s)
		}
		if !strings.Contains(flow, "cancel-in-progress") {
			t.Errorf("%s workflow lacks concurrency control; a force-push mid-review "+
				"would leave an older run reporting on code that is gone", s)
		}
	}
}

// github.ref alone cannot collapse a push and its pull_request into one
// concurrency group: push uses refs/heads/<branch>, pull_request uses
// refs/pull/<n>/merge, so the two never cancel each other and a branch with
// an open PR runs the whole matrix twice on the same SHA (OR-172). The
// group must fall back to the ref only when there is no PR number.
func TestConcurrencyGroupCollapsesPushAndPullRequest(t *testing.T) {
	for _, s := range []Stack{StackGo, StackPython, StackNode} {
		flow := workflowFor(s)
		if !strings.Contains(flow, "github.event.pull_request.number") {
			t.Errorf("%s workflow's concurrency group has no pull_request number fallback, "+
				"so a push and its PR land in different groups and both run: %s", s, flow)
		}
	}
}

// The scan is scaffolded, not hand-added. Editing one repository's workflow
// fixes that repository and leaves every adopted project unscanned.
func TestEveryAdoptedProjectGetsTheSecretScan(t *testing.T) {
	dir := repo(t, "go.mod")
	res, err := Ensure(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !res.ScanCreated {
		t.Fatalf("no secret scan was scaffolded: %+v", res)
	}
	b, err := os.ReadFile(filepath.Join(dir, ".github", "workflows", "orion-secret-scan.yml"))
	if err != nil {
		t.Fatal(err)
	}
	flow := string(b)
	// Matched against the run: lines rather than the file, so a comment
	// mentioning a flag cannot stand in for the flag being passed.
	var steps string
	for _, line := range strings.Split(flow, "\n") {
		if t := strings.TrimSpace(line); strings.HasPrefix(t, "run:") || strings.HasPrefix(t, "fetch-depth:") {
			steps += t + "\n"
		}
	}
	for _, want := range []struct{ frag, why string }{
		{"@" + gitleaksVersion, "the scanner version must be pinned"},
		{"go install " + gitleaksModule, "the download must be verified, not curled and run"},
		{"--redact", "CI logs are as readable as the repository; findings must be redacted"},
		{"--verbose", "a build that fails without saying where the secret is cannot be acted on"},
		{"fetch-depth: 0", "a diff-only scan misses anything committed before the scanner existed"},
		{"--exit-code 1", "a hit must fail the build; CI has no implementer to negotiate with"},
	} {
		if !strings.Contains(steps, want.frag) {
			t.Errorf("scan workflow steps are missing %q: %s\n%s", want.frag, want.why, steps)
		}
	}
}

// The scan must land in a repository that ALREADY has workflows -- which is
// every repository worth adopting, and Orion's own. The "another workflow
// exists, so add none" rule guards against running the same test suite
// twice; applying it to the scan would leave exactly those projects
// unscanned.
func TestTheScanIsAddedEvenWhenAnotherWorkflowAlreadyExists(t *testing.T) {
	dir := repo(t, "go.mod")
	flows := filepath.Join(dir, ".github", "workflows")
	if err := os.MkdirAll(flows, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(flows, "ci.yml"), []byte("name: ci\njobs: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	res, err := Ensure(dir)
	if err != nil {
		t.Fatal(err)
	}
	if res.FlowCreated {
		t.Error("added a competing test workflow")
	}
	if !res.ScanCreated {
		t.Fatal("skipped the secret scan because an unrelated workflow existed")
	}
}

// A project that already scans gets no second scanner (A5). Two scanners
// report the same findings under two check names, and neither is the one
// anybody configured.
func TestAnExistingScannerIsUsedRatherThanASecondAdded(t *testing.T) {
	for _, tc := range []struct{ name, path, body string }{
		{"gitleaks config", ".gitleaks.toml", "[extend]\n"},
		{"detect-secrets baseline", ".secrets.baseline", "{}"},
		{"a workflow running trufflehog", ".github/workflows/sec.yml", "name: sec\nsteps:\n  - run: trufflehog git file://.\n"},
		{"a pre-commit hook", ".pre-commit-config.yaml", "repos:\n  - repo: https://github.com/gitleaks/gitleaks\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := repo(t, "go.mod")
			p := filepath.Join(dir, filepath.FromSlash(tc.path))
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(p, []byte(tc.body), 0o644); err != nil {
				t.Fatal(err)
			}

			res, err := Ensure(dir)
			if err != nil {
				t.Fatal(err)
			}
			if res.ScanCreated {
				t.Fatal("added a second scanner to a project that already scans")
			}
			if !strings.Contains(strings.Join(res.Notes, " "), "already wired up") {
				t.Errorf("the note must say why nothing was added: %v", res.Notes)
			}
		})
	}
}

// An existing scan workflow is left alone, like every other file here.
func TestTheScanWorkflowIsNeverOverwritten(t *testing.T) {
	dir := repo(t, "go.mod")
	flows := filepath.Join(dir, ".github", "workflows")
	if err := os.MkdirAll(flows, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(flows, "orion-secret-scan.yml")
	mine := "name: mine\n"
	if err := os.WriteFile(p, []byte(mine), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Ensure(dir); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(p); string(b) != mine {
		t.Fatal("an existing secret-scan workflow was overwritten")
	}
}

// The whole point, end to end: a planted secret fails the build, and the
// secret does not appear in the output that failure is read from.
//
// Runs the SAME command the scaffolded workflow runs -- scanCommand is shared
// between the two, so this cannot pass against a command CI does not use.
func TestAPlantedSecretFailsTheScanAndIsNotPrintedInTheClear(t *testing.T) {
	if _, err := exec.LookPath("gitleaks"); err != nil {
		t.Skip("gitleaks not installed; the workflow installs its own pinned copy")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	dir := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init", "-q")
	git("config", "user.email", "t@example.com")
	git("config", "user.name", "t")

	// A structurally valid AWS key, so the default rules fire. It is not a
	// real credential, and gitleaks cannot tell the difference -- which is
	// the property under test. Deliberately NOT the AWS documentation key
	// (AKIAIOSFODNN7EXAMPLE): gitleaks allowlists that one, so a test using
	// it would pass while proving the scan works on nothing.
	//
	// Assembled at runtime rather than written as one literal, because THIS
	// file is committed and pushed, and GitHub's push protection scans it
	// with the same rules the test plants for. The planted file in the
	// throwaway repo gets the contiguous key; the source never contains it.
	planted := "AKIA" + "QYLPMN5HX7RZ2K4T"
	if err := os.WriteFile(filepath.Join(dir, "app.py"),
		[]byte("aws_access_key_id = \""+planted+"\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git("add", "-A")
	git("commit", "-qm", "plant a secret")

	// Committed, then removed, so only history holds it: the case a
	// diff-only scan misses.
	if err := os.Remove(filepath.Join(dir, "app.py")); err != nil {
		t.Fatal(err)
	}
	git("commit", "-qam", "remove it again")

	fields := strings.Fields(scanCommand)
	cmd := exec.Command(fields[0], fields[1:]...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("the scan passed on a repository with a secret in its history:\n%s", out)
	}
	if strings.Contains(string(out), planted) {
		t.Errorf("the scan printed the secret in the clear; CI logs are as public "+
			"as the repository:\n%s", out)
	}
}

func TestTheScriptIsExecutable(t *testing.T) {
	dir := repo(t, "go.mod")
	if _, err := Ensure(dir); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(filepath.Join(dir, "scripts", "test.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode()&0o111 == 0 {
		t.Fatal("scripts/test.sh is not executable; the workflow calls it directly")
	}
}
