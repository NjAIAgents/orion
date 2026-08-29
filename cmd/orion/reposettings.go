package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"

	"github.com/orion-sdlc/orion/internal/config"
	"github.com/orion-sdlc/orion/internal/provision"
	"github.com/orion-sdlc/orion/internal/ui"
)

// This file holds the two things Orion asks GitHub to enforce on its behalf:
// automatic head-branch deletion (set at adoption) and branch protection
// (applied by `orion protect`, deliberately separate -- see runProtect).

// ensureRepoSettings turns on GitHub's own cleanup of merged head branches.
//
// Orion deletes the branch itself after a successful collect, so this is
// belt and braces. It matters because the cases where collect does NOT run
// are exactly the messy ones: a watcher killed mid-run, a merge done by hand
// in the web UI, a network failure between merge and prune. Server-side
// deletion is the only cleanup that survives all of those, because it
// happens at merge time inside the same transaction as the merge.
//
// Detected, never required (A5). No gh, no remote, or no permission means a
// note and a normal exit -- this is tidiness, and nothing depends on it.
func ensureRepoSettings(dir string) {
	if _, err := exec.LookPath("gh"); err != nil {
		return
	}
	slug, err := repoSlug(dir)
	if err != nil {
		// No remote yet is ordinary during a local-first adoption.
		return
	}

	if on, err := deleteOnMergeEnabled(dir, slug); err == nil && on {
		ui.Ok(os.Stdout, "bound", "%s already deletes merged branches", slug)
		return
	}

	cmd := exec.Command("gh", "api", "--method", "PATCH",
		"repos/"+slug, "-F", "delete_branch_on_merge=true")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		ui.Warn(os.Stdout, "could not set delete_branch_on_merge on %s: %s\n"+
			"  Merged branches will accumulate on the remote. Needs admin on the repository.",
			slug, firstLineOf(strings.TrimSpace(string(out))))
		return
	}
	ui.Ok(os.Stdout, "set", "%s now deletes head branches when a PR merges", slug)
}

func deleteOnMergeEnabled(dir, slug string) (bool, error) {
	out, err := ghJSON(dir, "api", "repos/"+slug)
	if err != nil {
		return false, err
	}
	var v struct {
		DeleteBranchOnMerge bool `json:"delete_branch_on_merge"`
	}
	if err := json.Unmarshal(out, &v); err != nil {
		return false, err
	}
	return v.DeleteBranchOnMerge, nil
}

