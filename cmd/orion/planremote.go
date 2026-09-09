package main

// The remote as a step of the chain (docs/decisions/0022).
//
// It was `orion provision`: a separate command, typed after the chain, that
// created the GitHub repository and pushed the branch model. Separate meant
// forgettable, and forgotten meant a tracker tree naming branches nobody
// could push to. It is a frame step now -- Orion's own process, no model --
// between scaffold and decompose, and `orion provision` keeps working by
// building the same options this does.

import (
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/orion-sdlc/orion/internal/config"
	"github.com/orion-sdlc/orion/internal/provision"
	"github.com/orion-sdlc/orion/internal/ui"
	"github.com/orion-sdlc/orion/internal/workspace"
)

// remoteFn is provision.Remote, held in a variable so a test of the chain
// can stand something in for it: the real one creates a repository on
// GitHub, which is not a thing a unit test gets to do.
var remoteFn = provision.Remote

// remoteOptions is the one place the remote's options are assembled, for
// the chain and for `orion provision` alike, so the two cannot drift: same
// name, same description, same branch model, same visibility.
func remoteOptions(ws *workspace.Workspace, cfg config.Config, confirm func(string) bool, out io.Writer, org string) provision.Options {
	return provision.Options{
		Dir:           ws.RepoDir(),
		Name:          ws.Task.Slug,
		Description:   truncateStr(ws.Task.Idea, 200),
		DefaultBranch: cfg.VCS.DefaultBranch,
		WorkBranch:    cfg.VCS.WorkBranch,
		// Private, always, from a chain. Public is reachable only through
		// an explicit config change; a repository made public by an
		// unattended run is not something to do by default or by accident.
		Private: true,
		Org:     org,
		Confirm: confirm,
		Out:     out,
	}
}

// remoteStep creates the remote, confirm-gated inside provision.Remote, and
// records what it made. A declined confirmation comes back as an error,
// which stops the chain the way any failed step does -- nothing after this
// step can proceed without a remote.
func remoteStep(sio *stepIO, ws *workspace.Workspace) error {
	cfg := config.Load(ws.RepoDir())
	res, err := remoteFn(remoteOptions(ws, cfg, sio.Confirm, sio.Out, ws.Task.RemoteOrg))
	if err != nil {
		return err
	}
	fmt.Fprint(sio.Out, res.Summary())
	ws.Task.Remote = res.RemoteURL
	// Recorded rather than fatal: the remote exists whether or not this
	// write lands, and a resume re-discovers it from origin.
	if err := ws.SaveTask(); err != nil {
		ui.Warn(sio.Out, "could not record the remote in task.json: %v", err)
	}
	// The remote exists, so this is not a failure -- but a branch left
	// unprotected is the step not having done what it exists to do, and
	// saying "done" about it is how an unprotected main ships (OR-408).
	var short []string
	for _, br := range sortedBranches(res.Protection) {
		if res.Protection[br] != "applied" {
			short = append(short, fmt.Sprintf("%s (%s)", br, res.Protection[br]))
		}
	}
	if len(short) > 0 {
		return degraded(
			"not protected: "+strings.Join(short, ", "),
			"orion provision "+ws.ID)
	}
	if len(res.Warnings) > 0 {
		return degraded(res.Warnings[0], "orion provision "+ws.ID)
	}
	return nil
}

// sortedBranches orders protection keys so the report reads the same twice.
func sortedBranches(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// remoteDone: the remote is there when the task records one. provision.Remote
// is idempotent on an existing origin as well, so a task.json that lost the
// record costs one re-run that reports what exists rather than a duplicate.
func remoteDone(ws *workspace.Workspace) bool {
	return strings.TrimSpace(ws.Task.Remote) != ""
}
