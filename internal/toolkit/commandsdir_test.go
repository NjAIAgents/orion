package toolkit

import (
	"os"
	"path/filepath"
	"testing"
)

// speckitLayout writes the shape github/spec-kit actually has: commands as
// markdown files under templates/commands, and no skills/ directory at all.
// Verified against a clone of the real repository, 2026-09-06.
func speckitLayout(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, "templates", "commands")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"specify", "plan", "tasks", "implement", "analyze"} {
		if err := os.WriteFile(filepath.Join(dir, name+".md"),
			[]byte("---\ndescription: "+name+"\n---\n\nbody\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// A toolkit whose commands are not laid out as skills/<name>/SKILL.md was
// rejected outright: Validate returned nil, Discover found nothing, and
// `orion doctor` reported "not installed" for a clone sitting right there.
//
// That is why ADR 0019's own worked example -- spec-kit -- could never be
// adopted, while every child of its epic reported Done.
func TestASpecKitLayoutIsRecognisedAsAToolkit(t *testing.T) {
	root := speckitLayout(t)

	inst := Validate(root, Toolkit{Repo: "https://github.com/github/spec-kit.git"})
	if inst == nil {
		t.Fatal("a directory of commands was not recognised as a toolkit at all")
	}
	if inst.Root != root {
		t.Errorf("Root = %q, want %q", inst.Root, root)
	}
}

// Finding the directory is not enough: HasSkill reads the command file, and
// a toolkit that validates but whose every command reads as missing is a
// doctor that says "installed, and nothing in it works".
func TestACommandInASpecKitLayoutIsFound(t *testing.T) {
	root := speckitLayout(t)
	inst := Validate(root, Toolkit{})
	if inst == nil {
		t.Fatal("not recognised")
	}

	for _, name := range []string{"specify", "plan", "tasks"} {
		if !HasSkill(inst, name) {
			t.Errorf("%q is in templates/commands and was not found", name)
		}
	}
	// A command spec-kit's own prefix names, since that is what a stage
	// config says: "spec": "/speckit.specify".
	if !HasSkill(inst, "speckit.specify") {
		t.Error(`"speckit.specify" was not resolved to templates/commands/specify.md`)
	}
	if HasSkill(inst, "does-not-exist") {
		t.Error("a command that is not there was reported present")
	}
}

// The nj-agents layout must keep working exactly as it did. This is the
// shipped default and every existing installation has this shape.
func TestTheSkillsLayoutStillWins(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "skills", "pre-push-review")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	inst := Validate(root, Toolkit{})
	if inst == nil {
		t.Fatal("the nj-agents layout stopped being recognised")
	}
	if !HasSkill(inst, "pre-push-review") {
		t.Error("a skills/<name>/SKILL.md skill stopped resolving")
	}
	if HasSkill(inst, "not-a-skill") {
		t.Error("an absent skill was reported present")
	}
}

// A directory with neither layout is still not a toolkit. A false positive
// here tells someone an empty directory is a healthy install.
func TestADirectoryWithNoCommandsIsStillNotAToolkit(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if inst := Validate(root, Toolkit{}); inst != nil {
		t.Errorf("an ordinary directory was called a toolkit: %+v", inst)
	}
}

// An empty templates/commands is an INCOMPLETE toolkit, not a random
// directory -- the same rule skills/ already followed. "Found at X, these
// commands are missing" is a more useful answer than "not installed", and a
// second layout must not change that.
func TestAnEmptyCommandsDirectoryIsAnIncompleteToolkit(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "templates", "commands"), 0o755); err != nil {
		t.Fatal(err)
	}
	tk := Toolkit{Stages: map[string]string{"spec": "/speckit.specify"}}
	inst := Validate(root, tk)
	if inst == nil {
		t.Fatal("an empty commands directory disappeared into \"not installed\"")
	}
	if len(inst.Missing) == 0 {
		t.Error("nothing was reported missing from an empty toolkit")
	}
}
