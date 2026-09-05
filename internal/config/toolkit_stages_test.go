package config

import (
	"sort"
	"strings"
	"testing"
)

// Every canonical stage name Orion dispatches must be a valid key in
// toolkit.stages -- this is the full set stagePrompt's switch knows, not a
// sample of it.
func TestAllCanonicalStageNamesAreAccepted(t *testing.T) {
	canonical := []string{
		"intent", "spec", "plan", "ticket", "scaffold",
		"decompose", "build", "verify", "review", "pr",
	}
	for _, stage := range canonical {
		cfg := loadJSON(t, `{"toolkit":{"stages":{"`+stage+`":"/run-it"}}}`)
		if err := cfg.Validate(); err != nil {
			t.Fatalf("%s is a canonical stage: %v", stage, err)
		}
		if got := cfg.Toolkit.Stage(stage); got != "/run-it" {
			t.Errorf("stage %s = %q, want /run-it", stage, got)
		}
	}
}

// Every alias spelling Orion accepts must be a valid key too, distinct from
// the canonical-name test above so a regression in alias resolution doesn't
// hide behind the canonical set passing.
func TestAllAliasStageNamesAreAccepted(t *testing.T) {
	aliases := map[string]string{
		"design":    "spec",
		"implement": "build",
		"test":      "verify",
		"ship":      "pr",
	}
	for alias, canonical := range aliases {
		cfg := loadJSON(t, `{"toolkit":{"stages":{"`+alias+`":"/run-it"}}}`)
		if err := cfg.Validate(); err != nil {
			t.Fatalf("%s is a valid alias: %v", alias, err)
		}
		if got := cfg.Toolkit.Stage(canonical); got != "/run-it" {
			t.Errorf("alias %s declared, canonical %s reads %q", alias, canonical, got)
		}
	}
}

// The rejection for an unknown stage key must name the offending key AND
// list the valid stage names -- a bare "unknown stage" tells a project
// nothing about what it should have typed instead.
func TestUnknownStageErrorListsValidOptions(t *testing.T) {
	cfg := loadJSON(t, `{"toolkit":{"stages":{"deploy":"/ship-it"}}}`)
	err := cfg.Validate()
	if err == nil {
		t.Fatal("an unknown stage key must be reported as a config error")
	}
	msg := err.Error()
	if !strings.Contains(msg, "deploy") {
		t.Errorf("error must cite the invalid key %q, got: %v", "deploy", err)
	}
	for _, stage := range []string{"intent", "spec", "plan", "ticket", "scaffold",
		"decompose", "build", "verify", "review", "pr"} {
		if !strings.Contains(msg, stage) {
			t.Errorf("error must list valid stage %q among the options, got: %v", stage, err)
		}
	}
}

// The collision error names both spellings that collided, in sorted order,
// so the message is identical run to run rather than depending on Go's
// randomized map iteration.
func TestCollisionErrorNamesBothSpellingsSorted(t *testing.T) {
	cfg := loadJSON(t, `{"toolkit":{"stages":{"spec":"/plan","design":"/design-it"}}}`)
	err := cfg.Validate()
	if err == nil {
		t.Fatal("two spellings with different commands must be rejected")
	}
	msg := err.Error()
	if !strings.Contains(msg, `"design"`) || !strings.Contains(msg, `"spec"`) {
		t.Errorf("error must quote both colliding keys, got: %v", err)
	}
	// "design" sorts before "spec"; the error must report them in that order
	// regardless of which key was declared first in the JSON.
	if strings.Index(msg, `"design"`) > strings.Index(msg, `"spec"`) {
		t.Errorf("colliding keys must be reported sorted, got: %v", err)
	}
	got := []string{"design", "spec"}
	sort.Strings(got)
	if got[0] != "design" {
		t.Fatalf("sanity: sort.Strings ordering assumption is wrong: %v", got)
	}
}
