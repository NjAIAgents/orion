package ciscaffold

// Eval scaffolding for agent-shaped projects (OR-478).
//
// The nj-agents /agentic-design skill writes an eval plan into the spec as a
// fenced json block under "## Agentic design" / "### Eval plan" (OR-476). This
// turns that plan into files a project can run: one case file per case, a
// harness that calls the PROJECT'S OWN agent -- not `claude -p`, which would
// evaluate Claude Code rather than the thing being built -- and a CI job named
// agent-evals, the check auto_merge.require_checks already names.
//
// Everything is written only when absent, like the rest of this package: a
// case someone has since edited is theirs, and re-running the chain must not
// put the starter back over it.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// EvalPlan is the contract /agentic-design writes. Field names are fixed by
// that skill; changing one here without changing it there makes every plan
// parse as empty.
type EvalPlan struct {
	Gate struct {
		PassRate float64 `json:"pass_rate"`
		MinCases int     `json:"min_cases"`
	} `json:"gate"`
	Harness struct {
		Command        string `json:"command"`
		ReplaysAgainst string `json:"replays_against"`
	} `json:"harness"`
	Cases []EvalCase `json:"cases"`
}

// EvalCase is one case, in the shape evals/check.sh reads.
type EvalCase struct {
	Name           string   `json:"name"`
	Prompt         string   `json:"prompt"`
	MustContain    []string `json:"must_contain,omitempty"`
	MustNotContain []string `json:"must_not_contain,omitempty"`
	ShellCheck     string   `json:"shell_check,omitempty"`
	Discriminates  string   `json:"discriminates"`
}

var (
	sectionHeading = regexp.MustCompile(`(?m)^## Agentic design\s*$`)
	nextSection    = regexp.MustCompile(`(?m)^## `)
	evalHeading    = regexp.MustCompile(`(?m)^### Eval plan\s*$`)
	jsonFence      = regexp.MustCompile("(?s)```json\\s*\\n(.*?)\\n```")
	caseName       = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)
)

// ParseEvalPlan finds the eval plan in a spec. It returns nil, nil when the
// spec has no Agentic design section or no eval plan in it: that is the
// normal answer for every project that is not agent-shaped, not an error.
func ParseEvalPlan(spec []byte) (*EvalPlan, error) {
	loc := sectionHeading.FindIndex(spec)
	if loc == nil {
		return nil, nil
	}
	section := spec[loc[1]:]
	if end := nextSection.FindIndex(section); end != nil {
		section = section[:end[0]]
	}
	at := evalHeading.FindIndex(section)
	if at == nil {
		return nil, nil
	}
	m := jsonFence.FindSubmatch(section[at[1]:])
	if m == nil {
		return nil, errors.New("the Eval plan heading has no ```json block under it")
	}
	var p EvalPlan
	dec := json.NewDecoder(bytes.NewReader(m[1]))
	if err := dec.Decode(&p); err != nil {
		return nil, fmt.Errorf("the eval plan is not valid JSON: %w", err)
	}
	return &p, nil
}

// usable reports why a case cannot be scaffolded, or "" when it can.
//
// Every refusal is a case that would pass by construction or could not run:
// the skill's own contract says each case names what it discriminates, and a
// case with nothing to check is a green light wired to nothing.
func usable(c EvalCase) string {
	switch {
	case !caseName.MatchString(c.Name):
		return "its name is not kebab-case (it becomes a file name)"
	case strings.TrimSpace(c.Prompt) == "":
		return "it has no prompt"
	case strings.TrimSpace(c.Discriminates) == "":
		return "it does not say what wrong behaviour it catches"
	case len(c.MustContain) == 0 && len(c.MustNotContain) == 0 && strings.TrimSpace(c.ShellCheck) == "":
		return "it checks nothing, so it would pass on any answer"
	}
	return ""
}

