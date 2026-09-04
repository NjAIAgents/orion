package doctor

// Re-running ONE check, on demand.
//
// `orion doctor` answers "is this machine healthy" for a person, in fifteen
// lines. This answers a narrower question for a program: the batch stopped
// because of X, somebody says X is fixed -- is it? A reaction in Slack is a
// claim, and releasing a hold on a claim starts three runs that die the same
// way the first three did (OR-214).
//
// It runs the SAME check the doctor line runs, deliberately. Two
// implementations of "is the CLI signed in" drift, and the day they disagree
// is the day one of them is holding a queue shut for a reason the other says
// is not true.

import "github.com/orion-sdlc/orion/internal/config"

// Recheck re-runs the single check that speaks to one environmental fault.
//
// It returns the check's own grade label -- "OK", "WARN" or "FAIL" -- and its
// detail. An EMPTY grade means nothing here can answer the question: quota is
// the case that matters, because the only free way to ask whether a limit has
// reset is to spend a run finding out. The caller must not read that as
// health.
func Recheck(fault, root string) (label, detail string) {
	var c check
	switch fault {
	case "claude-auth":
		c = checkClaudeAuth()
	case "tracker":
		c = checkJira(trackerRequired(rootOr(root)))
	case "forge":
		c = checkGH()
	case "nj-agents":
		c = checkNJAgents(config.Load(rootOr(root)).Toolkit, false)
	default:
		return "", "there is no check for this that does not cost a run"
	}
	return c.grade.label(), c.detail
}
