package config

import (
	"strings"
	"testing"

	"github.com/orion-sdlc/orion/internal/njagents"
)

// toolkitErr is the field Validate reports from; a bad block must land there,
// and the rest of the file -- including Degraded -- must be untouched by it.
func TestToolkitErrHeldAndReportedByValidate(t *testing.T) {
	cfg := loadJSON(t, `{"toolkit":{"stages":["spec"]},"limits":{"max_tool_calls":42}}`)
	if cfg.toolkitErr == nil {
		t.Fatal("toolkitErr must hold the parsing error")
	}
	if err := cfg.Validate(); err != cfg.toolkitErr {
		t.Errorf("Validate() = %v, want the same error held in toolkitErr (%v)", err, cfg.toolkitErr)
	}
	if cfg.Degraded {
		t.Error("a bad toolkit block must not degrade the rest of the file")
	}
	if cfg.Limits.MaxToolCalls != 42 {
		t.Errorf("max_tool_calls = %d, want the configured 42", cfg.Limits.MaxToolCalls)
	}
}

// The ordering-key error must cite the actual decision file, not just the
// ADR number -- a project grepping for the string should land on the file.
func TestOrderingKeyErrorCitesTheDecisionFilePath(t *testing.T) {
	err := loadJSON(t, `{"toolkit":{"order":["spec","plan"],"stages":{"spec":"/plan"}}}`).Validate()
	if err == nil {
		t.Fatal("an order key must be rejected")
	}
	want := "decisions/0001-precedence-rule-orion-owns-orchestration.md"
	if !strings.Contains(err.Error(), want) {
		t.Errorf("error must cite %q, got: %v", want, err)
	}
}

// A toolkit block that isn't an object at all (a string, say) is a different
// complaint than a stages block with the wrong value type -- the two must not
// collapse into the same message.
func TestParseErrorDistinguishesNotAnObjectFromBadStagesShape(t *testing.T) {
	notObject := loadJSON(t, `{"toolkit":"nj-agents"}`).Validate()
	if notObject == nil {
		t.Fatal("a non-object toolkit block must be rejected")
	}
	if !strings.Contains(notObject.Error(), "not an object") {
		t.Errorf("error must say the block is not an object, got: %v", notObject)
	}

	badStages := loadJSON(t, `{"toolkit":{"stages":{"spec":123}}}`).Validate()
	if badStages == nil {
		t.Fatal("a stages map with a non-string value must be rejected")
	}
	if !strings.Contains(badStages.Error(), "not a map of stage to command") {
		t.Errorf("error must say the block is not a map of stage to command, got: %v", badStages)
	}
	if notObject.Error() == badStages.Error() {
		t.Error("the two shape failures must not produce identical messages")
	}
}

// A field that is well-formed JSON but the wrong type for Toolkit (repo as a
// number, not a string) fails the struct decode of the rest of the block --
// a third distinct failure from "not an object" and "bad stages shape".
func TestBadFieldTypeFailsStructDecodeOfRest(t *testing.T) {
	err := loadJSON(t, `{"toolkit":{"repo":123}}`).Validate()
	if err == nil {
		t.Fatal("a non-string repo must be rejected")
	}
	if !strings.Contains(err.Error(), "toolkit block is invalid") {
		t.Errorf("error must say the toolkit block is invalid, got: %v", err)
	}
}

// Unknown-stage, collision, and ordering errors must each read distinctly --
// a caller matching on message text should never confuse one for another.
func TestUnknownStageCollisionAndOrderingErrorsAreDistinct(t *testing.T) {
	unknown := loadJSON(t, `{"toolkit":{"stages":{"deploy":"/ship-it"}}}`).Validate()
	collision := loadJSON(t, `{"toolkit":{"stages":{"spec":"/plan","design":"/design-it"}}}`).Validate()
	ordering := loadJSON(t, `{"toolkit":{"order":["spec"],"stages":{"spec":"/plan"}}}`).Validate()

	for _, err := range []error{unknown, collision, ordering} {
		if err == nil {
			t.Fatal("all three cases must be rejected")
		}
	}
	if unknown.Error() == collision.Error() || unknown.Error() == ordering.Error() || collision.Error() == ordering.Error() {
		t.Errorf("messages must be distinct:\nunknown=%v\ncollision=%v\nordering=%v", unknown, collision, ordering)
	}
}

