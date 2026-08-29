package workspace

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// Where a ticket's ACTUAL branch lives between `orion work` and `orion
// collect`.
//
// AddWorktree may suffix the desired branch name -- orion/or-156 becomes
// orion/or-156-2 -- to keep a retried attempt from stacking its commits on a
// prior attempt's still-open pull request. That suffix is decided once, here,
// at creation time. Nothing else should ever recompute it: collect used to
// rebuild the name from the ticket key by convention, which matched on a
// ticket's first attempt and silently stopped matching on every retry after
// (OR-173).
func branchesPath(wsDir string) string {
	return filepath.Join(wsDir, ".orion", "branches.json")
}

type branchFile struct {
	Version  int               `json:"version"`
	Branches map[string]string `json:"branches"`
}

func loadBranches(wsDir string) branchFile {
	f := branchFile{Version: 1, Branches: map[string]string{}}
	b, err := os.ReadFile(branchesPath(wsDir))
	if err != nil {
		return f
	}
	if json.Unmarshal(b, &f) != nil || f.Branches == nil {
		return branchFile{Version: 1, Branches: map[string]string{}}
	}
	return f
}

// RecordBranch remembers the branch a job actually used for a ticket.
//
// Overwrites any earlier value on purpose: a retried ticket gets a new job
// and a new branch, and the record must follow the ticket's CURRENT attempt,
// not whichever one happened first.
func RecordBranch(ws *Workspace, key, branch string) error {
	f := loadBranches(ws.Dir)
	f.Branches[key] = branch
	p := branchesPath(ws.Dir)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

// BranchOf returns the branch recorded for a ticket, and whether one was
// found at all. False means no job has ever recorded one for this key --
// the caller falls back to guessing.
func BranchOf(ws *Workspace, key string) (string, bool) {
	branch, ok := loadBranches(ws.Dir).Branches[key]
	return branch, ok
}
