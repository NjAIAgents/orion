package supervisor

import (
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/orion-sdlc/orion/internal/config"
	"github.com/orion-sdlc/orion/internal/workspace"
)

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
	for i, o := range jobs {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, o Options) {
			defer wg.Done()
			defer func() { <-sem }()
			childWS := *ws
			res, err := Run(&childWS, o)
			results[i] = FanResult{Result: res, Err: err}
		}(i, o)
	}
	wg.Wait()
	return results
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
