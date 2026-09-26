package main

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/orion-sdlc/orion/internal/collect"
	"github.com/orion-sdlc/orion/internal/config"
	"github.com/orion-sdlc/orion/internal/ui"
	"github.com/orion-sdlc/orion/internal/workspace"
)

// Seams, so the step is testable without a forge.
var (
	pubPushFn     = pushBranch
	pubOpenPRFn   = openPR
	pubPRStatusFn = prStatus
	pubSwitchFn   = workspace.SwitchSandbox
)

// scaffoldPublishStep brings the planning chain's own feature branches to the
// remote by pull request, then returns the sandbox to the work branch (OR-482).
//
// The scaffold stage works on a feature branch cut from the work branch, as
// the constitution's branch gate requires. But the remote step pushes only the
// default and work branches, so before this the scaffold existed only on a
// local branch: the repository was created without it, and every later stage
// committed onto that branch because the sandbox was still on it.
//
// For each local branch under the configured prefix that has commits the work
// branch lacks, this pushes it and opens its pull request into the work
// branch -- reviewed, never merged here. An already-open pull request is left
// alone, so a resumed chain does not open a second one.
func scaffoldPublishStep(sio *stepIO, ws *workspace.Workspace) error {
	if strings.TrimSpace(ws.Task.Remote) == "" {
		return &Degraded{Reason: "no remote yet, so there is nothing to push the scaffold to",
			Fix: "orion plan " + ws.ID + " --from remote"}
	}
	cfg := config.Load(ws.RepoDir())
	work := cfg.VCS.WorkBranch
	if work == "" {
		return nil
	}
	for _, br := range strandedBranches(ws.RepoDir(), cfg) {
		if err := pubPushFn(ws.RepoDir(), br); err != nil {
			return &Degraded{Reason: fmt.Sprintf("could not push %s: %v", br, err),
				Fix: "git -C " + ws.RepoDir() + " push -u origin " + br}
		}
		ui.Ok(sio.Out, "pushed", "%s", br)

		pr, err := pubPRStatusFn(ws.RepoDir(), br)
		if err == nil && pr.Verdict != collect.VerdictUnknown && pr.Verdict != collect.VerdictClosed {
			fmt.Fprintf(sio.Out, "          %s\n", ui.Dim(sio.Out, "a pull request for "+br+" is already open; left alone"))
			continue
		}
		url, err := pubOpenPRFn(ws.RepoDir(), br, prTitleFor(ws.RepoDir(), br), scaffoldPRBody(br, work), work)
		if err != nil {
			return &Degraded{Reason: fmt.Sprintf("pushed %s but could not open its pull request: %v", br, err),
				Fix: "gh pr create --base " + work + " --head " + br + " --fill"}
		}
		ui.Ok(sio.Out, "opened", "%s into %s: %s", br, work, url)
	}

	msg, err := pubSwitchFn(ws, work)
	if err != nil {
		return &Degraded{Reason: fmt.Sprintf("could not switch the sandbox back to %s: %v", work, err),
			Fix: "git -C " + ws.RepoDir() + " checkout " + work}
	}
	if strings.HasPrefix(msg, "the sandbox has") || strings.HasPrefix(msg, "could not switch") {
		return &Degraded{Reason: msg, Fix: "git -C " + ws.RepoDir() + " checkout " + work}
	}
	if msg != "" {
		ui.Ok(sio.Out, "switched", "%s", msg)
	}
	return nil
}

// scaffoldPublishDone is live, not recorded: done once no prefixed branch is
// stranded and the sandbox is on the work branch. A person who follows the
// old warning and checks out the work branch by hand has not published the
// scaffold, and this must still say so.
func scaffoldPublishDone(ws *workspace.Workspace) bool {
	if strings.TrimSpace(ws.Task.Remote) == "" {
		return false
	}
	cfg := config.Load(ws.RepoDir())
	if cfg.VCS.WorkBranch == "" {
		return true
	}
	if len(strandedBranches(ws.RepoDir(), cfg)) > 0 {
		return false
	}
	cur, err := currentBranch(ws.RepoDir())
	return err == nil && cur == cfg.VCS.WorkBranch
}

// strandedBranches are local branches under the configured prefix that carry
// commits the work branch lacks and have no upstream yet -- work that exists
// only in the sandbox.
func strandedBranches(repo string, cfg config.Config) []string {
	prefix := cfg.VCS.BranchPrefix
	if prefix == "" || cfg.VCS.WorkBranch == "" {
		return nil
	}
	out, err := gitIn(repo, "for-each-ref", "--format=%(refname:short)\t%(upstream:short)", "refs/heads/"+prefix)
	if err != nil {
		return nil
	}
	var stranded []string
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		name, upstream, _ := strings.Cut(line, "\t")
		name = strings.TrimSpace(name)
		if name == "" || strings.TrimSpace(upstream) != "" {
			continue
		}
		n, err := gitIn(repo, "rev-list", "--count", cfg.VCS.WorkBranch+".."+name)
		if err != nil {
			continue
		}
		if c, _ := strconv.Atoi(strings.TrimSpace(n)); c > 0 {
			stranded = append(stranded, name)
		}
	}
	return stranded
}

// prTitleFor uses the branch's newest commit subject: for the scaffold that is
// the stage's own summary of what it laid down.
func prTitleFor(repo, branch string) string {
	if s, err := gitIn(repo, "log", "-1", "--format=%s", branch); err == nil && strings.TrimSpace(s) != "" {
		return strings.TrimSpace(s)
	}
	return "Scaffold from " + branch
}

func scaffoldPRBody(branch, work string) string {
	return "Opened by Orion's planning chain. The scaffold stage works on `" + branch +
		"`, per the constitution's branch gate, and this pull request is how that work reaches `" +
		work + "`: by review, not a direct push.\n\nOrion opened this pull request and will not merge it."
}
