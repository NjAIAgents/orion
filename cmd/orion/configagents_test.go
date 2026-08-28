package main

import (
	"strings"
	"testing"

	"github.com/orion-sdlc/orion/internal/config"
)

func strp(s string) *string { return &s }

// A wizard pass where the operator touched nothing at all must not add
// "id": {} noise to orion.json -- that would be indistinguishable from a
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

// Only the fields actually set are ever written -- an empty designation
// would otherwise silently mean "clear the designation", which is not a
// concept this field has (unlike Name, whose empty string is documented as
// exactly that).
func TestMarshalAgentOmitsUnsetFields(t *testing.T) {
	got := marshalAgent(config.Agent{Effort: "high"})
	if !strings.Contains(got, `"effort": "high"`) {
		t.Errorf("effort missing: %s", got)
	}
	if strings.Contains(got, `"model"`) || strings.Contains(got, `"designation"`) || strings.Contains(got, `"name"`) {
		t.Errorf("unset fields must not be written: %s", got)
	}
}

func TestMarshalAgentOnAZeroValueIsAnEmptyObject(t *testing.T) {
	if got := marshalAgent(config.Agent{}); got != "{}" {
		t.Errorf("marshalAgent(zero) = %q, want {}", got)
	}
}

// A cleared name (the "-" convention) must be written as an explicit empty
// string, not omitted -- omitting it would mean "no override" instead of
// "render as job title alone".
func TestMarshalAgentWritesAnExplicitlyClearedName(t *testing.T) {
	got := marshalAgent(config.Agent{Name: strp("")})
	if !strings.Contains(got, `"name": ""`) {
		t.Errorf("a cleared name must be written explicitly: %s", got)
	}
}

func TestJSONStrEscapesQuotes(t *testing.T) {
	if got := jsonStr(`say "hi"`); got != `"say \"hi\""` {
		t.Errorf("jsonStr = %q", got)
	}
}
