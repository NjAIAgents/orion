package supervisor

// OR-480: the constitution Orion seeds must permit the develop commits Orion's
// own planning chain makes, or every project's analyze stage reports its own
// history as a critical branch-model violation.

import (
	"strings"
	"testing"

	"github.com/orion-sdlc/orion/internal/config"
)

func TestConstitutionPromptCarriesThePlanningCarveOut(t *testing.T) {
	p, err := stagePrompt(ws(t, ""), "constitution", config.Toolkit{})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"before the\n  remote exists, the planning chain commits the planning artifacts",
		"Every implementation change still lands on a feature branch",
		"by reviewed pull request",
	} {
		if !strings.Contains(p, want) {
			t.Errorf("constitution prompt is missing %q", want)
		}
	}
	// The exception must stay narrow: it names planning artifacts, not code.
	if strings.Contains(p, "implementation change -- on") || strings.Contains(p, "source code on develop") {
		t.Error("the carve-out reads as permitting implementation commits on the work branch")
	}
}
