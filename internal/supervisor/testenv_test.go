package supervisor

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// The implementer prompt used to name no way of running the tests at all.
// The only mention of scripts/test.sh in the whole system was in the CI-fix
// prompt, so an agent starting a ticket discovered the entry point by
// guessing at it and then learned how to make it work by trial and error --
// rediscovered from zero on every ticket, because nothing carried forward.

func writeTestScript(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, "scripts"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "scripts", "test.sh"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
}

func writeVenv(t *testing.T, dir string) string {
	t.Helper()
	// The platform's own layout, matching venvInterpreter in prompts.go.
	sub, name := "bin", "python"
	if runtime.GOOS == "windows" {
		sub, name = "Scripts", "python.exe"
	}
	bin := filepath.Join(dir, ".venv", sub)
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	py := filepath.Join(bin, name)
	if err := os.WriteFile(py, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return py
}

func TestThePromptNamesTheTestCommand(t *testing.T) {
	dir := t.TempDir()
	writeTestScript(t, dir)

	p := TicketPrompt("OR-39", "s", "d", "u", dir, nil)

	if !strings.Contains(p, "./scripts/test.sh") {
		t.Errorf("the prompt must name the test entry point:\n%s", p)
	}
	if !strings.Contains(p, "what CI runs on every push") {
		t.Errorf("the prompt must say what that script is for:\n%s", p)
	}
	// And it must tell the agent NOT to run it. CI runs the whole suite on
	// three platforms for every push, so running it again on the critical
	// path buys nothing and costs model time while holding a job slot: on
	// OR-135 the agent ran it four times and spent 37 minutes, most of that
	// waiting on packages the change never touched (OR-266).
	if !strings.Contains(p, "do NOT run it here") {
		t.Errorf("the prompt must not ask the agent to run the whole suite:\n%s", p)
	}
	if !strings.Contains(p, "go test ./internal/<pkg>/") {
		t.Errorf("the prompt must name the scoped alternative:\n%s", p)
	}
	// The script itself must not be inlined: it would go stale the first time
	// somebody edited it, and a prompt that confidently describes a command
	// that no longer works is worse than no prompt.
	if strings.Contains(p, "#!/bin/sh") {
		t.Error("the contents of scripts/test.sh were pasted into the prompt")
	}
}

// A repository with no scripts/test.sh must not be told to run one. The
// failure mode is not a no-op: the agent runs the named command, it fails,
// and it now distrusts the instruction and goes exploring anyway -- the cost
// this change exists to remove, plus a wasted turn.
func TestNoTestCommandIsNamedWhenTheScriptIsAbsent(t *testing.T) {
	p := TicketPrompt("OR-39", "s", "d", "u", t.TempDir(), nil)

	if strings.Contains(p, "scripts/test.sh") {
		t.Errorf("named a script this repository does not have:\n%s", p)
	}
}

// The added lines belong to EVIDENCE, not to the section after it. Losing the
// blank line runs "the interpreter to use." straight into "COMMITS", which
// reads as one paragraph.
func TestTheSectionAfterEvidenceStillStartsOnItsOwn(t *testing.T) {
	dir := t.TempDir()
	writeTestScript(t, dir)

	for name, path := range map[string]string{"with a test script": dir, "with nothing to say": t.TempDir()} {
		if p := TicketPrompt("OR-39", "s", "d", "u", path, nil); !strings.Contains(p, "\n\nCOMMITS") {
			t.Errorf("%s: COMMITS lost its blank line:\n%s", name, p)
		}
	}
}

func TestThePromptNamesTheProvisionedInterpreter(t *testing.T) {
	dir := t.TempDir()
	py := writeVenv(t, dir)

	p := TicketPrompt("OR-39", "s", "d", "u", dir, nil)

	if !strings.Contains(p, py) {
		t.Errorf("the prompt must name the virtualenv's python (%s):\n%s", py, p)
	}
}

func TestNoInterpreterIsNamedWhenNoVirtualenvWasProvisioned(t *testing.T) {
	p := TicketPrompt("OR-39", "s", "d", "u", t.TempDir(), nil)

	if strings.Contains(p, "virtualenv is already built") {
		t.Errorf("claimed a virtualenv that does not exist:\n%s", p)
	}
}

// The agent works in a git worktree, which shares history but not ignored
// files -- so the virtualenv built once per sandbox lives in the clone the
// worktree hangs off, never in the worktree itself. Resolving only the local
// .venv would find nothing on every real run.
func TestTheInterpreterIsFoundInTheMainWorktree(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not available")
	}
	root := t.TempDir()
	clone := filepath.Join(root, "clone")
	if err := os.MkdirAll(clone, 0o755); err != nil {
		t.Fatal(err)
	}
	git := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@x",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@x")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	git(clone, "init", "-q", ".")
	if err := os.WriteFile(filepath.Join(clone, "f"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(clone, "add", "f")
	git(clone, "commit", "-qm", "first")

	py := writeVenv(t, clone) // built once, in the clone -- not in the worktree
	tree := filepath.Join(root, "job")
	git(clone, "worktree", "add", "-q", "-b", "orion/or-39", tree)

	p := TicketPrompt("OR-39", "s", "d", "u", tree, nil)

	// Either spelling of the same file. venvPython builds the path from
	// git's own output, and git prints the CANONICAL form -- long names on
	// Windows, resolved symlinks elsewhere -- while t.TempDir may have
	// handed this test a short-name (RUNNER~1) form. Both name one file;
	// the prompt is right whichever it carries (OR-344).
	canonical, evalErr := filepath.EvalSymlinks(py)
	if evalErr != nil {
		canonical = py
	}
	if !strings.Contains(p, py) && !strings.Contains(p, canonical) {
		t.Errorf("the worktree must be told about the clone's interpreter (%s):\n%s", py, p)
	}
}