// runProtect applies branch protection using the checks the repository is
// actually observed to run.
//
// SEPARATE FROM INIT ON PURPOSE. Protection is only worth having if it names
// the checks that must pass, and a required check that never reports blocks
// every pull request forever -- there is no timeout, the PR simply cannot
// merge. At adoption time no CI has ever run, so any context Orion named
// there would be a guess, and a wrong guess is unrecoverable without an
// admin visiting the settings UI. Provision therefore leaves
// required_status_checks nil, and this command applies the real ruleset once
// there is evidence of what the checks are called.
//
// The rule that carries the weight is strict: "require branches to be up to
// date before merging". That is the server-side form of the staleness gate
// in internal/collect/staleness.go. Both are worth having and they are not
// redundant: the gate needs no admin rights and works on any repository, but
// it can only warn a human, who can skim the warning and merge anyway. This
// refuses the merge.
//
// Whether strict is worth its cost is the OPERATOR's call, not a fact this
// command gets to assume: the benefit (catching a genuine semantic conflict)
// is fixed, but the cost scales with queue depth, since strict makes every
// merge invalidate every other open pull request. So this reads
// cfg.VCS.RequireUpToDate rather than hardcoding true, and states which
// value it is applying and where that value came from -- see
// config.VCSRequireUpToDateSource -- so the setting is visible here rather
// than discovered later from a merge refusal.
func runProtect(args []string) {
	dir := argFlag(args, "--dir", ".")
	dryRun := hasFlag(args, "--dry-run")

	if _, err := exec.LookPath("gh"); err != nil {
		ui.Warn(os.Stdout, "gh is not installed, so branch protection cannot be applied")
		os.Exit(1)
	}
	slug, err := repoSlug(dir)
	exitOn(err)

	cfg := config.Load(dir)
	branches := uniqueNonEmpty(cfg.VCS.DefaultBranch, cfg.VCS.WorkBranch)
	if b := argFlag(args, "--branch", ""); b != "" {
		branches = []string{b}
	}

	reviews, why := provision.RequiredReviews(dir, slug)
	ui.Ok(os.Stdout, "reviews", "requiring %d approving review(s): %s", reviews, why)

	strict := cfg.VCS.RequireUpToDate
	ui.Ok(os.Stdout, "strict", "requiring branches to be up to date before merge: %v (%s)",
		strict, cfg.VCSRequireUpToDateSource())

	for _, b := range branches {
		checks := observedChecks(dir, slug, b)
		if len(checks) == 0 {
			ui.Warn(os.Stdout, "%s: no checks have ever reported, so none will be required.\n"+
				"  Push a commit, let CI run once, then re-run `orion protect`.", b)
		} else {
			ui.Ok(os.Stdout, "checks", "%s will require: %s", b, strings.Join(checks, ", "))
		}

		if dryRun {
			reportStrictDryRun(dir, slug, b, strict, cfg.VCSRequireUpToDateSource())
			continue
		}
		if err := applyProtection(dir, slug, b, checks, reviews, strict); err != nil {
			ui.Warn(os.Stdout, "%s is NOT protected on the server: %v\n"+
				"  Orion's gate hook still refuses pushes to it, but that constrains the agent only, not a human.",
				b, err)
			continue
		}
		if strict {
			ui.Ok(os.Stdout, "protected", "%s (up to date required, force-push and deletion refused)", b)
		} else {
			ui.Ok(os.Stdout, "protected", "%s (up to date NOT required -- vcs.require_up_to_date=false, force-push and deletion refused)", b)
		}
	}
}

// reportStrictDryRun compares the strict value this run would apply against
// what the branch actually has on GitHub right now, so a dry-run cannot miss
// that a re-run would silently revert an operator's hand-edit -- exactly
// what happened to develop on 2026-08-29 (see the ticket this shipped
// under, OR-179).
func reportStrictDryRun(dir, slug, branch string, strict bool, source string) {
	haveCurrent, current := currentRequireUpToDate(dir, slug, branch)
	switch {
	case !haveCurrent:
		ui.Ok(os.Stdout, "dry-run", "%s: would set strict=%v (%s); branch has no existing required-status-checks block to compare", branch, strict, source)
	case current == strict:
		ui.Ok(os.Stdout, "dry-run", "%s: strict=%v already matches what's on GitHub (%s); no change", branch, strict, source)
	default:
		ui.Warn(os.Stdout, "dry-run %s: GitHub currently has strict=%v; this run would change it to %v (%s).\n"+
			"  Running `orion protect` for real would revert that setting.", branch, current, strict, source)
	}
}

// currentRequireUpToDate reads the strict value actually on the branch's
// protection right now. haveCurrent is false when there is no protection yet
// or no required_status_checks block to compare against -- distinct from a
// present block whose strict happens to be false.
func currentRequireUpToDate(dir, slug, branch string) (haveCurrent bool, strict bool) {
	out, err := ghJSON(dir, "api", fmt.Sprintf("repos/%s/branches/%s/protection", slug, branch))
	if err != nil {
		return false, false
	}
	var v struct {
		RequiredStatusChecks *struct {
			Strict bool `json:"strict"`
		} `json:"required_status_checks"`
	}
	if json.Unmarshal(out, &v) != nil || v.RequiredStatusChecks == nil {
		return false, false
	}
	return true, v.RequiredStatusChecks.Strict
}

