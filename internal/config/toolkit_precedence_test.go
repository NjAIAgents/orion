package config

import (
	"strings"
	"testing"
)

// Each ordering-key spelling is refused on its own, citing decisions/0001 --
// not just "order" and "sequence", but every alias parseToolkit knows about.

func TestToolkitOrderKeyRejected(t *testing.T) {
	err := loadJSON(t, `{"toolkit":{"order":["spec","plan"],"stages":{"spec":"/plan"}}}`).Validate()
	if err == nil {
		t.Fatal("an order key must be rejected")
	}
	if !strings.Contains(err.Error(), "decisions/0001") {
		t.Errorf("error must cite decisions/0001, got: %v", err)
	}
}

func TestToolkitSequenceKeyRejected(t *testing.T) {
	err := loadJSON(t, `{"toolkit":{"sequence":["spec","plan"],"stages":{"spec":"/plan"}}}`).Validate()
	if err == nil {
		t.Fatal("a sequence key must be rejected")
	}
	if !strings.Contains(err.Error(), "decisions/0001") {
		t.Errorf("error must cite decisions/0001, got: %v", err)
	}
}

func TestToolkitStageOrderKeyRejected(t *testing.T) {
	err := loadJSON(t, `{"toolkit":{"stage_order":["spec","plan"],"stages":{"spec":"/plan"}}}`).Validate()
	if err == nil {
		t.Fatal("a stage_order key must be rejected")
	}
	if !strings.Contains(err.Error(), "decisions/0001") {
		t.Errorf("error must cite decisions/0001, got: %v", err)
	}
}

func TestToolkitPipelineKeyRejected(t *testing.T) {
	err := loadJSON(t, `{"toolkit":{"pipeline":["spec","plan"],"stages":{"spec":"/plan"}}}`).Validate()
	if err == nil {
		t.Fatal("a pipeline key must be rejected")
	}
	if !strings.Contains(err.Error(), "decisions/0001") {
		t.Errorf("error must cite decisions/0001, got: %v", err)
	}
}

// The rejection must say WHY, not just cite the ADR number: sequencing
// belongs to Orion, not the toolkit -- a project reading only "see 0001"
// still has to go look up what that means.
func TestOrderingKeyErrorExplainsWhoOwnsSequencing(t *testing.T) {
	err := loadJSON(t, `{"toolkit":{"order":["spec","plan"],"stages":{"spec":"/plan"}}}`).Validate()
	if err == nil {
		t.Fatal("an order key must be rejected")
	}
	msg := err.Error()
	if !strings.Contains(msg, "Orion's") {
		t.Errorf("error must explain sequencing is Orion's, got: %v", err)
	}
	if !strings.Contains(msg, "not a toolkit's") {
		t.Errorf("error must explain sequencing is not the toolkit's, got: %v", err)
	}
}

// The offending key's own name must appear in the error -- "expresses
// order" alone doesn't tell a project which of its keys triggered it.
func TestOrderingKeyNameAppearsInError(t *testing.T) {
	err := loadJSON(t, `{"toolkit":{"pipeline":["spec"],"stages":{"spec":"/plan"}}}`).Validate()
	if err == nil {
		t.Fatal("a pipeline key must be rejected")
	}
	if !strings.Contains(err.Error(), "pipeline") {
		t.Errorf("error must name the offending key %q, got: %v", "pipeline", err)
	}
}

func TestToolkitStagesArrayRejectedCitingADR(t *testing.T) {
	err := loadJSON(t, `{"toolkit":{"stages":["spec","plan"]}}`).Validate()
	if err == nil {
		t.Fatal("toolkit.stages as a list must be rejected")
	}
	if !strings.Contains(err.Error(), "decisions/0001") {
		t.Errorf("error must cite decisions/0001, got: %v", err)
	}
}

func TestToolkitItselfArrayRejectedCitingADR(t *testing.T) {
	err := loadJSON(t, `{"toolkit":[{"repo":"https://example.com/a.git"}]}`).Validate()
	if err == nil {
		t.Fatal("toolkit as a list must be rejected")
	}
	if !strings.Contains(err.Error(), "decisions/0001") {
		t.Errorf("error must cite decisions/0001, got: %v", err)
	}
}

