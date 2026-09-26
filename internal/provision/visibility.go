package provision

import (
	"fmt"
	"os/exec"
	"strings"
)

// paidPlanReason is how applyProtection names GitHub's refusal to protect a
// private repository on a free plan. NeedsPaidPlan keys on it, so the offer to
// go public appears for that cause and no other (a missing permission is not
// fixed by changing visibility).
const paidPlanReason = "branch protection needs a paid plan for private repositories"

// NeedsPaidPlan reports whether protection failed only because the repository
// is private on a plan that cannot protect it -- the one failure making the
// repository public would fix (OR-485).
func NeedsPaidPlan(res *Result) bool {
	if res == nil || len(res.Protection) == 0 {
		return false
	}
	paidPlan := false
	for _, v := range res.Protection {
		switch {
		case v == "applied":
		case strings.Contains(v, paidPlanReason):
			paidPlan = true
		default:
			return false // another cause, which going public would not fix
		}
	}
	// At least one refusal must be the paid-plan one: a repository whose
	// protection all applied has nothing to trade for.
	return paidPlan
}

// MakePublic changes the repository's visibility to public and re-applies
// branch protection, replacing res's protection results and warnings with the
// new attempt's.
//
// Only ever called after the operator said yes to a question naming what this
// exposes. It is not reversible in effect: once public, the history can have
// been cloned, and switching back later does not un-share it.
func MakePublic(opts Options, res *Result) error {
	target := opts.Name
	if opts.Org != "" {
		target = opts.Org + "/" + opts.Name
	}
	if full := ownerRepo(opts.Dir, target); full != "" {
		target = full
	}
	out, err := exec.Command("gh", "repo", "edit", target,
		"--visibility", "public", "--accept-visibility-change-consequences").CombinedOutput()
	if err != nil {
		return fmt.Errorf("making %s public: %s", target, strings.TrimSpace(string(out)))
	}
	res.Protection = map[string]string{}
	res.Warnings = nil
	applyProtection(target, opts, res)
	return nil
}
