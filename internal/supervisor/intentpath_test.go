package supervisor

import (
	"strings"
	"testing"

	"github.com/orion-sdlc/orion/internal/config"
)

// The two sides of the intent artifact held together: the file the prompt
// asks for must be the file the artifact check demands.
//
// They were not. The prompt said "writes docs/intent/<slug>.md" and never
// said what the slug was, so the agent chose a descriptive name --
// docs/intent/cloudhealth-replacement-aws-cost-tool.md -- while the check
// looked for docs/intent/cloudlens.md and reported that the stage had written
// nothing. It had written a good file at a name nobody would read, and the
// chain stopped with two and a half minutes spent.
//
// One test rather than two: a test that checks only the prompt, and another
// that checks only the artifact path, both pass while the two disagree.
func TestTheIntentPromptNamesTheFileTheCheckDemands(t *testing.T) {
	w := ws(t, `{}`)
	w.Task.Slug = "cloudlens"

	p, err := stagePrompt(w, "intent", config.Toolkit{})
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Load(w.RepoDir())

	want := stageArtifact(cfg, "intent", w.Task.Slug)
	if want == "" {
		t.Fatal("the intent stage owes no artifact, so nothing holds this together")
	}
	if !strings.Contains(p, want) {
		t.Errorf("the prompt never names %q, which is the file the check demands:\n%s", want, p)
	}
	// And it must not leave the slug as a placeholder for the agent to fill.
	if strings.Contains(p, "<slug>") {
		t.Errorf("the prompt still says <slug>; an agent has to invent one:\n%s", p)
	}
}

// A project that moves paths.intent moves it for both sides at once.
func TestAMovedIntentDirectoryMovesForThePromptToo(t *testing.T) {
	w := ws(t, `{"paths":{"intent":"docs/product/intent"}}`)
	w.Task.Slug = "thing"

	p, err := stagePrompt(w, "intent", config.Toolkit{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(p, "docs/product/intent/thing.md") {
		t.Errorf("the prompt does not follow the configured intent path:\n%s", p)
	}
}

// An instruction that sends an agent hunting for its own input is worse than
// no instruction. The first version said "IF THE TRACKER HOLDS AN IDEA" and
// the agent spent ninety seconds reading orion's help output to find out.
func TestTheIdeaIsNamedWhenThereIsOneAndOmittedWhenThereIsNot(t *testing.T) {
	w := ws(t, `{}`)
	w.Task.Slug = "thing"

	w.Task.IdeaKey = "PRIOR-3"
	withKey, err := stagePrompt(w, "intent", config.Toolkit{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(withKey, "PRIOR-3") {
		t.Errorf("the prompt does not name the idea it is asking about:\n%s", withKey)
	}
	if !strings.Contains(withKey, "orion idea fields PRIOR-3") {
		t.Error("the prompt does not give the command with the key already in it")
	}

	w.Task.IdeaKey = ""
	without, err := stagePrompt(w, "intent", config.Toolkit{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(without, "orion idea") {
		t.Errorf("with no idea to fill in, the prompt still asks for one:\n%s", without)
	}
}
