package supervisor

import (
	"strings"
	"testing"
)

// The triage subagent must be told it is READ ONLY. It runs inside the same
// worktree the fix run is about to work in, so an agent that decided to
// "helpfully" fix what it found would be editing a tree another run is about
// to edit, from a session nobody is watching (OR-143).
func TestLogTriagePromptCarriesTheLogAndForbidsEditing(t *testing.T) {
	p := LogTriagePrompt("orion/or-1", "FAIL\tinternal/work\t0.4s\nwork_test.go:12: boom")

	for _, want := range []string{"orion/or-1", "work_test.go:12", "boom"} {
		if !strings.Contains(p, want) {
			t.Errorf("prompt is missing %q; triage cannot report on a log it was not given", want)
		}
	}

	lower := strings.ToLower(p)
	if !strings.Contains(lower, "read") {
		t.Error("prompt must state that the run is read-only")
	}
	if !strings.Contains(lower, "do not edit") && !strings.Contains(lower, "not edit") {
		t.Error("prompt must forbid editing outright: it shares a worktree with the fix run")
	}
}