// A declared stage with an empty command string is a valid, if pointless,
// declaration -- Stage still reads back the empty string, not a rejection.
func TestStagesMapEntryWithEmptyCommandIsValid(t *testing.T) {
	cfg := loadJSON(t, `{"toolkit":{"stages":{"spec":""}}}`)
	if err := cfg.Validate(); err != nil {
		t.Fatalf("an empty command string is valid: %v", err)
	}
	if got := cfg.Toolkit.Stage("spec"); got != "" {
		t.Errorf("stage spec = %q, want \"\"", got)
	}
}

// An explicit empty stages map is valid and behaves exactly like an absent
// one: every stage answers "".
func TestStagesMapWithZeroEntriesIsValid(t *testing.T) {
	cfg := loadJSON(t, `{"toolkit":{"stages":{}}}`)
	if err := cfg.Validate(); err != nil {
		t.Fatalf("an empty stages map is valid: %v", err)
	}
	if len(cfg.Toolkit.Stages) != 0 {
		t.Errorf("stages = %v, want empty", cfg.Toolkit.Stages)
	}
	if got := cfg.Toolkit.Stage("spec"); got != "" {
		t.Errorf("stage spec = %q, want \"\"", got)
	}
}

// Declaring every canonical stage at once is valid -- no limit on how many
// entries a stages map may carry.
func TestStagesMapWithManyEntriesWorks(t *testing.T) {
	body := `{"toolkit":{"stages":{
		"intent":"/i","spec":"/s","plan":"/p","ticket":"/t","scaffold":"/sc",
		"decompose":"/d","build":"/b","verify":"/v","review":"/r","pr":"/pr"
	}}}`
	cfg := loadJSON(t, body)
	if err := cfg.Validate(); err != nil {
		t.Fatalf("ten distinct canonical stages is valid: %v", err)
	}
	if len(cfg.Toolkit.Stages) != 10 {
		t.Errorf("stages = %d entries, want 10", len(cfg.Toolkit.Stages))
	}
}

// A very long but well-formed repo URL is just a string to this block; it is
// stored and read back unchanged, no length limit enforced.
func TestRepoURLWithVeryLongPathIsHandled(t *testing.T) {
	longPath := strings.Repeat("a/", 200) + "kit.git"
	repo := "https://example.com/" + longPath
	cfg := loadJSON(t, `{"toolkit":{"repo":"`+repo+`"}}`)
	if err := cfg.Validate(); err != nil {
		t.Fatalf("a long repo path is still valid: %v", err)
	}
	if cfg.Toolkit.Repo != repo {
		t.Errorf("repo round-trip failed for a long path")
	}
}

// Naming every stage under both its alias and its canonical spelling, all
// with matching commands, is not a collision anywhere -- every pair agrees.
func TestBothAliasSpellingsOfEveryStageWithSameCommandsWork(t *testing.T) {
	body := `{"toolkit":{"stages":{
		"spec":"/x","design":"/x",
		"build":"/y","implement":"/y",
		"verify":"/z","test":"/z",
		"pr":"/w","ship":"/w"
	}}}`
	cfg := loadJSON(t, body)
	if err := cfg.Validate(); err != nil {
		t.Fatalf("identical commands under both spellings must not collide: %v", err)
	}
	for stage, want := range map[string]string{"spec": "/x", "build": "/y", "verify": "/z", "pr": "/w"} {
		if got := cfg.Toolkit.Stage(stage); got != want {
			t.Errorf("stage %s = %q, want %q", stage, got, want)
		}
	}
}

// toolkit.repo explicitly set to "" is the same as omitting it: the nj-agents
// default fills in, not a blank repo URL.
func TestEmptyRepoStringGetsNJAgentsDefault(t *testing.T) {
	cfg := loadJSON(t, `{"toolkit":{"repo":""}}`)
	if cfg.Toolkit.Repo != njagents.RepoURL {
		t.Errorf("repo = %q, want the nj-agents default %q", cfg.Toolkit.Repo, njagents.RepoURL)
	}
}
