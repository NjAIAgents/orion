package main

// The chain's decompose and release steps.
//
// Decompose is native when the plan stage left a tasks.md and supervised
// otherwise; release is opt-in and attaches the tree to a version. Both
// read the tree from the same task list the plan stage wrote, at the exact
// path the chain pinned (SPECIFY_FEATURE_DIRECTORY), never by searching --
// the chain knows which feature it is planning.

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/orion-sdlc/orion/internal/config"
	"github.com/orion-sdlc/orion/internal/decompose"
	"github.com/orion-sdlc/orion/internal/supervisor"
	"github.com/orion-sdlc/orion/internal/tracker"
	"github.com/orion-sdlc/orion/internal/ui"
	"github.com/orion-sdlc/orion/internal/workspace"
)

// errNotApplicable is a fallback frame step saying the artifact it works
// from is not there, so the supervised stage of the same name runs instead.
var errNotApplicable = errors.New("not applicable")

// releaseTracker opens the tracker the release step writes to. A variable
// so tests stand in a fake.
var releaseTracker = func() (releaseAPI, error) { return tracker.NewJiraFromEnv() }

// tasksPath is where the plan stage's task list is, under the feature
// directory the chain pinned.
func tasksPath(ws *workspace.Workspace) string {
	cfg := config.Load(ws.RepoDir())
	return filepath.Join(ws.RepoDir(), filepath.FromSlash(cfg.FeatureDir(ws.Task.Slug)), "tasks.md")
}

// bindingKey is the tracker project the workspace was planned for, or "".
func bindingKey(ws *workspace.Workspace) string {
	var b tracker.Binding
	if len(ws.Task.Tracker) > 0 && json.Unmarshal(ws.Task.Tracker, &b) == nil {
		return strings.ToUpper(strings.TrimSpace(b.Key))
	}
	return ""
}

// decomposeStep creates the tracker tree from tasks.md, or declines when
// there is none so the supervised stage runs.
func decomposeStep(sio *stepIO, ws *workspace.Workspace) error {
	path := tasksPath(ws)
	if _, err := os.Stat(path); err != nil {
		rel, _ := filepath.Rel(ws.RepoDir(), path)
		fmt.Fprintf(sio.Out, "  %s\n", ui.Dim(sio.Out, "no "+filepath.ToSlash(rel)+" here, so the decompose stage runs its configured command instead"))
		return errNotApplicable
	}
	project := bindingKey(ws)
	if project == "" {
		return fmt.Errorf("this workspace records no tracker binding, so there is no project to create the tree in.\n  orion plan <KEY> binds one; orion decompose <KEY> %s creates the tree by hand", path)
	}
	return decomposeTree(sio.Out, ws.RepoDir(), project, path, sio.Confirm)
}

// decomposeDone is "every item in the task list is in the tracker" when
// there is a task list, and the supervised stage's own record otherwise.
// One tracker search per resume, the same one Build makes before creating.
func decomposeDone(ws *workspace.Workspace) bool {
	plan, ok := treePlan(ws)
	if !ok {
		return supervisor.StageDone(ws, "decompose")
	}
	return plan != nil && plan.NewCount() == 0
}

// treePlan resolves the task list against the tracker. ok is false when
// there is no task list to read; a nil plan with ok true means the tracker
// could not be asked, which is "not done" rather than "no such step".
func treePlan(ws *workspace.Workspace) (*decompose.Plan, bool) {
	path := tasksPath(ws)
	text, err := os.ReadFile(path)
	if err != nil {
		return nil, false
	}
	project := bindingKey(ws)
	if project == "" {
		return nil, true
	}
	tree, err := decompose.Parse(string(text), path)
	if err != nil {
		return nil, true
	}
	b, err := decomposeBackend(ws.RepoDir())
	if err != nil {
		return nil, true
	}
	plan, err := decompose.Build(tree, b, project)
	if err != nil {
		return nil, true
	}
	return plan, true
}

// releaseStep creates the version --release named and attaches every
// ticket in the tree to it. Without --release it has nothing to do and
// says so.
func releaseStep(sio *stepIO, ws *workspace.Workspace) error {
	version := strings.TrimSpace(ws.Task.ReleaseVersion)
	if version == "" {
		fmt.Fprintf(sio.Out, "  %s\n", ui.Dim(sio.Out, "= skipped (no --release)"))
		return nil
	}
	project := bindingKey(ws)
	if project == "" {
		return errors.New("this workspace records no tracker binding, so there is no project to create the version in")
	}
	plan, ok := treePlan(ws)
	if !ok {
		return fmt.Errorf("no %s to read the tree from, so its tickets cannot be attached here.\n  Attach them by hand: orion release add %s <KEY>...", filepath.Base(tasksPath(ws)), version)
	}
	if plan == nil {
		return errors.New("the tracker could not be asked which tickets the tree holds")
	}
	var keys []string
	for _, s := range plan.Steps {
		if s.ExistingKey != "" {
			keys = append(keys, s.ExistingKey)
		}
	}
	if len(keys) == 0 {
		return errors.New("the tree is not in the tracker yet, so there is nothing to attach: run decompose first")
	}

	j, err := releaseTracker()
	if err != nil {
		return err
	}
	v, created, err := j.CreateVersion(project, version, "")
	if err != nil {
		return err
	}
	if created {
		ui.Ok(sio.Out, "created", "version %s on %s", v.Name, project)
	} else {
		ui.Ok(sio.Out, "exists", "version %s already exists on %s", v.Name, project)
	}
	if err := attachToVersion(j, sio.Out, project, version, keys, false); err != nil {
		return err
	}
	ws.Task.Released = version
	return ws.SaveTask()
}

// releaseDone: nothing asked for, or the asked-for version attached.
func releaseDone(ws *workspace.Workspace) bool {
	v := strings.TrimSpace(ws.Task.ReleaseVersion)
	return v == "" || ws.Task.Released == v
}
