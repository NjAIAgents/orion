package supervisor

// OR-302 adds `orion decompose`, a native Jira-only route that a person
// invokes directly on a tasks.md. It does not touch the decompose STAGE
// prompt this package builds: that prompt still tells the planner to run
// /pm-plan, on any tracker, whether or not the project has spec-kit output
// at all -- because nothing here reads for tasks.md or for the project's
// tracker provider.

import (
	"strings"
	"testing"

	"github.com/orion-sdlc/orion/internal/config"
)

// A project with no specs/*/tasks.md is the common case, and the decompose
// stage prompt must be unaffected by it: it names no spec-kit artifact, and
// still sends the planner to /pm-plan by default.
func TestDecomposeStagePromptUnaffectedByAbsentSpecKit(t *testing.T) {
	p, err := stagePrompt(ws(t, ""), "decompose", config.Toolkit{})
	if err != nil {
		t.Fatalf("decompose: %v", err)
	}
	if !strings.Contains(p, "/pm-plan") {
		t.Errorf("the decompose stage prompt must still point the planner at /pm-plan:\n%s", p)
	}
	for _, absent := range []string{"tasks.md", "speckit", "spec-kit"} {
		if strings.Contains(strings.ToLower(p), strings.ToLower(absent)) {
			t.Errorf("the stage prompt must not name a spec-kit artifact (%q); a project\n"+
				"with none must decompose exactly as before:\n%s", absent, p)
		}
	}
}

// A project on Linear, Notion or GitHub Issues has no native `orion
// decompose` route (Jira-only per OR-303's sequencing) and must keep using
// /pm-plan through the stage prompt. Nothing in stagePrompt reads the
// tracker provider, so the prompt is identical regardless of it -- asserted
// here across the trackers that matter rather than assumed.
func TestDecomposeStagePromptStaysOnPmPlanForEveryTracker(t *testing.T) {
	for _, provider := range []string{"jira", "linear", "notion", "github"} {
		t.Run(provider, func(t *testing.T) {
			w := ws(t, `{"tracker":{"enabled":true,"provider":"`+provider+`"}}`)
			tk := config.Load(w.RepoDir()).Toolkit

			p, err := stagePrompt(w, "decompose", tk)
			if err != nil {
				t.Fatalf("decompose: %v", err)
			}
			if !strings.Contains(p, "/pm-plan") {
				t.Errorf("provider %q: the decompose stage prompt must still say /pm-plan,\n"+
					"since only Jira has a native route: %s", provider, p)
			}
		})
	}
}
