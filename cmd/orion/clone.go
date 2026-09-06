package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/orion-sdlc/orion/internal/ui"
	"github.com/orion-sdlc/orion/internal/workspace"
)

// Getting the repository out of Orion's sandbox.
//
// A new project's repository is provisioned under ~/.orion/projects/<id>/repo,
// which is Orion's own working area: agents run there, worktrees are cut from
// there, and `orion rm` deletes the lot. That isolation is the point -- a bad
// run is a directory you can delete rather than a mess in your own files.
//
// But it left the work somewhere nobody would look, in a directory whose name
// says "internal", with no way to open it in an editor except by knowing the
// path. So: `orion clone <ID> <path>` puts a normal clone wherever you want
// one, and `orion new` asks up front so the common case needs no second
// command.
//
// A CLONE, NOT A MOVE. The sandbox stays where the stages expect it; moving it
// would break every worktree cut from it and every path recorded in task.json.
// What you get is an ordinary git repository whose origin is the sandbox, so
// `git pull` brings across whatever the stages have committed since.

func runClone(args []string) {
	if len(args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: orion clone <workspace-id> <path>\n"+
			"  e.g. orion clone cloudlens ~/code/cloudlens\n"+
			"  orion ls lists the workspaces you have.")
		os.Exit(64)
	}
	ws, err := workspace.Open(args[0])
	exitOn(err)
	exitOn(cloneWorkspace(os.Stdout, ws, args[1]))
}

// cloneWorkspace clones a workspace's repository to dest.
func cloneWorkspace(out *os.File, ws *workspace.Workspace, dest string) error {
	dest, err := expandPath(dest)
	if err != nil {
		return err
	}

	// REFUSED RATHER THAN MERGED INTO. Cloning onto an existing directory is
	// how someone loses uncommitted work in it, and "it already exists" is a
	// recoverable annoyance where an overwrite is not.
	if _, err := os.Stat(dest); err == nil {
		return fmt.Errorf("%s already exists.\n"+
			"  Pick another path, or pull into the one you have: git -C %s pull", dest, dest)
	}

	src := ws.RepoDir()
	if _, err := os.Stat(filepath.Join(src, ".git")); err != nil {
		return fmt.Errorf("%s has no repository yet at %s.\n"+
			"  Run the planning stages first: orion plan <KEY>", ws.ID, src)
	}

	cmd := exec.Command("git", "clone", src, dest)
	if outBytes, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("cloning %s to %s: %v\n%s", ws.ID, dest, err, strings.TrimSpace(string(outBytes)))
	}

	ui.Ok(out, "cloned", "%s -> %s", ws.ID, dest)
	fmt.Fprintf(out, "  %s\n", ui.Dim(out,
		"its origin is the sandbox, so `git pull` brings across what the stages commit next"))
	return nil
}

// expandPath resolves ~ and makes the path absolute.
//
// Done here rather than left to the shell because this path can arrive from a
// prompt as well as from a command line, and a "~/code/x" typed at a prompt is
// a literal directory named "~" if nobody expands it.
func expandPath(p string) (string, error) {
	p = strings.TrimSpace(p)
	if p == "" {
		return "", fmt.Errorf("no path given")
	}
	if p == "~" || strings.HasPrefix(p, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		p = filepath.Join(home, strings.TrimPrefix(strings.TrimPrefix(p, "~"), "/"))
	}
	return filepath.Abs(p)
}
