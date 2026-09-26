package main

import (
	"fmt"
	"os/exec"
	"strconv"
	"strings"

	"github.com/orion-sdlc/orion/internal/ui"
	"github.com/orion-sdlc/orion/internal/workspace"
)

// listOwnersFn is a seam over listGitHubOwners, so the question is testable
// without an account.
var listOwnersFn = listGitHubOwners

// listGitHubOwners returns the signed-in gh user and the organisations it
// belongs to, in the order GitHub lists them.
func listGitHubOwners() (user string, orgs []string, err error) {
	out, err := exec.Command("gh", "api", "user", "-q", ".login").Output()
	if err != nil {
		return "", nil, fmt.Errorf("reading the gh account: %w", err)
	}
	user = strings.TrimSpace(string(out))
	out, err = exec.Command("gh", "api", "user/orgs", "--paginate", "-q", ".[].login").Output()
	if err != nil {
		return user, nil, fmt.Errorf("listing organisations: %w", err)
	}
	for _, l := range strings.Fields(string(out)) {
		orgs = append(orgs, l)
	}
	return user, orgs, nil
}

// chooseRemoteOwner asks which account or organisation the repository is
// created in, when nobody said (OR-490).
//
// Asked only when there is a real choice and a person to make it: --org, or
// an owner recorded by an earlier run, is the answer already; a
// non-interactive run, an account in no organisation, or an org list that
// cannot be read all keep today's behaviour -- the personal account --
// without a question.
//
// The answer is recorded in task.json as the owner's login, personal account
// included, so a resume creates the same repository without asking again.
func chooseRemoteOwner(sio *stepIO, ws *workspace.Workspace) {
	if strings.TrimSpace(ws.Task.RemoteOrg) != "" || sio.Ask == nil {
		return
	}
	user, orgs, err := listOwnersFn()
	if err != nil || user == "" || len(orgs) == 0 {
		return
	}
	owners := append([]string{user}, orgs...)

	fmt.Fprintln(sio.Out, "\n  Where should the repository be created?")
	for i, o := range owners {
		note := ""
		if i == 0 {
			note = ui.Dim(sio.Out, "  (your account)")
		}
		fmt.Fprintf(sio.Out, "    %d) %s%s\n", i+1, o, note)
	}

	choice := ""
	for attempt := 0; attempt < 3 && choice == ""; attempt++ {
		ans := strings.TrimSpace(sio.Ask(fmt.Sprintf("  Choose 1-%d, or press enter for 1:", len(owners))))
		if ans == "" {
			choice = user
			break
		}
		if n, err := strconv.Atoi(ans); err == nil && n >= 1 && n <= len(owners) {
			choice = owners[n-1]
			break
		}
		// A name typed instead of a number is accepted when it is on the list.
		for _, o := range owners {
			if strings.EqualFold(ans, o) {
				choice = o
			}
		}
		if choice == "" {
			fmt.Fprintf(sio.Out, "          %s\n", ui.Dim(sio.Out, fmt.Sprintf("%q is not one of 1-%d", ans, len(owners))))
		}
	}
	if choice == "" {
		choice = user
		ui.Warn(sio.Out, "no valid choice; creating it under your account, %s", user)
	}

	ws.Task.RemoteOrg = choice
	if err := ws.SaveTask(); err != nil {
		ui.Warn(sio.Out, "could not record the owner in task.json: %v -- a resume will ask again", err)
	}
}
