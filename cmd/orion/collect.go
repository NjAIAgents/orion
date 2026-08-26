package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/orion-sdlc/orion/internal/collect"
	"github.com/orion-sdlc/orion/internal/slack"
	"github.com/orion-sdlc/orion/internal/ui"
	"github.com/orion-sdlc/orion/internal/workspace"
)

func runCollect(args []string) {
	var keys []string
	for _, a := range args {
		if !strings.HasPrefix(a, "-") {
			keys = append(keys, a)
		}
	}
	res := collect.Run(collect.Options{
		Keys:    keys,
		Out:     os.Stdout,
		DryRun:  hasFlag(args, "--dry-run"),
		NoPrune: hasFlag(args, "--no-prune"),
		NoFix:   hasFlag(args, "--no-fix"),
	}, collect.Deps{
		Jira:    mustJiraSearch(),
		Status:  prStatus,
		Refresh: workspace.Refresh,
		Prune:   pruneBranch,
		Merge:   mergePR,
		Fix:     fixRun,
		Slack:   slackForApproval(),
	})

	// Exit non-zero when anything failed, so this composes in a cron line or
	// a shell that checks. A pending check is not a failure.
	for _, r := range res {
		if r.Err != nil || r.Verdict == collect.VerdictFailing {
			os.Exit(1)
		}
	}
}

// prStatus asks gh for the pull request on a branch.
//
// One call, with the fields a decision needs. `gh pr checks` would give a
// richer per-check breakdown but exits non-zero when checks fail, which
// makes "the command failed" and "the checks failed" indistinguishable
// without parsing its output anyway.
func prStatus(dir, branch string) (collect.PR, error) {
	if _, err := exec.LookPath("gh"); err != nil {
		return collect.PR{}, fmt.Errorf("gh is not installed, so CI cannot be read")
	}
	cmd := exec.Command("gh", "pr", "view", branch,
		"--json", "url,state,mergedAt,statusCheckRollup")
	// The repository comes from the working directory. Getting this wrong is
	// what broke openPR on the first real run.
	cmd.Dir = dir

	out, err := cmd.CombinedOutput()
	if err != nil {
		text := strings.TrimSpace(string(out))
		// "no pull requests found" is an answer, not an error: the branch
		// may have been pushed without a PR, or the PR opened elsewhere.
		if strings.Contains(strings.ToLower(text), "no pull requests found") ||
			strings.Contains(strings.ToLower(text), "not found") {
			return collect.PR{Verdict: collect.VerdictUnknown, Detail: text}, nil
		}
		return collect.PR{}, fmt.Errorf("%v\n%s", err, text)
	}

	var v struct {
		URL      string `json:"url"`
		State    string `json:"state"`
		MergedAt string `json:"mergedAt"`
		Rollup   []struct {
			Name       string `json:"name"`
			Status     string `json:"status"`     // checks: QUEUED, IN_PROGRESS, COMPLETED
			Conclusion string `json:"conclusion"` // SUCCESS, FAILURE, CANCELLED, ...
			State      string `json:"state"`      // statuses: SUCCESS, FAILURE, PENDING
			DetailsURL string `json:"detailsUrl"`
			Context    string `json:"context"`
		} `json:"statusCheckRollup"`
	}
	if err := json.Unmarshal(out, &v); err != nil {
		return collect.PR{}, fmt.Errorf("could not read gh output: %w", err)
	}

	pr := collect.PR{URL: v.URL}
	switch {
	case v.MergedAt != "" || strings.EqualFold(v.State, "MERGED"):
		pr.Verdict = collect.VerdictMerged
		return pr, nil
	case strings.EqualFold(v.State, "CLOSED"):
		pr.Verdict = collect.VerdictClosed
		pr.Detail = "closed without merging"
		return pr, nil
	}

	// No checks configured at all is PASSING, not pending. A repository
	// without CI would otherwise leave every ticket in ci-wait forever,
	// waiting for a verdict that will never come.
	if len(v.Rollup) == 0 {
		pr.Verdict = collect.VerdictPassing
		pr.Detail = "no checks are configured on this repository"
		return pr, nil
	}

	var failed, running []string
	for _, c := range v.Rollup {
		name := c.Name
		if name == "" {
			name = c.Context
		}
		concl := strings.ToUpper(c.Conclusion)
		if concl == "" {
			concl = strings.ToUpper(c.State)
		}
		switch concl {
		case "SUCCESS", "NEUTRAL", "SKIPPED":
			// Done, and not a reason to stop.
		case "FAILURE", "TIMED_OUT", "STARTUP_FAILURE", "ACTION_REQUIRED", "ERROR":
			failed = append(failed, name+" ("+strings.ToLower(concl)+")")
		case "CANCELLED":
			// A cancelled check is not a pass. Treating it as one would
			// green-light a merge on evidence nobody produced.
			failed = append(failed, name+" (cancelled)")
		default:
			running = append(running, name)
		}
	}

	switch {
	case len(failed) > 0:
		pr.Verdict = collect.VerdictFailing
		pr.Detail = strings.Join(failed, "\n")
	case len(running) > 0:
		pr.Verdict = collect.VerdictPending
		pr.Detail = fmt.Sprintf("%d check(s) still running: %s",
			len(running), strings.Join(running, ", "))
	default:
		pr.Verdict = collect.VerdictPassing
		pr.Detail = fmt.Sprintf("%d check(s) passed", len(v.Rollup))
	}
	return pr, nil
}