// EvalsResult reports what EnsureEvals did.
type EvalsResult struct {
	CasesCreated []string
	Skipped      []string // "name: why", for cases refused as unusable
	Created      []string // repo-relative harness/CI files written this run
	Notes        []string
}

// Any reports whether anything was written, i.e. whether there is something
// to commit.
func (r EvalsResult) Any() bool { return len(r.CasesCreated) > 0 || len(r.Created) > 0 }

// EnsureEvals writes the eval suite a plan describes into dir.
func EnsureEvals(dir string, p *EvalPlan) (EvalsResult, error) {
	var res EvalsResult
	if p == nil {
		return res, nil
	}
	casesDir := filepath.Join(dir, "evals", "cases")
	if err := os.MkdirAll(casesDir, 0o755); err != nil {
		return res, err
	}
	for _, c := range p.Cases {
		if why := usable(c); why != "" {
			res.Skipped = append(res.Skipped, fmt.Sprintf("%s: %s", c.Name, why))
			continue
		}
		path := filepath.Join(casesDir, c.Name+".json")
		if _, err := os.Stat(path); err == nil {
			continue
		}
		b, err := json.MarshalIndent(c, "", "  ")
		if err != nil {
			return res, err
		}
		if err := os.WriteFile(path, append(b, '\n'), 0o644); err != nil {
			return res, err
		}
		res.CasesCreated = append(res.CasesCreated, "evals/cases/"+c.Name+".json")
	}

	gate := map[string]any{
		"harness_command": strings.TrimSpace(p.Harness.Command),
		"replays_against": strings.TrimSpace(p.Harness.ReplaysAgainst),
		"pass_rate":       p.Gate.PassRate,
		"min_cases":       p.Gate.MinCases,
	}
	gb, _ := json.MarshalIndent(gate, "", "  ")
	for _, f := range []struct {
		rel  string
		body string
		mode os.FileMode
	}{
		{"evals/gate.json", string(gb) + "\n", 0o644},
		{"evals/run.sh", evalRunScript, 0o755},
		{"evals/check.sh", evalCheckScript, 0o755},
		{".github/workflows/agent-evals.yml", agentEvalsWorkflow, 0o644},
	} {
		path := filepath.Join(dir, filepath.FromSlash(f.rel))
		if _, err := os.Stat(path); err == nil {
			res.Notes = append(res.Notes, f.rel+" already exists and was left alone")
			continue
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return res, err
		}
		if err := os.WriteFile(path, []byte(f.body), f.mode); err != nil {
			return res, err
		}
		res.Created = append(res.Created, f.rel)
	}

	if cmd := strings.TrimSpace(p.Harness.Command); cmd == "" || strings.EqualFold(cmd, "TBD") {
		res.Notes = append(res.Notes,
			"harness.command is not decided yet, so evals/run.sh fails until evals/gate.json names it")
	}
	if n := len(p.Cases) - len(res.Skipped); p.Gate.MinCases > 0 && n < p.Gate.MinCases {
		res.Notes = append(res.Notes, fmt.Sprintf(
			"%d starter case(s) against a minimum of %d: the rest is real work, and auto_merge stays off until it is done",
			n, p.Gate.MinCases))
	}
	return res, nil
}

// DescribeEvals renders a result as lines for the chain's output.
func DescribeEvals(r EvalsResult) []string {
	var out []string
	for _, c := range r.CasesCreated {
		out = append(out, "created "+c)
	}
	for _, f := range r.Created {
		out = append(out, "created "+f)
	}
	for _, s := range r.Skipped {
		out = append(out, "skipped case "+s)
	}
	return append(out, r.Notes...)
}

