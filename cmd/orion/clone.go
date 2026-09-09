package main

import (
	"fmt"
	"io"
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
//
// FROM THE REMOTE, when the workspace has one. A copy whose origin is the
// sandbox can only push into a directory ~/.orion/rm deletes, and no pull
// request can be opened from it; what people want is an ordinary checkout of
// the GitHub repository. The chain's clone step sends the branch it has been
// committing to up first, so the copy has the work in it (OR-418). Without a
// remote the sandbox is the only source there is, and stays the fallback.

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
func cloneWorkspace(out io.Writer, ws *workspace.Workspace, dest string) error {
	dest, err := expandPath(dest)
	if err != nil {
		return err
	}

	// A DIRECTORY YOU ALREADY KEEP CODE IN IS A PARENT, NOT THE TARGET.
	// "~/Desktop/github/me" is where someone's repositories live, and the
	// answer to "where do you want your copy" is nearly always that rather
	// than the repository directory itself -- which does not exist yet, so
	// it cannot be typed with any confidence. Cloning INTO it under the
	// workspace's own name is what was meant; refusing was a dead end that
	// named no way forward (FOUND ON A REAL PROJECT: the chain asked, the
	// operator gave their github folder, and the step failed).
	if info, err := os.Stat(dest); err == nil {
		if !info.IsDir() {
			return fmt.Errorf("%s exists and is not a directory.\n"+
				"  Pick another path.", dest)
		}
		inside := filepath.Join(dest, ws.ID)
		if _, err := os.Stat(inside); err == nil {
			return fmt.Errorf("%s already exists.\n"+
				"  Pick another path, or pull into the one you have: git -C %s pull", inside, inside)
		}
		dest = inside
	}

	src, from, err := cloneSource(ws)
	if err != nil {
		return err
	}

	// ON THE BRANCH THE CHAIN WORKED ON, not the repository's default.
	// Cloning leaves you on the default branch, which is develop and has
	// none of the planning artifacts on it -- so the copy opens on an
	// empty tree and the work looks lost until someone thinks to check
	// what branch they are on (FOUND ON A REAL PROJECT).
	branch := strings.TrimSpace(ws.Task.PlanBranch)
	outBytes, err := gitClone(src, dest, branch)
	if err != nil && branch != "" {
		// A branch the source does not have is not worth failing over: the
		// copy is what was asked for, so it is made without one. Clear
		// anything the failed attempt left, or the retry refuses its own
		// half-written directory.
		_ = os.RemoveAll(dest)
		branch = ""
		outBytes, err = gitClone(src, dest, "")
	}
	if err != nil {
		return fmt.Errorf("cloning %s to %s: %v\n%s", ws.ID, dest, err, strings.TrimSpace(string(outBytes)))
	}
	if branch != "" {
		from += ", on " + branch
	}

	ui.Ok(out, "cloned", "%s -> %s", ws.ID, dest)
	fmt.Fprintf(out, "  %s\n", ui.Dim(out, from))
	return nil
}

// cloneSource is what the copy is made from, and the line explaining it.
//
// THE REMOTE WHEN THERE IS ONE. A clone of the sandbox has the sandbox as
// its origin, so `git push` from it goes to a directory under ~/.orion that
// `orion rm` deletes, and a pull request cannot be opened from it at all.
// What people want from their own copy is an ordinary checkout of the
// GitHub repository (OR-418). The sandbox stays the fallback for a
// workspace with no remote, where it is the only source there is.
func cloneSource(ws *workspace.Workspace) (src, from string, err error) {
	if remote := strings.TrimSpace(ws.Task.Remote); remote != "" {
		return remote, "its origin is " + remote, nil
	}
	dir := ws.RepoDir()
	if _, err := os.Stat(filepath.Join(dir, ".git")); err != nil {
		return "", "", fmt.Errorf("%s has no repository yet at %s.\n"+
			"  Run the planning stages first: orion plan <KEY>", ws.ID, dir)
	}
	return dir, "this workspace has no remote, so its origin is the sandbox: " +
		"`git pull` brings across what the stages commit next", nil
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

// gitClone runs one clone, optionally checking out a named branch.
func gitClone(src, dest, branch string) ([]byte, error) {
	args := []string{"clone"}
	if branch != "" {
		args = append(args, "--branch", branch)
	}
	return exec.Command("git", append(args, src, dest)...).CombinedOutput()
}
