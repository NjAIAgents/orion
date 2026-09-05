package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeTasks(t *testing.T, root, feature string) string {
	t.Helper()
	dir := filepath.Join(root, "specs", feature)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "tasks.md")
	if err := os.WriteFile(p, []byte("# Tasks: X\n\n- [ ] T001 Do it in a.go\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// One task list is found without being named; several are reported rather
// than resolved, because picking one would decompose a feature nobody asked
// about into a shared tracker.
func TestFindTasksReportsAmbiguityRatherThanGuessing(t *testing.T) {
	root := t.TempDir()

	if _, err := findTasks(root); err == nil {
		t.Error("want an error when there is no task list")
	} else if !strings.Contains(err.Error(), "orion decompose") {
		t.Errorf("the error should say how to name the file: %v", err)
	}

	want := writeTasks(t, root, "001-alpha")
	got, err := findTasks(root)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Errorf("found %q, want %q", got, want)
	}

	second := writeTasks(t, root, "002-beta")
	_, err = findTasks(root)
	if err == nil {
		t.Fatal("two task lists must not resolve to one silently")
	}
	for _, p := range []string{want, second} {
		if !strings.Contains(err.Error(), p) {
			t.Errorf("the error should list %s so the operator can choose: %v", p, err)
		}
	}
}

// The confirmation is the whole guarantee: a run with nobody present
// creates nothing, and says why rather than appearing to have worked.
func TestConfirmCreateAnswersNoWhenNobodyIsThere(t *testing.T) {
	defer swapTTY(t, false)()

	var out bytes.Buffer
	if confirmCreate(&out, "Create 8 items in CAT?") {
		t.Fatal("an unattended run must not create issues in a shared tracker")
	}
	if !strings.Contains(out.String(), "nothing was created") {
		t.Errorf("say what happened rather than falling silent:\n%s", out.String())
	}
}

// And with somebody there, only a yes is a yes.
func TestConfirmCreateNeedsAnExplicitYes(t *testing.T) {
	defer swapTTY(t, true)()

	for _, tc := range []struct {
		answer string
		want   bool
	}{
		{"y\n", true},
		{"yes\n", true},
		{"n\n", false},
		{"\n", false},
		{"later\n", false},
	} {
		restore := swapConfirmIn(t, tc.answer)
		var out bytes.Buffer
		got := confirmCreate(&out, "Create 8 items in CAT?")
		restore()
		if got != tc.want {
			t.Errorf("answer %q -> %v, want %v: silence is not consent for issues other\n"+
				"people will see", strings.TrimSpace(tc.answer), got, tc.want)
		}
	}
}

func swapTTY(t *testing.T, tty bool) func() {
	t.Helper()
	prev := stdinIsTTY
	stdinIsTTY = func() bool { return tty }
	return func() { stdinIsTTY = prev }
}

func swapConfirmIn(t *testing.T, answer string) func() {
	t.Helper()
	prev := confirmIn
	confirmIn = strings.NewReader(answer)
	return func() { confirmIn = prev }
}
