package supervisor

// An agent-shaped project gets the /agentic-design note in its spec and plan
// prompts; every other project gets exactly the prompt it always had (OR-477).

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/orion-sdlc/orion/internal/config"
)

const agentIntent = "# Log triage\n\nAn LLM agent reads failing service logs, uses tool calling to " +
	"search runbooks and open a ticket, and loops until it names a likely cause.\n"

const plainIntent = "# Address book\n\nA REST service that stores customer addresses and " +
	"validates postcodes against a lookup table.\n"

// withSkill decides, for one test, whether nj-agents ships the skill.
func withSkill(t *testing.T, installed bool) {
	t.Helper()
	old := agenticDesignInstalled
	agenticDesignInstalled = func() bool { return installed }
	t.Cleanup(func() { agenticDesignInstalled = old })
}

func writeIntent(t *testing.T, repoDir, rel, body string) {
	t.Helper()
	abs := filepath.Join(repoDir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(abs, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func promptFor(t *testing.T, stage, intent string, installed bool) string {
	t.Helper()
	withSkill(t, installed)
	w := ws(t, "")
	if intent != "" {
		writeIntent(t, w.RepoDir(), config.Load(w.RepoDir()).IntentPath(w.Task.Slug), intent)
	}
	p, err := stagePrompt(w, stage, config.Toolkit{})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestSpecAndPlanNameTheSkillForAnAgentShapedIntent(t *testing.T) {
	for _, stage := range []string{"spec", "plan"} {
		p := promptFor(t, stage, agentIntent, true)
		if !strings.Contains(p, "/agentic-design") {
			t.Errorf("%s prompt never names /agentic-design for an agent-shaped intent:\n%s", stage, p)
		}
		if !strings.Contains(p, "## Agentic design") {
			t.Errorf("%s prompt does not name the section the skill owes", stage)
		}
	}
}

// Byte-identical, not merely "does not mention it": the guarantee is that a
// project this does not apply to runs the prompt it always ran.
func TestPromptIsUnchangedUnlessBothConditionsHold(t *testing.T) {
	for _, stage := range []string{"spec", "plan"} {
		baseline := promptFor(t, stage, plainIntent, false)
		for _, c := range []struct {
			name      string
			intent    string
			installed bool
		}{
			{"skill installed, intent not agent-shaped", plainIntent, true},
			{"agent-shaped intent, skill not installed", agentIntent, false},
		} {
			got := promptFor(t, stage, c.intent, c.installed)
			// The intent text itself is not in the prompt, so the only way the
			// two can differ is the note.
			if got != baseline {
				t.Errorf("%s / %s: prompt changed although the note must not apply", stage, c.name)
			}
		}
	}
}

func TestNoIntentFileMeansNoNote(t *testing.T) {
	if p := promptFor(t, "spec", "", true); strings.Contains(p, "/agentic-design") {
		t.Errorf("the note appeared with no intent to read")
	}
}

func TestAgentShapedNeedsTwoKindsOfEvidence(t *testing.T) {
	for _, c := range []struct {
		text string
		want bool
	}{
		{agentIntent, true},
		{plainIntent, false},
		{"Parse the browser user agent string for analytics.", false},
		{"Estate agents list properties; buyers filter them.", false},
		{"We use Claude for summaries.", false}, // one signal only
		{"A support agent backed by Claude answers questions.", true},
		{"An agentic workflow with an evaluation harness.", true},
	} {
		if got := agentShaped(c.text); got != c.want {
			t.Errorf("agentShaped(%q) = %v, want %v", c.text, got, c.want)
		}
	}
}
