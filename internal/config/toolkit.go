package config

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/orion-sdlc/orion/internal/toolkit"
)

// Toolkit declares WHICH skill repository a project delegates to, and what
// each stage should invoke inside it.
//
// It sits alongside Delegation rather than inside it because Delegation also
// carries budgets and risk tiers -- how much a delegated run may spend is a
// separate question from whose skills it runs.
//
// The block declares no ORDER, and cannot: sequencing across stages is
// Orion's, per decisions/0001. A stages MAP answers "what does the review
// stage run", which is methodology inside a stage; a stages LIST would answer
// "what runs after review", which is control flow across them. The two look
// almost identical in JSON, so the shape is validated rather than merely
// documented -- see parseToolkit.
type Toolkit struct {
	// Repo is the clone URL. Empty takes toolkit.RepoURL, so a project that
	// declares nothing keeps the toolkit Orion has always used.
	Repo string `json:"repo,omitempty"`
	// Ref pins the clone to a tag or branch. Empty falls back to the
	// deprecated delegation.nj_agents_ref.
	Ref string `json:"ref,omitempty"`
	// Dir points at an existing clone, overriding the derived vendor path.
	// Empty falls back to the deprecated delegation.nj_agents_dir.
	Dir string `json:"dir,omitempty"`
	// Stages maps a stage to the command that stage delegates to. Keys are
	// canonical after loading: an alias spelling resolves to the stage
	// stagePrompt's switch dispatches on.
	//
	// EMPTY IS THE DEFAULT AND MEANS "unset", never "run nothing" -- a
	// consumer that finds no command for a stage falls back to Orion's own
	// built-in prompt.
	Stages map[string]string `json:"stages,omitempty"`
}

// canonicalStages is every stage key a toolkit block may name, both
// spellings, mapped to the canonical one. Deliberately the exact set
// stagePrompt's switch knows (internal/supervisor/prompts.go): a stage that
// cannot be dispatched is a typo, and a typo silently ignored is a stage
// nobody notices is unconfigured.
var canonicalStages = map[string]string{
	"intent":    "intent",
	"spec":      "spec",
	"design":    "spec",
	"plan":      "plan",
	"ticket":    "ticket",
	"scaffold":  "scaffold",
	"decompose": "decompose",
	"build":     "build",
	"implement": "build",
	"verify":    "verify",
	"test":      "verify",
	"review":    "review",
	"pr":        "pr",
	"ship":      "pr",
}

// orderingKeys are the spellings that would express sequence. Rejected by
// name so the error can say why rather than "unknown key".
var orderingKeys = map[string]bool{"order": true, "sequence": true, "stage_order": true, "pipeline": true}

// Stage returns the command declared for a stage, or "" when none is.
//
// Accepts either spelling, because the caller is a stage name from
// supervisor's vocabulary and that vocabulary has two words for four of the
// stages. "" is a normal answer, not a failure: the caller falls back to
// Orion's built-in prompt.
func (t Toolkit) Stage(name string) string {
	return t.Stages[canonicalStages[strings.ToLower(strings.TrimSpace(name))]]
}

// Spec hands the block to the toolkit package, which resolves what a toolkit
// must ship from it. A copy rather than a shared type because that package
// cannot import config -- config imports it for RepoURL -- and one conversion
// in one place is cheaper than teaching every caller to build the struct.
func (t Toolkit) Spec() toolkit.Toolkit {
	return toolkit.Toolkit{Repo: t.Repo, Dir: t.Dir, Stages: t.Stages}
}