// applyProtection writes the ruleset for one branch.
//
// enforce_admins stays false so a solo maintainer can still merge their own
// work. That does mean an admin can override the gate, which makes this a
// guard rail rather than a wall -- the honest description of any protection
// a repository owner can edit.
//
// strict is cfg.VCS.RequireUpToDate, not a hardcoded true: see the doc
// comment on runProtect for why this is the operator's call.
func applyProtection(dir, slug, branch string, checks []string, reviews int, strict bool) error {
	rules := map[string]any{
		"required_status_checks": map[string]any{
			"strict":   strict,
			"contexts": checks,
		},
		"enforce_admins":     false,
		"restrictions":       nil,
		"allow_force_pushes": false,
		"allow_deletions":    false,
	}
	// A review requirement of zero is expressed by omitting the block
	// entirely. Sending required_approving_review_count: 0 asks GitHub to
	// require pull requests with no approvals, which is a different and
	// stricter thing than not requiring review at all.
	if reviews > 0 {
		rules["required_pull_request_reviews"] = map[string]any{
			"required_approving_review_count": reviews,
			"dismiss_stale_reviews":           true,
		}
	} else {
		rules["required_pull_request_reviews"] = nil
	}

	body, err := json.Marshal(rules)
	if err != nil {
		return err
	}
	cmd := exec.Command("gh", "api", "--method", "PUT",
		fmt.Sprintf("repos/%s/branches/%s/protection", slug, branch), "--input", "-")
	cmd.Dir = dir
	cmd.Stdin = strings.NewReader(string(body))
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%s", explainProtectionFailure(strings.TrimSpace(string(out))))
	}
	return nil
}

// explainProtectionFailure translates GitHub's two common refusals.
//
// The upgrade message is worth rewriting because its advice is incomplete:
// it says to upgrade, and does not mention that making the repository public
// works too and costs nothing. That was a real dead end here until somebody
// noticed the second option.
func explainProtectionFailure(out string) string {
	low := strings.ToLower(out)
	switch {
	case strings.Contains(low, "upgrade"):
		return "branch protection needs a paid plan for PRIVATE repositories; " +
			"making the repository public also enables it, at no cost"
	case strings.Contains(low, "not found"), strings.Contains(low, "403"):
		return "no admin permission on this repository (" + firstLineOf(out) + ")"
	}
	return firstLineOf(out)
}

// observedChecks returns the check names that have actually reported on a
// branch, which is the only trustworthy source for what to require.
//
// Reads both modern check runs and legacy commit statuses, because a
// repository can use either and requiring the wrong kind names a context
// that never arrives.
func observedChecks(dir, slug, branch string) []string {
	seen := map[string]bool{}

	if out, err := ghJSON(dir, "api", fmt.Sprintf("repos/%s/commits/%s/check-runs", slug, branch)); err == nil {
		var v struct {
			CheckRuns []struct {
				Name string `json:"name"`
			} `json:"check_runs"`
		}
		if json.Unmarshal(out, &v) == nil {
			for _, c := range v.CheckRuns {
				if c.Name != "" {
					seen[c.Name] = true
				}
			}
		}
	}

	if out, err := ghJSON(dir, "api", fmt.Sprintf("repos/%s/commits/%s/status", slug, branch)); err == nil {
		var v struct {
			Statuses []struct {
				Context string `json:"context"`
			} `json:"statuses"`
		}
		if json.Unmarshal(out, &v) == nil {
			for _, s := range v.Statuses {
				if s.Context != "" {
					seen[s.Context] = true
				}
			}
		}
	}

	names := make([]string, 0, len(seen))
	for n := range seen {
		names = append(names, n)
	}
	// Sorted so re-running produces an identical ruleset rather than a
	// reordered one, which would otherwise show as a change in an audit log.
	sort.Strings(names)
	return names
}

func repoSlug(dir string) (string, error) {
	out, err := ghJSON(dir, "repo", "view", "--json", "nameWithOwner")
	if err != nil {
		return "", fmt.Errorf("could not identify the GitHub repository for %s: %w", dir, err)
	}
	var v struct {
		NameWithOwner string `json:"nameWithOwner"`
	}
	if err := json.Unmarshal(out, &v); err != nil || v.NameWithOwner == "" {
		return "", fmt.Errorf("could not identify the GitHub repository for %s", dir)
	}
	return v.NameWithOwner, nil
}

func ghJSON(dir string, args ...string) ([]byte, error) {
	cmd := exec.Command("gh", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	return out, nil
}

func uniqueNonEmpty(in ...string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}
