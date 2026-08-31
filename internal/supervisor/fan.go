package supervisor

import (
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/orion-sdlc/orion/internal/config"
	"github.com/orion-sdlc/orion/internal/workspace"
)

// WriteOnlyTools is what a fan-out child is given: it may read the tree and
// edit files, and it has no shell.
//
// This is the enforcement behind "only the parent verifies" (OR-230), and it
// is a tool list rather than a rule in a prompt on purpose. A pattern match
// on the command would have to guess at every spelling of running a suite --
// `go test`, `./scripts/test.sh`, `make check`, `npm test`, a script that
// wraps one of those -- and would be leaky by construction. A child with no
// Bash cannot run any of them, including the one nobody thought of.
//
// The cost this avoids is not hypothetical: N children each running a suite
// against a tree their peers are still writing produces N runs of failures
// that are not theirs, and the test suite was 21% of all Bash calls in a run
// before any fan-out multiplied it.
//
// No Task either. A child that can spawn its own children escapes the width
// the validator just admitted.
var WriteOnlyTools = []string{"Read", "Glob", "Grep", "Edit", "Write", "MultiEdit"}

// ShellTools names what a fan-out child is denied outright, alongside the
// allowlist above. See Options.DeniedTools for why both.
var ShellTools = []string{"Bash", "BashOutput", "KillShell", "Task"}

// FanResult pairs one child's Result with whatever error Run returned for
// it. Indexed the same as the Options slice given to Fan, so a caller
// matching a result back to what produced it never has to search.
type FanResult struct {
	Result *Result
	Err    error
}

// Fan runs N Options concurrently and returns N FanResults in the same
// order as jobs -- the one place the cap, the accounting and the failure
// policy live, so they cannot drift between call sites the way they would
// if each caller fanned out by hand.
//
// Concurrency is capped by cfg.Limits.MaxConcurrentChildren (configurable
// per project, low by default): unbounded fan-out against a rate-limited
// API converts a queue into a stampede, which is what OR-162 cost when that
// limit was misread.
//
// One child failing does not stop the others. Every child runs to
// completion and its result is collected regardless of what the others did
// -- a fan-out where one timeout discards four completed reports is worse
// than running them in sequence.
//
// Each job keeps its own Actor, Key, Model and Effort, so supervisor.Run's
// existing per-run cost accounting (recordTicketCost) produces one row per
// child exactly as it already does for a single run -- Fan adds
// concurrency, not a second accounting path.
//
// Each child runs against its own shallow copy of ws, not the shared
// pointer: Run mutates ws.Task in place before saving it, and N goroutines
// writing that struct concurrently is a data race, not just a bookkeeping
// wrinkle. This is the same isolation fixRun already uses for one
// off-workspace run (jobWS := *ws); Fan is that pattern applied to N.
func Fan(ws *workspace.Workspace, jobs []Options) []FanResult {
	maxConcurrent := config.Load(ws.RepoDir()).Limits.MaxConcurrentChildren
	announceFan(jobs, maxConcurrent)

	results := make([]FanResult, len(jobs))
	sem := make(chan struct{}, maxConcurrent)
	var wg sync.WaitGroup
	var landings sync.Mutex
	landed := 0
	for i, o := range jobs {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, o Options) {
			defer wg.Done()
			defer func() { <-sem }()
			childWS := *ws
			res, err := Run(&childWS, o)
			// Written at index i, captured by value: a fan that pairs a
			// result with the wrong job is worse than the serial work it
			// replaced, because it is wrong rather than merely slow.
			results[i] = FanResult{Result: res, Err: err}

			landings.Lock()
			landed++
			announceLanding(i, landed, len(jobs), results[i])
			landings.Unlock()
		}(i, o)
	}
	wg.Wait()
	return results
}

// announceLanding marks one child as it finishes, per nj-agents
// CONVENTIONS-orchestration §R.
//
// N children that report only once the last one is done are a silent gap
// ending in a wall of output, which from outside is indistinguishable from
// stuck. The count is running rather than positional because the order they
// land in is not the order they were dispatched in.
//
// Under the same mutex as the counter, so two children finishing together
// interleave their bytes on one line rather than producing a line each.
func announceLanding(i, landed, total int, r FanResult) {
	verdict := "done"
	switch {
	case r.Err != nil:
		verdict = "failed: " + r.Err.Error()
	case r.Result == nil:
		verdict = "returned nothing"
	case r.Result.ExitCode != 0:
		verdict = fmt.Sprintf("exit %d: %s", r.Result.ExitCode, r.Result.Reason)
	}
	fmt.Fprintf(os.Stderr, "orion: fan-out child %d %s (%d/%d landed)\n",
		i+1, verdict, landed, total)
}

// announceFan states the cost shape before any child starts: how many,
// capped at what, and on which models. Per nj-agents
// CONVENTIONS-orchestration §C -- concurrency that hides its own
// multiplication is a way to spend money faster, not a way to save it.
func announceFan(jobs []Options, maxConcurrent int) {
	models := make([]string, len(jobs))
	for i, o := range jobs {
		if o.Model != "" {
			models[i] = o.Model
		} else {
			models[i] = "default"
		}
	}
	fmt.Fprintf(os.Stderr, "orion: fan-out %d children (cap %d) -- models: %s\n",
		len(jobs), maxConcurrent, strings.Join(models, ", "))
}
