package njagents

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// OR-297: fromRunnerSymlink (the configured-skill-name search) and the
// Clone/Confirm consent path. writeToolkit and writeToolkitAt are shared
// helpers defined in required_test.go and discover_test.go respectively.

// evalSymlinksOrFatal canonicalizes an expected root the same way
// fromRunnerSymlink canonicalizes its result (filepath.EvalSymlinks on the
// resolved skill link). Without this, a t.TempDir() root compared directly
// against fromRunnerSymlink's return value only matches by coincidence: on
// macOS /tmp is itself a symlink to /private/tmp, so a TMPDIR under /tmp
// resolves to a /private/tmp path that a raw string comparison would wrongly
// reject as "not the same root".
func evalSymlinksOrFatal(t *testing.T, root string) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	return resolved
}

// -- fromRunnerSymlink: searching for configured skill names -------------

func TestFromRunnerSymlinkReturnsToolkitRootWhenSymlinkResolvesToIt(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	toolkitRoot := t.TempDir()
	writeToolkitAt(t, toolkitRoot, "custom-skill")

	skillsDir := filepath.Join(home, ".claude", "skills")
	if err := os.MkdirAll(skillsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(toolkitRoot, "skills", "custom-skill"),
		filepath.Join(skillsDir, "custom-skill")); err != nil {
		t.Fatal(err)
	}

	tk := Toolkit{Repo: "https://example.com/kit.git", Stages: map[string]string{"review": "/custom-skill"}}
	got := fromRunnerSymlink(tk)
	want := evalSymlinksOrFatal(t, toolkitRoot)
	if got != want {
		t.Errorf("fromRunnerSymlink = %q, want the resolved toolkit root %q", got, want)
	}
}

func TestFromRunnerSymlinkReturnsEmptyStringWhenNoConfiguredSymlinksExist(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	tk := Toolkit{Repo: "https://example.com/kit.git", Stages: map[string]string{"review": "/custom-skill"}}
	if got := fromRunnerSymlink(tk); got != "" {
		t.Errorf("fromRunnerSymlink = %q, want empty: nothing is linked", got)
	}
}

func TestFromRunnerSymlinkWorksAcrossAllRunnerDirectories(t *testing.T) {
	for _, runner := range []string{".claude", ".agents", ".codex", ".gemini", ".cursor"} {
		t.Run(runner, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)

			toolkitRoot := t.TempDir()
			writeToolkitAt(t, toolkitRoot, "custom-skill")

			skillsDir := filepath.Join(home, runner, "skills")
			if err := os.MkdirAll(skillsDir, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(filepath.Join(toolkitRoot, "skills", "custom-skill"),
				filepath.Join(skillsDir, "custom-skill")); err != nil {
				t.Fatal(err)
			}

			tk := Toolkit{Repo: "https://example.com/kit.git", Stages: map[string]string{"review": "/custom-skill"}}
			got := fromRunnerSymlink(tk)
			want := evalSymlinksOrFatal(t, toolkitRoot)
			if got != want {
				t.Errorf("fromRunnerSymlink under %s = %q, want %q", runner, got, want)
			}
		})
	}
}

// A skill symlink resolving somewhere is not enough: the resolved root must
// also pass isToolkitRoot. Here it names the default toolkit, so
// CONVENTIONS.md is required and missing -- the root must be rejected even
// though the symlink itself resolves cleanly.
func TestFromRunnerSymlinkRejectsRootFailingIsToolkitRootEvenWithSkillFound(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	root := t.TempDir()
	writeToolkitAt(t, root, "pre-push-review") // no CONVENTIONS.md

	skillsDir := filepath.Join(home, ".claude", "skills")
	if err := os.MkdirAll(skillsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "skills", "pre-push-review"),
		filepath.Join(skillsDir, "pre-push-review")); err != nil {
		t.Fatal(err)
	}

	got := fromRunnerSymlink(Toolkit{}) // default toolkit: requires CONVENTIONS.md
	if got != "" {
		t.Errorf("fromRunnerSymlink = %q, want empty: root lacks CONVENTIONS.md so isToolkitRoot must reject it", got)
	}
}

// -- Clone: validating the result of a real clone -------------------------

func hasGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
}

// localGitRepo creates a real, committable git repository at a local path so
// Clone can fetch it without touching the network. It deliberately does not
// look like a toolkit: no skills/ directory.
func localGitRepo(t *testing.T) string {
	t.Helper()
	hasGit(t)
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %s: %v", args, out, err)
		}
	}
	run("init")
	run("config", "user.email", "test@example.com")
	run("config", "user.name", "test")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("not a toolkit"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", ".")
	run("commit", "-m", "init")
	return dir
}

func TestCloneValidatesTheResultAndReturnsErrorOnIncompleteCheckout(t *testing.T) {
	src := localGitRepo(t)
	home := t.TempDir()
	tk := Toolkit{Repo: src, Stages: map[string]string{"review": "/only"}}

	inst, err := Clone(home, tk, "", func(string) bool { return true })
	if err == nil {
		t.Fatal("Clone of a non-toolkit repository succeeded, want an incomplete-checkout error")
	}
	if !strings.Contains(err.Error(), "incomplete") {
		t.Errorf("error = %q, want it to say the checkout is incomplete", err)
	}
	if inst != nil && inst.OK() {
		t.Errorf("Install = %+v, want not OK for a non-toolkit checkout", inst)
	}
	if _, statErr := os.Stat(VendorDirFor(home, src)); statErr != nil {
		t.Errorf("expected the clone to actually land at the vendor dir: %v", statErr)
	}
}

// -- ConfirmOnStdin ---------------------------------------------------------

// ConfirmOnStdin's char-device check runs before it reads or prints
// anything, so this is the one behavior testable without a real TTY: a
// piped/redirected stdin (exactly what CI and this test process itself have)
// must decline without blocking on input.
func TestConfirmOnStdinReturnsFalseWhenStdinNotInteractive(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()

	orig := os.Stdin
	os.Stdin = r
	defer func() { os.Stdin = orig }()

	if got := ConfirmOnStdin("https://example.com/kit.git"); got != false {
		t.Errorf("ConfirmOnStdin(piped stdin) = %v, want false", got)
	}
}

// ConfirmOnStdin's "y"/"yes" acceptance, its rejection of "n"/"no"/empty/
// anything else, and the prompt text all live behind the same char-device
// check above, and only fire when os.Stdin is a real TTY. There is no pty
// dependency in this module to fake that character device, so those three
// cases cannot be exercised without either adding a new dependency or
// changing ConfirmOnStdin's structure -- both out of scope here. Leaving
// them untested rather than papering over it with a fake that never
// exercises the real char-device branch.
