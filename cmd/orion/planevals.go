package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/orion-sdlc/orion/internal/ciscaffold"
	"github.com/orion-sdlc/orion/internal/config"
	"github.com/orion-sdlc/orion/internal/supervisor"
	"github.com/orion-sdlc/orion/internal/ui"
	"github.com/orion-sdlc/orion/internal/workspace"
)

// evalsStep scaffolds an agent-shaped project's eval suite from the plan the
// spec carries (OR-478).
//
// The spec stage has /agentic-design write an "## Agentic design" section
// whose eval plan is a fixed-shape json block (OR-476, OR-477). This turns it
// into files that run: a case per case, a harness calling the project's own
// agent, and the agent-evals CI job auto_merge.require_checks already names.
// auto_merge itself is left alone -- starter cases are not min_eval_cases
// real ones, and a scaffold that flipped the gate on would make green mean
// nothing.
//
// Placed after scaffold and before remote, and it COMMITS what it writes:
// remote pushes the branches as they stand, so a file left uncommitted here
// would never reach the repository the tickets are worked in.
//
// A project with no eval plan -- every project that is not agent-shaped --
// gets nothing and a one-line note.
func evalsStep(sio *stepIO, ws *workspace.Workspace) error {
	plan, specRel, err := evalPlanFor(ws)
	if err != nil {
		// A malformed plan is worth saying, not worth stopping the chain for:
		// the project still builds without evals, and the spec can be fixed.
		ui.Warn(sio.Out, "evals: %s: %v", specRel, err)
		return nil
	}
	if plan == nil {
		fmt.Fprintf(sio.Out, "          %s\n", ui.Dim(sio.Out,
			"no eval plan in "+specRel+" -- nothing to scaffold (not agent-shaped)"))
		return nil
	}
	res, err := ciscaffold.EnsureEvals(ws.RepoDir(), plan)
	if err != nil {
		return fmt.Errorf("scaffolding evals: %w", err)
	}
	for _, line := range ciscaffold.DescribeEvals(res) {
		if strings.HasPrefix(line, "created ") {
			ui.Ok(sio.Out, "created", "%s", strings.TrimPrefix(line, "created "))
		} else {
			fmt.Fprintf(sio.Out, "          %s\n", ui.Dim(sio.Out, line))
		}
	}
	if !res.Any() {
		return nil
	}
	paths := append(append([]string{}, res.CasesCreated...), res.Created...)
	if out, err := gitIn(ws.RepoDir(), append([]string{"add", "--"}, paths...)...); err != nil {
		return fmt.Errorf("staging the eval suite: %s", strings.TrimSpace(out))
	}
	msg := "chore(evals): scaffold the eval suite from the spec's agentic design (OR-478)"
	if out, err := gitIn(ws.RepoDir(), "commit", "-q", "-m", msg); err != nil {
		return fmt.Errorf("committing the eval suite: %s", strings.TrimSpace(out))
	}
	return nil
}

// evalsDone: nothing to do when the spec has no eval plan, and done once the
// harness's gate file exists -- the artifact, not a flag, per planStage.Done.
func evalsDone(ws *workspace.Workspace) bool {
	plan, _, err := evalPlanFor(ws)
	if err != nil || plan == nil {
		return true
	}
	_, statErr := os.Stat(filepath.Join(ws.RepoDir(), "evals", "gate.json"))
	return statErr == nil
}

// evalPlanFor reads the eval plan from the spec the spec stage wrote, through
// the same path helper the artifact gate uses.
func evalPlanFor(ws *workspace.Workspace) (*ciscaffold.EvalPlan, string, error) {
	rel := supervisor.SpecArtifact(config.Load(ws.RepoDir()), ws.Task.Slug)
	b, err := os.ReadFile(filepath.Join(ws.RepoDir(), filepath.FromSlash(rel)))
	if err != nil {
		return nil, rel, nil // no spec, no plan: nothing to do
	}
	p, err := ciscaffold.ParseEvalPlan(b)
	return p, rel, err
}
