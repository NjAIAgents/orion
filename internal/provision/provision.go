// Package provision creates the remote repository and its branch model.
//
// Branch model: two long-lived branches. `main` is the release branch and is
// protected. `develop` is the integration branch, the repository default,
// and the base for every pull request. Feature branches are cut from develop
// and merge back into it.
//
// Both are push-protected, and that is not belt-and-braces. Protecting only
// main would leave the PR into develop optional, and a review gate you can
// skip is not a gate.
//
// Everything here is idempotent: re-running against an already-provisioned
// workspace reports what exists rather than failing or duplicating.
package provision

import (
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"strings"
)

// Result records what provisioning actually achieved, including the parts
// that did not work. A partial success reported as success is how people end
// up believing a branch is protected when it is not.
type Result struct {
	RemoteURL       string
	CreatedRemote   bool
	BranchesCreated []string
	DefaultBranch   string
	Protection      map[string]string // branch -> "applied" | reason it was not
	Warnings        []string
}

type Options struct {
	Dir           string
	Name          string
	Description   string
	DefaultBranch string // main
	WorkBranch    string // develop
	Private       bool
	Org           string
	// Confirm is called before anything is created remotely. Returning false
	// aborts. Never bypassed by auto-merge: creating a repository in someone's
	// account is not sandboxed work.
	Confirm func(prompt string) bool
	Out     io.Writer
}

// InitBranches creates the two long-lived branches locally. Safe to call on
// an existing repo: it creates only what is missing.
//
// Order matters. main is created first and develop branched from it, so the two
// share history. Creating them independently produces unrelated histories
// and the first develop-to-main pull request is unmergeable.
func InitBranches(dir, mainBranch, workBranch string) ([]string, error) {
	var created []string

	if out, err := git(dir, "rev-parse", "--git-dir"); err != nil {
		return nil, fmt.Errorf("%s is not a git repository: %s", dir, out)
	}

	// An empty repository has no commit, and a branch cannot exist without
	// one. Make the root commit before branching.
	if _, err := git(dir, "rev-parse", "HEAD"); err != nil {
		if _, err := git(dir, "checkout", "-B", mainBranch); err != nil {
			return nil, err
		}
		if out, err := git(dir, "commit", "--allow-empty", "-m",
			"chore: initialise repository\n\nRoot commit created by Orion so the branch model can be established."); err != nil {
			return nil, fmt.Errorf("root commit: %s", out)
		}
		created = append(created, mainBranch)
	} else if !branchExists(dir, mainBranch) {
		if out, err := git(dir, "branch", mainBranch); err != nil {
			return nil, fmt.Errorf("creating %s: %s", mainBranch, out)
		}
		created = append(created, mainBranch)
	}

	if !branchExists(dir, workBranch) {
		if out, err := git(dir, "branch", workBranch, mainBranch); err != nil {
			return nil, fmt.Errorf("creating %s from %s: %s", workBranch, mainBranch, out)
		}
		created = append(created, workBranch)
	}

	// Land on develop: it is where work happens, and leaving the tree on
	// main invites a first commit onto the protected branch.
	if out, err := git(dir, "checkout", workBranch); err != nil {
		return created, fmt.Errorf("switching to %s: %s", workBranch, out)
	}
	return created, nil
}

// Remote creates the GitHub repository, pushes both branches, sets develop
// as the repository default, and applies protection to both.
func Remote(opts Options) (*Result, error) {
	res := &Result{
		DefaultBranch: opts.WorkBranch,
		Protection:    map[string]string{},
	}

	if _, err := exec.LookPath("gh"); err != nil {
		return nil, fmt.Errorf("gh CLI not found. Orion creates the remote through gh.\n" +
			"  Install it and run `gh auth login`, then re-run. Check with: orion doctor")
	}
	if out, err := exec.Command("gh", "auth", "status").CombinedOutput(); err != nil {
		return nil, fmt.Errorf("gh is not authenticated:\n%s\n  Run: gh auth login", strings.TrimSpace(string(out)))
	}

	// Already provisioned? Report and stop rather than creating a second.
	if url := existingRemote(opts.Dir); url != "" {
		res.RemoteURL = url
		res.Warnings = append(res.Warnings,
			"origin already exists ("+url+"); skipped creation")
		return res, nil
	}

	target := opts.Name
	if opts.Org != "" {
		target = opts.Org + "/" + opts.Name
	}
	visibility := "--private"
	if !opts.Private {
		// Reachable only by an explicit config change. Creating a public
		// repository from an unattended agent run is not something to do by
		// default or by accident.
		visibility = "--public"
	}

	prompt := fmt.Sprintf("Create %s repository %q on GitHub and push %s and %s?",
		strings.TrimPrefix(visibility, "--"), target, opts.DefaultBranch, opts.WorkBranch)
	if opts.Confirm != nil && !opts.Confirm(prompt) {
		return nil, fmt.Errorf("cancelled: no remote created")
	}

	args := []string{"repo", "create", target, visibility, "--source", opts.Dir, "--remote", "origin"}
	if opts.Description != "" {
		args = append(args, "--description", opts.Description)
	}
	if out, err := exec.Command("gh", args...).CombinedOutput(); err != nil {
		return nil, fmt.Errorf("gh repo create failed: %s", strings.TrimSpace(string(out)))
	}
	res.CreatedRemote = true
	res.RemoteURL = existingRemote(opts.Dir)

	for _, b := range []string{opts.DefaultBranch, opts.WorkBranch} {
		if out, err := git(opts.Dir, "push", "-u", "origin", b); err != nil {
			return res, fmt.Errorf("pushing %s: %s", b, out)
		}
	}

	// develop becomes the repository default so pull requests target it
	// without anyone having to remember to change the base.
	if out, err := exec.Command("gh", "repo", "edit", target,
		"--default-branch", opts.WorkBranch).CombinedOutput(); err != nil {
		res.Warnings = append(res.Warnings,
			"could not set "+opts.WorkBranch+" as the default branch: "+strings.TrimSpace(string(out)))
	}

	applyProtection(target, opts, res)
	return res, nil
}

