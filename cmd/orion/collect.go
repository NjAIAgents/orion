package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/orion-sdlc/orion/internal/collect"
	"github.com/orion-sdlc/orion/internal/slack"
	"github.com/orion-sdlc/orion/internal/ui"
	"github.com/orion-sdlc/orion/internal/workspace"
)

func runCollect(args []string) {
	keys, err := ticketKeys("collect", "[KEY...]", positional(args, "--wait"))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(64)
	}
	res := collect.Run(collect.Options{
		Keys:          keys,
		Out:           os.Stdout,
		DryRun:        hasFlag(args, "--dry-run"),
		NoPrune:       hasFlag(args, "--no-prune"),
		NoFix:         hasFlag(args, "--no-fix"),
		AwaitApproval: waitFor(args),
	}, collect.Deps{
		Jira:    mustJiraSearch(),
		Status:  prStatus,
		Refresh: workspace.Refresh,
		Prune:   pruneBranch,
		Merge:   mergePR,
		OpenPR:  openPR,
		Fix:     fixRun,
		Judge:   doneJudge,
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

// defaultCollectWait is how long a hand-run collect waits for an approval.
//
// Long enough to cover stepping away from the desk, short enough that a
// forgotten terminal does not sit open overnight holding a request nobody
// remembers making.
const defaultCollectWait = 30 * time.Minute

// waitFor decides how long this pass should wait for an approval reaction.
//
// Waiting is the DEFAULT here and nowhere else. `orion collect` is run by a
// person, at a terminal, generally because a run or a watcher failed midway
// -- which is exactly the situation in which no other process will ever come
// back to read the answer. Asking someone to approve and then exiting leaves
// their approval unread and the pipeline stalled while every line of output
// says success. `orion watch` passes zero and keeps its old behaviour,
// because for a watcher the next tick IS the second pass.
//
//	(no flag)      wait 30m
//	--wait 5m      wait 5m
//	--wait 0       do not wait; ask and return
//	--no-wait      the same, spelled the way people reach for
func waitFor(args []string) time.Duration {
	if hasFlag(args, "--no-wait") || hasFlag(args, "--dry-run") {
		return 0
	}
	v := argFlag(args, "--wait", "")
	if v == "" {
		return defaultCollectWait
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		ui.Warn(os.Stdout, "--wait %q is not a duration (try 30m, 2h); waiting %s instead",
			v, defaultCollectWait)
		return defaultCollectWait
	}
	if d < 0 {
		return 0
	}
	return d
}

// mergedBranch answers the one question `orion work` asks before it claims a
// ticket: has this branch's pull request already merged?
//
// The same gh call as prStatus, reduced to a yes or no. Reusing it rather
// than asking a narrower question keeps ONE definition of merged-ness; two
// would eventually disagree, and the disagreement would surface as a ticket
// worked twice.
//
// "No pull request found" is an answer of no, not an error -- prStatus
// already treats it that way -- which is the overwhelmingly common case here:
// a ticket nobody has worked yet.
func mergedBranch(dir, branch string) (bool, string, error) {
	pr, err := prStatus(dir, branch)
	if err != nil {
		return false, "", err
	}
	return pr.Verdict == collect.VerdictMerged, pr.URL, nil
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
	// mergeable and headRefOid are asked for so a CONFLICTING branch can be
	// recognised as such. Without them a conflict looked like any other merge
	// failure, and the retry-on-failure path re-attempted an impossible merge
	// every tick forever.
	// The repository comes from the working directory. Getting this wrong is
	// what broke openPR on the first real run. Bounded by ghTimeout (OR-128):
	// a hung gh used to block orion watch's whole loop indefinitely.
	cmd, cancel := ghCommand(dir, "pr", "view", branch,
		"--json", "url,state,mergedAt,statusCheckRollup,mergeable,headRefOid,baseRefName")
	defer cancel()

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
		URL       string `json:"url"`
		State     string `json:"state"`
		MergedAt  string `json:"mergedAt"`
		Mergeable string `json:"mergeable"`  // MERGEABLE, CONFLICTING, UNKNOWN
		HeadOid   string `json:"headRefOid"` // the branch tip, to notice a rebase
		BaseRef   string `json:"baseRefName"`
		Rollup    []struct {
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

	pr := collect.PR{URL: v.URL, Head: v.HeadOid, BaseRef: v.BaseRef}
	// CONFLICTING only. UNKNOWN means GitHub has not finished computing
	// mergeability yet -- common for seconds after a push -- and treating
	// that as a conflict would announce a rebase nobody needs.
	pr.Conflicted = strings.EqualFold(v.Mergeable, "CONFLICTING")

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
		// Kept as data in the same pass that flattens it into Detail, so the
		// display costs no extra call and cannot disagree with the verdict.
		state := collect.CheckPassed
		switch concl {
		case "FAILURE", "TIMED_OUT", "STARTUP_FAILURE", "ACTION_REQUIRED", "ERROR", "CANCELLED":
			state = collect.CheckFailed
		case "SUCCESS", "NEUTRAL", "SKIPPED":
		default:
			state = collect.CheckRunning
		}
		pr.Checks = append(pr.Checks, collect.Check{Name: name, State: state})

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
	// A PASS IS GROUNDED IN THE RUNS, NOT THE ROLLUP (OR-327). Right after a
	// push the rollup holds only the checks GitHub has registered so far:
	// the fast Analyze jobs report SUCCESS inside a minute while the slow
	// Go jobs do not yet exist as checks, so a rollup of two successes and
	// nothing running read as "2 check(s) passed". A batch was declared
	// green on that, approved, and landed on develop two minutes before its
	// run had even been queued. The workflow runs for the head commit say
	// whether anything is still to come.
	if pr.Verdict == collect.VerdictPassing {
		if why := runsUnfinished(dir, v.HeadOid); why != "" {
			pr.Verdict = collect.VerdictPending
			pr.Detail = why
		}
	}
	return pr, nil
}

// runsUnfinished reports why a passing rollup cannot yet be trusted: no
// workflow run exists for the commit, or one is still queued or running.
// Empty when every run for the commit has completed.
func runsUnfinished(dir, head string) string {
	if head == "" {
		return ""
	}
	cmd, cancel := ghCommand(dir, "run", "list", "--commit", head, "--limit", "20",
		"--json", "status,conclusion,name")
	defer cancel()
	out, err := cmd.Output()
	if err != nil {
		// Unreadable is not "finished": the rollup alone cannot prove a pass.
		return "the workflow runs for " + shortSHA(head) + " could not be read; not treating the rollup as complete"
	}
	var runs []struct {
		Status     string `json:"status"`
		Conclusion string `json:"conclusion"`
		Name       string `json:"name"`
	}
	if err := json.Unmarshal(out, &runs); err != nil {
		return "the workflow runs for " + shortSHA(head) + " could not be read; not treating the rollup as complete"
	}
	return unfinishedRuns(runs)
}

// unfinishedRuns is the decision, separated from gh so it can be tested.
func unfinishedRuns(runs []struct {
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
	Name       string `json:"name"`
}) string {
	if len(runs) == 0 {
		return "no workflow run has been recorded for this commit yet"
	}
	var open []string
	for _, r := range runs {
		if !strings.EqualFold(r.Status, "completed") {
			open = append(open, r.Name+" ("+strings.ToLower(r.Status)+")")
		}
	}
	if len(open) > 0 {
		return fmt.Sprintf("%d workflow run(s) not finished: %s", len(open), strings.Join(open, ", "))
	}
	return ""
}

func shortSHA(s string) string {
	if len(s) > 7 {
		return s[:7]
	}
	return s
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

// mergePR merges an approved pull request, using the configured strategy.
//
// The strategy is not cosmetic: it decides whether the agent's authorship
// survives onto the trunk. A --squash merge collapses the branch into one
// new commit authored by the PULL REQUEST's author -- whoever's token opened
// it -- so every orionbot commit disappears from develop's history, which is
// exactly what the first real merge produced. --rebase replays each commit
// with its author intact. See config.VCS.MergeStrategy.
//
// NOT --admin. That flag bypasses branch protection, which would mean Orion
// merging past the very rules a repository set up to constrain it -- and it
// would do so on the authority of a Slack reaction.
func mergePR(dir, branch, reason, strategy string) error {
	if _, err := exec.LookPath("gh"); err != nil {
		return fmt.Errorf("gh is not installed, so the merge cannot be performed")
	}
	flag := "--rebase"
	switch strings.ToLower(strings.TrimSpace(strategy)) {
	case "squash":
		flag = "--squash"
	case "merge":
		flag = "--merge"
	case "", "rebase":
		// The default. See config.VCS.MergeStrategy for why.
	default:
		return fmt.Errorf("unknown merge strategy %q: use rebase, squash or merge", strategy)
	}
	// NOT --delete-branch. gh deletes the local branch too, and that branch
	// is checked out in the job worktree -- git refuses, gh exits 1, and the
	// merge is reported as FAILED after it has already succeeded on GitHub.
	// The ticket then stays open over work that has landed, which is the
	// worst way to be wrong: the repository and the tracker disagree, and the
	// tracker is the one people believe.
	//
	// Orion removes the worktree first and the branch second, in that order,
	// during the prune that follows a confirmed merge.
	args := []string{"pr", "merge", branch, flag}
	// --body only means anything to a strategy that writes a new commit
	// message. A rebase replays the branch's own commits, so gh rejects it.
	if flag != "--rebase" {
		args = append(args, "--body", reason)
	}
	// Bounded by ghTimeout (OR-128): a hung merge call must not block the
	// watcher forever with no line on the console to say why.
	cmd, cancel := ghCommand(dir, args...)
	defer cancel()
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%v\n%s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// pruneBranch removes one merged job worktree, its local branch, and the
// remote branch.
//
// CONTRACT: only called once the pull request reports MERGED. collect.go
// reaches Prune from the VerdictMerged path and nowhere else, so merged-ness
// is already established, by the forge, before this runs.
//
// That contract is why the local delete is `-D` and not `-d`. `-d` decides
// merged-ness by ancestry: is the branch tip reachable from HEAD. Orion
// merges by REBASE, which replays the commits onto the base as new objects
// with new SHAs, so the originals are never reachable and `-d` refuses --
// every time, for every ticket, no matter how cleanly it landed:
//
//	$ git merge-base --is-ancestor origin/orion/or-38 origin/main
//	(exit 1, despite PR #10 being MERGED)
//
// The old code caught that refusal and downgraded it to a warning, reasoning
// that git disagreeing about merged-ness was worth surfacing. Sound for a
// merge-commit workflow; wrong for ours, where the disagreement is the
// guaranteed and meaningless consequence of the merge strategy we chose. It
// fired on every success, meant nothing, and left the branch behind forever.
// A warning guaranteed on the happy path is one people stop reading.
//
// The same reasoning is why this calls RemoveMergedWorktree rather than
// RemoveWorktree: that function's unpushed-commits guard is ancestry too, and
// a squash merge plus delete_branch_on_merge makes every merged branch look
// like it carries commits no remote has (OR-122).
//
// The uncommitted-work refusal is a different matter and stays: it declines
// while the checkout holds work the pull request cannot know about. That one
// is load-bearing -- this runs unattended, where deleting something wanted
// costs far more than leaving a directory behind.
func pruneBranch(ws *workspace.Workspace, branch string) error {
	jobs, err := workspace.ListWorktrees(ws)
	if err != nil {
		return err
	}
	for _, j := range jobs {
		if j.Branch != branch {
			continue
		}
		if err := workspace.RemoveMergedWorktree(ws, j.Path); err != nil {
			return err
		}
		if out, err := deleteLocalBranch(ws.RepoDir(), branch); err != nil {
			// Still not fatal -- the worktree is gone, which was the point.
			// With -D the remaining causes are real ones worth reporting:
			// the branch is checked out somewhere else, or does not exist.
			ui.Warn(os.Stdout, "worktree removed; local branch %s kept: %s", branch, firstLineOf(out))
		}
		deleteRemoteBranch(ws.RepoDir(), branch)
		return nil
	}
	return fmt.Errorf("no worktree for %s", branch)
}

// deleteLocalBranch removes a branch the pull request has confirmed merged.
//
// Its own function so a test can hold it to that contract. The distinction
// between -d and -D is invisible in a diff and consequential in effect, and
// the property worth pinning is behavioural: given a branch merged the way
// Orion merges, this must delete it. Asserting git's ancestry rules instead
// would pass whichever flag were here, which is how the bug lasted.
func deleteLocalBranch(dir, branch string) (string, error) {
	return gitIn(dir, "branch", "-D", branch)
}

// deleteRemoteBranch removes the pushed branch, best effort.
//
// Already-gone is the EXPECTED case, not an error: `orion init` sets
// delete_branch_on_merge on the repository, so GitHub usually deletes the
// head branch at merge time and this arrives to find nothing left. Treating
// that as a failure would put a warning on the path we deliberately made
// most common.
//
// Never fatal. The branch is merged; a leftover remote ref is untidy, not
// dangerous, and is not worth failing a collect that otherwise succeeded.
func deleteRemoteBranch(dir, branch string) {
	out, err := gitIn(dir, "push", "origin", "--delete", branch)
	if err == nil {
		ui.Ok(os.Stdout, "removed", "the remote branch %s", branch)
		return
	}
	if isAlreadyGone(out) {
		return
	}
	ui.Warn(os.Stdout, "remote branch %s kept: %s", branch, firstLineOf(out))
}

// isAlreadyGone recognises git's several ways of saying "no such branch".
//
// Matched on text because git offers no distinguishing exit code here: a
// missing ref and a rejected push both exit 1.
func isAlreadyGone(out string) bool {
	s := strings.ToLower(out)
	for _, phrase := range []string{
		"remote ref does not exist",
		"unable to delete",
		"does not appear to be a git repository", // no remote configured
	} {
		if strings.Contains(s, phrase) {
			return true
		}
	}
	return false
}

func firstLineOf(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
