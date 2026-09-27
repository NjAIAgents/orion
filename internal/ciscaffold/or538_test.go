package ciscaffold

// OR-538: CI written before the toolchain existed, and a secret scan that
// read every branch's history, together kept log-triage-agent red on every
// branch for reasons none of its tickets caused.

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The plan chain writes CI before the scaffold creates pyproject.toml. The
// workflow must still set up Python when it RUNS on a tree that has one.
func TestAnUnknownToolchainWorkflowChoosesItsToolchainWhenItRuns(t *testing.T) {
	wf := workflowFor(StackUnknown)
	for _, want := range []string{
		"actions/setup-python@v5", "hashFiles('pyproject.toml'",
		`pip install -e ".[dev]"`,
		"actions/setup-go@v5", "hashFiles('go.mod')",
		"actions/setup-node@v4", "hashFiles('package.json')",
	} {
		if !strings.Contains(wf, want) {
			t.Errorf("the unknown-toolchain workflow has no %q", want)
		}
	}
	if !strings.Contains(wf, "run: ./scripts/test.sh") {
		t.Error("the workflow no longer runs scripts/test.sh")
	}
}

func writeFlow(t *testing.T, dir, name, body string) string {
	t.Helper()
	p := filepath.Join(dir, ".github", "workflows", name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// A workflow Orion wrote before the toolchain existed is upgraded; one a
// person edited is not.
func TestAWorkflowWrittenBeforeTheToolchainExistedIsUpgraded(t *testing.T) {
	dir := repo(t, "pyproject.toml")
	p := writeFlow(t, dir, "orion-ci.yml", workflowWith(""))
	if _, err := Ensure(dir); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(p); !strings.Contains(string(b), "actions/setup-python@v5") {
		t.Errorf("the bare workflow Orion wrote was not upgraded:\n%s", b)
	}

	dir = repo(t, "pyproject.toml")
	edited := workflowWith("") + "# mine\n"
	p = writeFlow(t, dir, "orion-ci.yml", edited)
	if _, err := Ensure(dir); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(p); string(b) != edited {
		t.Error("a workflow someone edited was overwritten")
	}
}

func TestTheScanIsScopedToTheRefUnderTest(t *testing.T) {
	if !strings.Contains(scanCommand, "--log-opts=HEAD") {
		t.Errorf("the scan reads more than the ref under test: %s", scanCommand)
	}
	dir := repo(t, "go.mod")
	p := writeFlow(t, dir, "orion-secret-scan.yml", scanWorkflowWith(legacyScanCommand))
	if _, err := Ensure(dir); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(p); !strings.Contains(string(b), "--log-opts=HEAD") {
		t.Error("the scan workflow Orion wrote before OR-538 was not upgraded")
	}
}

// End to end with the real scanner: a secret on ANOTHER branch no longer
// fails this one, and a secret on this branch still does.
func TestASecretOnAnotherBranchDoesNotFailThisOne(t *testing.T) {
	if _, err := exec.LookPath("gitleaks"); err != nil {
		t.Skip("gitleaks not installed; the workflow installs its own pinned copy")
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
	git("init", "-q", "-b", "main")
	git("config", "user.email", "t@example.com")
	git("config", "user.name", "t")
	if err := os.WriteFile(filepath.Join(dir, "README"), []byte("clean\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git("add", "-A")
	git("commit", "-qm", "clean")
	git("checkout", "-qb", "leaky")
	planted := "AKIA" + "QYLPMN5HX7RZ2K4T" // see TestAPlantedSecret... for why it is split
	if err := os.WriteFile(filepath.Join(dir, "app.py"),
		[]byte("aws_access_key_id = \""+planted+"\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git("add", "-A")
	git("commit", "-qm", "plant")

	scan := func() error {
		f := strings.Fields(scanCommand)
		cmd := exec.Command(f[0], f[1:]...)
		cmd.Dir = dir
		return cmd.Run()
	}
	git("checkout", "-q", "main")
	if err := scan(); err != nil {
		t.Error("a clean branch failed the scan because another branch holds a secret")
	}
	git("checkout", "-q", "leaky")
	if err := scan(); err == nil {
		t.Error("the branch holding the secret passed the scan")
	}
}
