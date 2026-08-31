package supervisor

import (
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/orion-sdlc/orion/internal/config"
	"github.com/orion-sdlc/orion/internal/workspace"
)

// fanOut is where a fan-out narrates itself.
//
// Stderr in every real run, because stdout is whatever the caller is printing
// for its own reader and a roster interleaved into that is corruption, not
// progress. A variable so a test can read what a dispatch actually said:
// "announce before dispatch" is a behaviour, and a behaviour nothing can
// observe is one that regresses silently.
var fanOut io.Writer = os.Stderr

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
	// One child is not a fan-out, and §C and §R are both about a fan-out:
	// what they exist to make legible is the multiplication, and there is
	// none here. Announcing it anyway would put three lines of roster on
	// every single-question `orion explore` -- a call that printed nothing
	// before this and whose whole contract is that it behaves as it always
	// did. Noise on the common path also costs the batch case, because a
	// reader who has learned to skip these lines skips the ones that matter.
	announce := len(jobs) > 1
	if announce {
		announceFan(jobs, maxConcurrent)
	}

	results := make([]FanResult, len(jobs))
	sem := make(chan struct{}, maxConcurrent)
	var wg sync.WaitGroup
	// Landings are printed under a lock and counted there, so N goroutines
	// finishing at once produce N whole lines in the order they landed rather
	// than interleaved halves, and so "3/5 back" is a count nobody has to
	// reconstruct from how many lines have scrolled past.
	var mu sync.Mutex
	var landed int
	for i, o := range jobs {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, o Options) {
			defer wg.Done()
			defer func() { <-sem }()
			childWS := *ws
			res, err := Run(&childWS, o)
			results[i] = FanResult{Result: res, Err: err}
			if !announce {
				return
			}
			mu.Lock()
			defer mu.Unlock()
			landed++
			announceLanded(i, o, res, err, landed, len(jobs))
		}(i, o)
	}
	wg.Wait()
	return results
}

// announceFan states the cost shape before any child starts: how many,
// capped at what, and on which models. Per nj-agents
// CONVENTIONS-orchestration §C -- concurrency that hides its own
// multiplication is a way to spend money faster, not a way to save it.
//
// Then the roster, one line per child, per §R. A subagent reports once at the
// end and has no way to say anything while it works, so this is the only
// place a fan-out can be made legible: without it, N children are a silent
// gap ending in a wall of output, and nobody watching can tell working from
// stuck. Printed here rather than by each caller for the reason the cap is
// enforced here -- one place, so it cannot drift between call sites.
func announceFan(jobs []Options, maxConcurrent int) {
	models := make([]string, len(jobs))
	for i, o := range jobs {
		models[i] = modelOf(o)
	}
	fmt.Fprintf(fanOut, "orion: fan-out %d children (cap %d) -- models: %s\n",
		len(jobs), maxConcurrent, strings.Join(models, ", "))
	for i, o := range jobs {
		fmt.Fprintf(fanOut, "orion:   ... %s  %s\n", labelOf(i, o), modelOf(o))
	}
}

// announceLanded marks one child as it comes back, with its verdict rather
// than a summary of it: which child, whether it worked, and how long it took.
// The n/total is the outstanding count made visible -- §R's requirement is
// that at any moment a reader can see what was dispatched, what is back, and
// what is still running.
//
// The child is named by the same label its roster line used, so a reader
// matches a landing to a dispatch by reading rather than by counting. In a
// fan of five explores every label is the word "explore", which is why the
// label carries the job's position too.
func announceLanded(i int, o Options, res *Result, err error, n, total int) {
	mark, verdict := "ok", "exit 0"
	switch {
	case err != nil && res == nil:
		mark, verdict = "FAILED", err.Error()
	case res == nil:
		mark, verdict = "FAILED", "no result"
	case res.ExitCode != 0:
		mark, verdict = "FAILED", fmt.Sprintf("exit %d: %s", res.ExitCode, res.Reason)
	}
	var took string
	if res != nil {
		took = fmt.Sprintf(" in %s", res.Duration.Round(time.Second))
	}
	fmt.Fprintf(fanOut, "orion:   %s %d/%d %s  %s%s\n", mark, n, total, labelOf(i, o), verdict, took)
}

// labelOf names a child the way its caller would recognise it: its position
// in the jobs slice, and the actor it runs as, else the stage. Position alone
// is unreadable and a name alone is ambiguous -- five explores dispatched
// together share one name and differ only by which question they were given.
func labelOf(i int, o Options) string {
	name := o.Actor
	if name == "" {
		name = o.Stage
	}
	return strings.TrimSpace(fmt.Sprintf("#%d %s", i+1, name))
}

func modelOf(o Options) string {
	if o.Model == "" {
		return "default"
	}
	return o.Model
}