// applyProtection sets branch protection, recording per branch whether it
// actually took.
//
// This fails on free-plan private repositories, where branch protection is a
// paid feature. That failure is reported loudly rather than swallowed,
// because the whole review-gate story rests on it. Orion's own gate hook
// still refuses pushes to protected branches, but that only constrains the
// agent, not a human with a terminal. Server-side protection is what
// constrains everyone.
func applyProtection(target string, opts Options, res *Result) {
	reviews, why := RequiredReviews("", target)
	res.Warnings = append(res.Warnings, fmt.Sprintf(
		"requiring %d approving review(s) on protected branches: %s", reviews, why))

	rules := map[string]any{
		"required_status_checks": nil,
		"enforce_admins":         false,
		"restrictions":           nil,
		"allow_force_pushes":     false,
		"allow_deletions":        false,
	}
	// Zero is expressed by omitting the block. Sending a count of 0 asks for
	// pull requests that need no approvals, which still forces every change
	// through a PR -- a different and stricter rule than not requiring
	// review, and not the one meant here.
	if reviews > 0 {
		rules["required_pull_request_reviews"] = map[string]any{
			"required_approving_review_count": reviews,
			"dismiss_stale_reviews":           true,
		}
	} else {
		rules["required_pull_request_reviews"] = nil
	}
	body, _ := json.Marshal(rules)

	for _, b := range []string{opts.DefaultBranch, opts.WorkBranch} {
		cmd := exec.Command("gh", "api", "--method", "PUT",
			fmt.Sprintf("repos/%s/branches/%s/protection", target, b),
			"--input", "-")
		cmd.Stdin = strings.NewReader(string(body))
		out, err := cmd.CombinedOutput()
		if err != nil {
			reason := strings.TrimSpace(string(out))
			if strings.Contains(reason, "Upgrade") || strings.Contains(reason, "upgrade") {
				reason = "branch protection needs a paid plan for private repositories"
			}
			res.Protection[b] = "NOT APPLIED: " + truncate(reason, 160)
			res.Warnings = append(res.Warnings, fmt.Sprintf(
				"%s is NOT protected on the server (%s). Orion's gate hook still refuses "+
					"pushes to it, but that constrains the agent only, not a human.", b, truncate(reason, 90)))
			continue
		}
		res.Protection[b] = "applied"
	}
}

// RequiredReviews decides how many approving reviews to demand, and says why.
//
// A hardcoded 1 is wrong for a solo repository: GitHub will not let anyone
// approve their own pull request, so a lone maintainer can never satisfy the
// rule and finds out only at the moment they try to merge their first change
// -- by which point the branch is protected and the fix is buried in the
// repository settings UI. A hardcoded 0 is wrong for a team, where the review
// gate is the entire point. So it is derived from who can actually push.
//
// The reason travels with the number and is always reported. A value that
// silently differs between two repositories, with nothing on screen saying
// why, is worse than picking either default and stating it. It also changes
// on its own when a collaborator is added, and the next person to notice
// should not have to read this comment to find out what happened.
//
// dir may be empty, in which case gh resolves the repository from the slug
// alone rather than the working directory.
func RequiredReviews(dir, slug string) (int, string) {
	cmd := exec.Command("gh", "api", "repos/"+slug+"/collaborators", "--paginate")
	if dir != "" {
		cmd.Dir = dir
	}
	out, err := cmd.Output()
	if err != nil {
		// Not fatal, and deliberately biased towards the permissive answer:
		// guessing high here bricks merging for a solo user, while guessing
		// low costs a team one setting they can raise.
		return 0, "could not read collaborators, so assuming solo (no review required)"
	}
	var people []struct {
		Login string `json:"login"`
	}
	if err := json.Unmarshal(out, &people); err != nil || len(people) <= 1 {
		return 0, "solo repository, and GitHub does not let you approve your own pull request"
	}
	return 1, fmt.Sprintf("%d collaborators can push", len(people))
}

func branchExists(dir, name string) bool {
	_, err := git(dir, "show-ref", "--verify", "--quiet", "refs/heads/"+name)
	return err == nil
}

func existingRemote(dir string) string {
	out, err := git(dir, "remote", "get-url", "origin")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

func git(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(cmd.Environ(),
		"GIT_TERMINAL_PROMPT=0", // never block an unattended run on a credential prompt
		"GIT_COMMITTER_NAME=orion", "GIT_COMMITTER_EMAIL=orion@localhost",
	)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// Summary renders the result for a human, leading with anything that did not
// work rather than burying it under what did.
func (r *Result) Summary() string {
	var b strings.Builder
	if r.RemoteURL != "" {
		b.WriteString("remote     " + r.RemoteURL + "\n")
	}
	if len(r.BranchesCreated) > 0 {
		b.WriteString("branches   " + strings.Join(r.BranchesCreated, ", ") + "\n")
	}
	b.WriteString("default    " + r.DefaultBranch + " (pull requests target this)\n")
	for _, br := range sortedKeys(r.Protection) {
		b.WriteString(fmt.Sprintf("protect    %-6s %s\n", br, r.Protection[br]))
	}
	for _, w := range r.Warnings {
		b.WriteString("WARNING    " + w + "\n")
	}
	return b.String()
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	// Two entries at most; a stable order beats importing sort.
	for i := 0; i < len(out); i++ {
		for j := i + 1; j < len(out); j++ {
			if out[j] < out[i] {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out
}

func truncate(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}