// parseToolkit reads the raw toolkit block, validating its SHAPE before the
// rest of the config decodes it. Raw rather than post-unmarshal because the
// two rejections that matter most -- an array of stages, an order key -- are
// invisible once JSON has been forced into a struct: an array fails the whole
// decode with a type error naming no reason, and an unknown key vanishes.
func parseToolkit(raw json.RawMessage) (Toolkit, error) {
	if isJSONArray(raw) {
		return Toolkit{}, adrError("toolkit is a list")
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return Toolkit{}, fmt.Errorf("toolkit is not an object: %w", err)
	}
	for k := range fields {
		if orderingKeys[strings.ToLower(k)] {
			return Toolkit{}, adrError("toolkit." + k + " expresses order")
		}
	}

	var tk Toolkit
	// Everything except stages decodes normally; stages is held back so its
	// keys can be checked before they are folded into a map.
	stagesRaw, hasStages := fields["stages"]
	delete(fields, "stages")
	rest, _ := json.Marshal(fields)
	if err := json.Unmarshal(rest, &tk); err != nil {
		return Toolkit{}, fmt.Errorf("toolkit block is invalid: %w", err)
	}
	if !hasStages {
		return tk, nil
	}
	if isJSONArray(stagesRaw) {
		return Toolkit{}, adrError("toolkit.stages is a list")
	}
	var declared map[string]string
	if err := json.Unmarshal(stagesRaw, &declared); err != nil {
		return Toolkit{}, fmt.Errorf("toolkit.stages is not a map of stage to command: %w", err)
	}

	tk.Stages = map[string]string{}
	// from records which spelling supplied each canonical stage, so a
	// collision can name BOTH keys. Picking one silently would leave a
	// project whose file says two things running whichever one map iteration
	// happened to reach last.
	from := map[string]string{}
	for _, key := range sortedKeys(declared) {
		name := strings.ToLower(strings.TrimSpace(key))
		if orderingKeys[name] {
			return Toolkit{}, adrError("toolkit.stages." + key + " expresses order")
		}
		canon, known := canonicalStages[name]
		if !known {
			return Toolkit{}, fmt.Errorf(
				"toolkit.stages.%s is not a stage Orion runs.\n"+
					"  Valid stages: %s.\n"+
					"  A stage name that dispatches nowhere is a typo, and Orion will "+
					"not quietly run the built-in prompt for a stage you meant to delegate.",
				key, strings.Join(stageNames(), ", "))
		}
		if prev, dup := from[canon]; dup && declared[prev] != declared[key] {
			return Toolkit{}, fmt.Errorf(
				"toolkit.stages names the %s stage twice, as %q and %q, with different commands.\n"+
					"  They are the same stage, so one of the two would be discarded. "+
					"Keep whichever you meant and delete the other.",
				canon, prev, key)
		}
		from[canon] = key
		tk.Stages[canon] = declared[key]
	}
	return tk, nil
}

// adrError phrases a rejection in the terms of the decision it enforces.
// what names the shape found, not the fix, because the fix is always the
// same one.
func adrError(what string) error {
	return fmt.Errorf(
		"%s, which expresses the ORDER stages run in.\n"+
			"  Sequencing across stages is Orion's, not a toolkit's "+
			"(decisions/0001-precedence-rule-orion-owns-orchestration.md).\n"+
			"  Declare toolkit.stages as a MAP of stage name to command: it says what a "+
			"stage runs, which is methodology inside a stage, and says nothing about "+
			"what runs next.", what)
}

func isJSONArray(raw json.RawMessage) bool {
	return strings.HasPrefix(strings.TrimSpace(string(raw)), "[")
}

// sortedKeys makes the collision error deterministic: whichever spelling
// sorts first is reported as the one already seen, on every machine.
func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func stageNames() []string {
	out := make([]string, 0, len(canonicalStages))
	for k := range canonicalStages {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// defaultToolkit fills the toolkit block from what is absent, including the
// deprecated delegation spellings. Kept beside the struct rather than in
// normalize so the fallback chain reads in one place.
func defaultToolkit(c *Config) {
	if c.Toolkit.Repo == "" {
		c.Toolkit.Repo = toolkit.RepoURL
	}
	// toolkit.dir/ref WIN over the delegation aliases they replace: the newer
	// spelling is the one a project chose deliberately.
	var superseded []string
	if c.Delegation.NJAgentsDir != "" {
		if c.Toolkit.Dir == "" {
			c.Toolkit.Dir = c.Delegation.NJAgentsDir
		} else {
			superseded = append(superseded, "delegation.nj_agents_dir (toolkit.dir wins)")
		}
	}
	if c.Delegation.NJAgentsRef != "" {
		if c.Toolkit.Ref == "" {
			c.Toolkit.Ref = c.Delegation.NJAgentsRef
		} else {
			superseded = append(superseded, "delegation.nj_agents_ref (toolkit.ref wins)")
		}
	}
	if len(superseded) > 0 {
		c.ToolkitWarning = "deprecated: " + strings.Join(superseded, ", ")
	}
	if c.Toolkit.Stages == nil {
		c.Toolkit.Stages = map[string]string{}
	}
}
