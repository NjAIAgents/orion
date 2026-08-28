package main

import (
	"testing"

	"github.com/orion-sdlc/orion/internal/config"
)

func strp(s string) *string { return &s }

// A wizard pass where the operator touched nothing at all must not add
// "id": {} noise to agents.json -- that would be indistinguishable from a
// real (empty) override to a human reading the file later.
func TestAgentIsZeroOnAnUntouchedAgent(t *testing.T) {
	if !agentIsZero(config.Agent{}) {
		t.Error("a zero-value Agent must read as zero")
	}
	if agentIsZero(config.Agent{Model: "opus"}) {
		t.Error("a set Model must not read as zero")
	}
	if agentIsZero(config.Agent{Name: strp("")}) {
		t.Error("an explicitly cleared name is still a real override, not zero")
	}
}

// The wizard's save path (SaveAgents then LoadAgents) must round-trip an
// entry with only some fields set without inventing the rest -- config.Agent
// now relies on json "omitempty" doing this correctly rather than the
// hand-rolled marshalling OR-131 shipped with (OR-132).
func TestAgentRoundTripsOnlyTheFieldsThatWereSet(t *testing.T) {
	home := t.TempDir()
	want := map[string]config.Agent{
		"implementer": {Effort: "high"},
		"qa":          {Name: strp(""), Model: "haiku"},
	}
	if err := config.SaveAgents(home, want); err != nil {
		t.Fatal(err)
	}
	got, err := config.LoadAgents(home)
	if err != nil {
		t.Fatal(err)
	}
	if got["implementer"].Effort != "high" || got["implementer"].Model != "" {
		t.Errorf("implementer = %+v", got["implementer"])
	}
	if got["qa"].Name == nil || *got["qa"].Name != "" {
		t.Errorf("qa.Name = %v, want an explicit empty string, not absent", got["qa"].Name)
	}
	if got["qa"].Model != "haiku" {
		t.Errorf("qa.Model = %q", got["qa"].Model)
	}
}