// slackForApproval returns a client, or nil to disable the approval path.
//
// Nil rather than an error: a missing or unusable Slack token means Orion
// falls back to reporting that checks pass and waiting for a human to merge
// on GitHub. That is the safe direction. Failing the whole collector would
// also stop it closing tickets that had already merged.
func slackForApproval() collect.SlackAPI {
	c, err := slack.FromEnv()
	if err != nil {
		return nil
	}
	return c
}

// mergePR merges an approved pull request.
//
// --squash, and the reason goes in the commit body: the branch carries the
// agent's incremental commits and its decision records, which are worth
// reading on the branch and noise on the trunk. The one line that must
// survive into develop's history is who authorised it.
//
// NOT --admin. That flag bypasses branch protection, which would mean Orion
// merging past the very rules a repository set up to constrain it -- and it
// would do so on the authority of a Slack reaction.
func mergePR(dir, branch, reason string) error {
	if _, err := exec.LookPath("gh"); err != nil {
		return fmt.Errorf("gh is not installed, so the merge cannot be performed")
	}
	cmd := exec.Command("gh", "pr", "merge", branch,
		"--squash", "--delete-branch", "--body", reason)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%v\n%s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// pruneBranch removes one merged job worktree and its local branch.
//
// Shares the safety rule with `orion sandbox prune`: RemoveWorktree refuses
// while the checkout holds uncommitted work, and `git branch -d` refuses a
// branch that is not merged. Both refusals are load-bearing -- this runs
// unattended, where the cost of deleting something wanted is much higher
// than the cost of leaving a directory behind.
func pruneBranch(ws *workspace.Workspace, branch string) error {
	jobs, err := workspace.ListWorktrees(ws)
	if err != nil {
		return err
	}
	for _, j := range jobs {
		if j.Branch != branch {
			continue
		}
		if err := workspace.RemoveWorktree(ws, j.Path, false); err != nil {
			return err
		}
		if out, err := gitIn(ws.RepoDir(), "branch", "-d", branch); err != nil {
			// Not fatal: the worktree is gone, which was the point. A branch
			// git will not delete is one git thinks is unmerged, and that
			// disagreement is worth surfacing rather than forcing past.
			ui.Warn(os.Stdout, "worktree removed; branch %s kept: %s", branch, firstLineOf(out))
		}
		return nil
	}
	return fmt.Errorf("no worktree for %s", branch)
}

func firstLineOf(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
