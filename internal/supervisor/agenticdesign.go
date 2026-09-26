package supervisor

import (
	"os"
	"path/filepath"
	"regexp"

	"github.com/orion-sdlc/orion/internal/toolkit"
	"github.com/orion-sdlc/orion/internal/workspace"
)

// agenticDesignSkill is the nj-agents skill that designs the parts of an
// agent-shaped system a general spec pass leaves out: loop bounds, tool
// contracts, state, a discriminating eval plan and the harness (OR-476).
const agenticDesignSkill = "agentic-design"

// agenticDesignInstalled reports whether nj-agents ships the skill.
//
// Asked of nj-agents specifically -- toolkit.Toolkit{} is its default -- and
// NOT of the project's configured toolkit. A project whose stages delegate to
// spec-kit still has nj-agents installed globally, and that is where this
// skill lives; asking the configured toolkit would make the note vanish on
// exactly the projects Orion now scaffolds by default.
//
// A variable so tests decide the answer instead of whatever this machine has
// installed.
var agenticDesignInstalled = func() bool {
	return toolkit.HasSkill(toolkit.Discover(workspace.Home(), toolkit.Toolkit{}), agenticDesignSkill)
}

// agentSignals are the phrases that mark an intent as agent-shaped. Each is a
// separate KIND of evidence, and agentShaped wants two of them: "agent" alone
// matches a user agent or an estate agent, and one match on its own is not a
// design worth writing a loop bound for.
var agentSignals = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\bllms?\b|\blarge language model`),
	regexp.MustCompile(`(?i)\bagentic\b|\b(ai|llm|autonomous|coding|support|triage|research)[- ]agents?\b`),
	regexp.MustCompile(`(?i)\btool[- ]?(use|calls?|calling)\b|\bfunction[- ]calling\b`),
	regexp.MustCompile(`(?i)\b(claude|gpt-?\d|openai|anthropic|gemini|bedrock)\b`),
	regexp.MustCompile(`(?i)\b(agent|reasoning) loop\b|\bmulti[- ]step reasoning\b`),
	regexp.MustCompile(`(?i)\b(evals?|evaluation (suite|harness))\b`),
}

// agentShaped reports whether an intent describes a system whose core is a
// model deciding what to do next. Deterministic on purpose, for the reason
// OR-424 gave for prose dependencies: a pattern returns the same answer every
// time and can be tested; a model asked "is this an agent?" returns a
// plausible one.
func agentShaped(text string) bool {
	kinds := 0
	for _, re := range agentSignals {
		if re.MatchString(text) {
			kinds++
			if kinds >= 2 {
				return true
			}
		}
	}
	return false
}

// agenticDesignNote asks the spec or plan stage to run /agentic-design, or
// returns "" -- which leaves the prompt byte for byte what it was.
//
// Both conditions are required: the skill has to be installed, and the intent
// has to be agent-shaped. Orion still sequences nothing new here (decisions/
// 0001): the skill runs inside this stage and adds a section to the file this
// stage already owes.
func agenticDesignNote(ws *workspace.Workspace, stage, intentPath, spec string) string {
	if !agenticDesignInstalled() {
		return ""
	}
	b, err := os.ReadFile(filepath.Join(ws.RepoDir(), filepath.FromSlash(intentPath)))
	if err != nil || !agentShaped(string(b)) {
		return ""
	}
	switch stage {
	case "spec", "design":
		return join(
			"THIS SYSTEM IS AGENT-SHAPED -- a model decides what happens next. Also run",
			"/"+agenticDesignSkill+" and have it write its `## Agentic design` section into "+spec+":",
			"loop bounds as named config, a contract per tool, the state carried between",
			"steps, the eval plan as its fenced json block, and the harness. The eval plan",
			"is parsed later to scaffold evals/cases/, so keep its field names exactly.",
			"Framework, model and hosting choices the intent did not name are Open",
			"Questions, not defaults.",
		)
	case "plan":
		return join(
			"THIS SYSTEM IS AGENT-SHAPED. "+spec+" carries an `## Agentic design` section;",
			"if it does not, run /"+agenticDesignSkill+" to write one there before planning.",
			"The plan must schedule its parts as work: the loop-bound config keys, a test",
			"per tool contract, and the eval harness. If planning changes a decision in",
			"that section, update the section too -- the eval plan there is what the",
			"scaffold stage reads.",
		)
	}
	return ""
}
