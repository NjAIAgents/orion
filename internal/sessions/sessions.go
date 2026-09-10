// Package sessions enumerates every workspace Orion could be running in.
//
// A run view has to start by answering "what is there", and the answer lives
// in two places that neither one alone gets right. The registry
// (internal/registry) maps a tracker project key to the workspace that owns
// it -- that is where the KEY comes from, and a workspace with no key is
// unlabelled on a board. But `orion new` and `orion plan` create workspaces
// under ~/.orion/projects before anything binds them, and a run that failed
// before adoption never gets bound at all, so a scan of the registry alone
// silently omits exactly the workspaces somebody is most likely looking for.
// Reading the projects directory alone loses the key. So: both, unioned.
//
// NOTHING HERE READS AN EVENT LOG. A Session says where a run is and what it
// is called; what happened inside it comes off events.jsonl on its own
// ticket. Splitting it that way keeps this pure -- given a home directory it
// returns the same list every time -- and keeps the expensive part (parsing
// a log per workspace) out of the cheap part (deciding which logs exist).
//
// A MISSING SOURCE IS NOT AN ERROR. No registry and no projects directory is
// what a fresh install looks like, and a board that refuses to draw on a
// machine where nothing has run yet reports a normal state as a fault.
package sessions

import (
	"os"
	"path/filepath"
	"sort"

	"github.com/orion-sdlc/orion/internal/registry"
)

// Session is one workspace: where it is, and what it is bound to.
type Session struct {
	// ID is the workspace directory name under ~/.orion/projects, which is
	// what `orion open`, `orion status` and `orion rm` all take.
	ID string
	// Dir is the absolute path to that directory. Carried rather than
	// recomputed by every caller, because the caller that wants it wants it
	// to reach .orion/events.jsonl inside, and each caller rebuilding the
	// path is each caller getting a chance to disagree about it.
	Dir string
	// Key is the tracker project key this workspace is bound to, uppercased
	// as the registry stores it.
	//
	// EMPTY MEANS UNREGISTERED, and that is a state worth showing rather
	// than hiding: a workspace with no binding is one `orion init` never
	// finished, or one whose key was unbound, and it is invisible to every
	// command that resolves work by key.
	Key string
}

// Scan lists every workspace: one per registry entry, plus every directory
// under <home>/projects that no entry claims.
//
// Registered ones come first, ordered by key, then the unregistered ones by
// id. A total order rather than whatever the filesystem hands back, because
// a list that reshuffles between two identical scans cannot be diffed, and
// the surface reading this draws a grid.
//
// A registry entry whose workspace directory has gone is still returned.
// registry.Prune already sets the rule for this tree -- report what is
// missing, never quietly forget a binding -- and dropping the entry here
// would make a vanished workspace look like one that was never bound.
func Scan(home string) ([]Session, error) {
	reg, err := registry.Load(home)
	if err != nil {
		return nil, err
	}

	projects := filepath.Join(home, "projects")
	var out []Session
	claimed := map[string]bool{}

	for _, key := range reg.Keys() {
		e := reg.Repos[key]
		claimed[e.Workspace] = true
		out = append(out, Session{
			ID:  e.Workspace,
			Dir: filepath.Join(projects, e.Workspace),
			Key: key,
		})
	}

	entries, err := os.ReadDir(projects)
	if err != nil {
		if os.IsNotExist(err) {
			return out, nil
		}
		return nil, err
	}
	var orphans []Session
	for _, de := range entries {
		if !de.IsDir() || claimed[de.Name()] {
			continue
		}
		orphans = append(orphans, Session{
			ID:  de.Name(),
			Dir: filepath.Join(projects, de.Name()),
		})
	}
	sort.Slice(orphans, func(a, b int) bool { return orphans[a].ID < orphans[b].ID })

	return append(out, orphans...), nil
}