// evalRunScript runs every case through the project's own agent.
//
// The agent is whatever evals/gate.json names as harness_command: it reads
// the prompt on stdin and answers on stdout. That is the contract
// /agentic-design writes into the plan, and the reason this is not Orion's own
// evals/run.sh, which runs `claude -p` and so evaluates Claude Code.
const evalRunScript = `#!/usr/bin/env bash
# Run every eval case through this project's agent and gate on the pass rate.
# Scaffolded by Orion from the spec's Agentic design eval plan (OR-478).
#
# The agent is evals/gate.json's harness_command: prompt on stdin, answer on
# stdout. Every case names the plausible wrong answer it catches; a suite of
# happy paths passes on a broken agent.
set -uo pipefail
cd "$(dirname "$0")/.."

command -v jq >/dev/null || { echo "evals/run.sh needs jq on PATH"; exit 2; }
cmd=$(jq -r '.harness_command // ""' evals/gate.json)
min=$(jq -r '.pass_rate // 0.95' evals/gate.json)
mincases=$(jq -r '.min_cases // 0' evals/gate.json)

if [ -z "$cmd" ] || [ "$cmd" = "TBD" ]; then
  echo "evals/gate.json has no harness_command yet -- name how to run the agent on one prompt."
  exit 1
fi

RESULTS="evals/results"; mkdir -p "$RESULTS"
pass=0; total=0
shopt -s nullglob
for case_file in evals/cases/*.json; do
  total=$((total+1))
  name=$(jq -r '.name' "$case_file")
  echo "-- $name"
  if jq -r '.prompt' "$case_file" | bash -c "$cmd" > "$RESULTS/$name.out" 2> "$RESULTS/$name.err" \
     && ./evals/check.sh "$case_file" "$RESULTS/$name.out"; then
    echo "   PASS"; pass=$((pass+1))
  else
    echo "   FAIL"
  fi
done

if [ "$total" -eq 0 ]; then
  # An empty suite passing vacuously is the most dangerous outcome here.
  echo "no eval cases in evals/cases/ -- refusing to report a pass"
  exit 1
fi

rate=$(awk "BEGIN{printf \"%.3f\", $pass/$total}")
echo
echo "pass rate: $pass/$total = $rate (threshold $min)"
echo "case count: $total (minimum before any gate should trust this: $mincases)"
awk "BEGIN{exit !($rate >= $min)}" || { echo "below threshold"; exit 1; }
`

// evalCheckScript is Orion's own evals/check.sh: the case shape is shared, so
// the checker is too.
const evalCheckScript = `#!/usr/bin/env bash
# Check one eval result against the case's expectations.
#   $1 case file   $2 agent output
set -uo pipefail
case_file="$1"; result="$2"

# must_contain: strings that have to appear in the answer
while IFS= read -r needle; do
  [ -z "$needle" ] && continue
  grep -qF -- "$needle" "$result" || { echo "   missing expected: $needle"; exit 1; }
done < <(jq -r '.must_contain[]? // empty' "$case_file")

# must_not_contain: the negative cases matter more, because they catch an
# agent doing something plausible and wrong
while IFS= read -r needle; do
  [ -z "$needle" ] && continue
  if grep -qF -- "$needle" "$result"; then echo "   found forbidden: $needle"; exit 1; fi
done < <(jq -r '.must_not_contain[]? // empty' "$case_file")

# shell_check: an arbitrary command that must exit 0
cmd=$(jq -r '.shell_check // empty' "$case_file")
if [ -n "$cmd" ]; then
  bash -c "$cmd" >/dev/null 2>&1 || { echo "   shell_check failed: $cmd"; exit 1; }
fi
exit 0
`

// agentEvalsWorkflow runs the suite as the agent-evals check that
// auto_merge.require_checks names.
const agentEvalsWorkflow = `# Scaffolded by Orion from the spec's Agentic design eval plan (OR-478).
# The job name is the check auto_merge.require_checks waits for.
name: agent-evals
on:
  pull_request:
  push:
    branches: [main, develop]
permissions:
  contents: read
jobs:
  agent-evals:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - name: Run the eval suite
        run: bash evals/run.sh
`
