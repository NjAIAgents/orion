package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The rule the branch model always stated and never enforced: agent work
// lands on the integration branch, a human promotes it to the release
// branch. A default is not a constraint -- somebody edits work_branch for a
// good local reason and Orion starts merging into the release branch,
// reporting it accurately, which is what makes it hard to see.
func TestWorkBranchEqualToDefaultBranchIsRefused(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, `{"vcs":{"default_branch":"main","work_branch":"main"}}`)

	err := Load(dir).Validate()
	if err == nil {
		t.Fatal("work_branch == default_branch must be refused, not merged into")
	}
	msg := err.Error()
	// Naming both values, so the reader can find the two lines they must
	// change without going looking for them.
	for _, want := range []string{"work_branch", "default_branch", `"main"`} {
		if !strings.Contains(msg, want) {
			t.Errorf("the error must name %s: %s", want, msg)
		}
	}
	// The reason, not merely the refusal.
	if !strings.Contains(msg, "release branch") || !strings.Contains(msg, "integration branch") {
		t.Errorf("the error must explain the two roles: %s", msg)
	}
	// The remedy, including that init can create the missing branch.
	if !strings.Contains(msg, "orion init") {
		t.Errorf("the error must point at orion init, which creates the branch: %s", msg)
	}
	if !strings.Contains(msg, "allow_release_branch_merges") {
		t.Errorf("the error must name the escape hatch: %s", msg)
	}
}

// A repository with genuinely one branch and no release process is
// legitimate. It has to say so by name, and it gets told what it gave up.
func TestTheSingleBranchOverrideIsExplicitAndLoud(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir,
		`{"vcs":{"default_branch":"main","work_branch":"main","allow_release_branch_merges":true}}`)

	cfg := Load(dir)
	if err := cfg.Validate(); err != nil {
		t.Fatalf("the named opt-in must be honoured: %v", err)
	}
	waiver := cfg.ReleaseBranchWaiver()
	if waiver == "" {
		t.Fatal("an override nobody is reminded of is an override nobody remembers making")
	}
	if !strings.Contains(waiver, "allow_release_branch_merges") ||
		!strings.Contains(waiver, "no human promotion step") {
		t.Errorf("the waiver must say what protection is given up: %s", waiver)
	}
}

func TestTheTwoBranchModelPassesAndSaysNothing(t *testing.T) {
	cfg := Defaults()
	if err := cfg.Validate(); err != nil {
		t.Fatalf("the shipped defaults must be a valid branch model: %v", err)
	}
	if w := cfg.ReleaseBranchWaiver(); w != "" {
		t.Errorf("no waiver is in force, so nothing should be warned about: %s", w)
	}
	// The key on a repository that has both branches waives nothing, so it
	// must not warn about a protection that is still fully in place.
	cfg.VCS.AllowReleaseBranchMerges = true
	if w := cfg.ReleaseBranchWaiver(); w != "" {
		t.Errorf("the key is inert while the branches differ: %s", w)
	}
}

// The shipped template and this repository's own config are the two
// configurations most likely to be copied, so they are the two that must
// not describe a model Orion refuses to run.
func TestTheShippedConfigsSatisfyTheirOwnRule(t *testing.T) {
	for _, root := range []string{"../..", "../../templates"} {
		cfg := Load(root)
		if cfg.Degraded {
			t.Fatalf("%s: %s", root, cfg.DegradedReason)
		}
		if err := cfg.Validate(); err != nil {
			t.Errorf("%s/orion.json: %v", root, err)
		}
	}
}

func writeConfig(t *testing.T, dir, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "orion.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}