// --- Toolkit precedence over delegation ---

func TestToolkitDirAloneWinsOverDelegationDir(t *testing.T) {
	cfg := loadJSON(t, `{"toolkit":{"dir":"/new/kit"},"delegation":{"nj_agents_dir":"/old/kit"}}`)
	if cfg.Toolkit.Dir != "/new/kit" {
		t.Errorf("toolkit.dir must win, got %q", cfg.Toolkit.Dir)
	}
	if !strings.Contains(cfg.ToolkitWarning, "delegation.nj_agents_dir") {
		t.Errorf("warning must name delegation.nj_agents_dir, got %q", cfg.ToolkitWarning)
	}
}

func TestToolkitRefAloneWinsOverDelegationRef(t *testing.T) {
	cfg := loadJSON(t, `{"toolkit":{"ref":"v2"},"delegation":{"nj_agents_ref":"v1"}}`)
	if cfg.Toolkit.Ref != "v2" {
		t.Errorf("toolkit.ref must win, got %q", cfg.Toolkit.Ref)
	}
	if !strings.Contains(cfg.ToolkitWarning, "delegation.nj_agents_ref") {
		t.Errorf("warning must name delegation.nj_agents_ref, got %q", cfg.ToolkitWarning)
	}
}

// Both fields set on both sides: toolkit wins on both, and the warning names
// both superseded keys, not just whichever was checked first.
func TestBothToolkitFieldsWinAndWarningNamesBothOldKeys(t *testing.T) {
	cfg := loadJSON(t, `{"toolkit":{"dir":"/new/kit","ref":"v2"},
	                     "delegation":{"nj_agents_dir":"/old/kit","nj_agents_ref":"v1"}}`)
	if cfg.Toolkit.Dir != "/new/kit" || cfg.Toolkit.Ref != "v2" {
		t.Fatalf("toolkit.* must win on both: dir=%q ref=%q", cfg.Toolkit.Dir, cfg.Toolkit.Ref)
	}
	if !strings.Contains(cfg.ToolkitWarning, "delegation.nj_agents_dir") ||
		!strings.Contains(cfg.ToolkitWarning, "delegation.nj_agents_ref") {
		t.Errorf("warning must name both superseded keys, got %q", cfg.ToolkitWarning)
	}
}

// toolkit.dir unset: the delegation value fills in, and since nothing was
// actually superseded there is nothing to warn about.
func TestDelegationDirUsedWhenToolkitDirUnset(t *testing.T) {
	cfg := loadJSON(t, `{"delegation":{"nj_agents_dir":"/old/kit"}}`)
	if cfg.Toolkit.Dir != "/old/kit" {
		t.Errorf("delegation.nj_agents_dir must fill toolkit.dir, got %q", cfg.Toolkit.Dir)
	}
	if cfg.ToolkitWarning != "" {
		t.Errorf("no deprecated key was superseded, want no warning, got %q", cfg.ToolkitWarning)
	}
}

// toolkit.dir set, delegation.nj_agents_dir unset: toolkit's value is used
// and, again, there is nothing superseded to warn about.
func TestToolkitDirUsedWhenDelegationDirUnset(t *testing.T) {
	cfg := loadJSON(t, `{"toolkit":{"dir":"/new/kit"}}`)
	if cfg.Toolkit.Dir != "/new/kit" {
		t.Errorf("toolkit.dir must be used, got %q", cfg.Toolkit.Dir)
	}
	if cfg.ToolkitWarning != "" {
		t.Errorf("no deprecated key was superseded, want no warning, got %q", cfg.ToolkitWarning)
	}
}

// The baseline: no toolkit block, no delegation aliases at all -- the
// warning stays the empty string, not nil-vs-empty or some other sentinel.
func TestToolkitWarningEmptyStringWhenNoDeprecationApplies(t *testing.T) {
	cfg := loadJSON(t, `{}`)
	if cfg.ToolkitWarning != "" {
		t.Errorf("ToolkitWarning = %q, want \"\"", cfg.ToolkitWarning)
	}
}
