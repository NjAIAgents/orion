package main

// `orion watch --demo`: drive the live region through its states against
// fabricated data, then exit.
//
// This exists because there was no cheap way to LOOK at the display. A real
// run costs agents and takes an hour to reach a batch; --dry-run rehearses one
// tick and starts nothing, so the region has no rows, no batch and no checks
// and correctly draws nothing -- which is indistinguishable from a region that
// was never built. The batch view was believed unimplemented for weeks on
// exactly that evidence (OR-264).
//
// So: no network, no credentials, no agents, no spend, and no writes. It
// touches the same renderer the watcher does, through the same exported calls
// in internal/ui, so what it shows is what a real run shows. A demo that drew
// its own approximation of the display would be worse than none -- it would
// agree with the code only until somebody changed one of them.

import (
	"flag"
	"fmt"
	"io"
	"time"

	"github.com/orion-sdlc/orion/internal/events"
	"github.com/orion-sdlc/orion/internal/ui"
)

// demoKeys are the tickets in the fabricated run. Three, because that is the
// smallest number that shows the paired rows AND leaves an odd one over.
var demoKeys = []string{"OR-223", "OR-224", "OR-242"}

// demoStep is how long each state holds before the next.
const demoStep = 4 * time.Second

func runWatchDemo(w io.Writer, args []string) {
	fs := flag.NewFlagSet("watch --demo", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	step := fs.Duration("demo-step", demoStep, "how long each state holds")
	// Parsing is best-effort: --demo arrives alongside watch's own flags,
	// which this set does not know about. An unparseable extra is not worth
	// refusing to draw over.
	_ = fs.Parse(args)

	fmt.Fprintln(w, ui.Heading(w, "demo"))
	fmt.Fprintln(w, ui.Dim(w, "  fabricated data, no agents and no network. ctrl-c to stop."))
	fmt.Fprintln(w)

	ui.LiveReset()
	defer ui.LiveReset()
	// A median for every actor, so the bars have something to measure
	// against. The no-baseline case gets its own state below.
	ui.LiveMedians(func(string) time.Duration { return 24 * time.Minute })

	live := ui.NewLive(w)
	defer live.Close()
	// THE WRITER IS REPLACED, exactly as internal/watch does it. A line that
	// went to the original w would bypass the frozen window entirely and be
	// overdrawn by the next redraw -- which is precisely the bug this demo was
	// written to make visible, and which it shipped with on the first cut.
	w = live

	say := func(format string, a ...any) {
		ui.Say(w, "", events.ActorOrion, ui.VerbWorking, format, a...)
	}

	// 1. Agents working: a row each, with bars, sparklines and call counts.
	for i, k := range demoKeys {
		ui.LiveStart(k)
		ui.LiveActivity(k, actorFor(i))
		say("claimed %s", k)
		time.Sleep(*step / 4)
	}
	demoActivity(w, *step)

	// 2. Assembling: membership is the information, and one branch is ejected.
	say("assembling %d branches into orion/batch", len(demoKeys))
	ui.LiveBatchStart("orion/batch", "develop", append(demoKeys, "OR-229"))
	ui.LiveBatchPhase(ui.BatchAssembling)
	for _, k := range demoKeys {
		ui.LiveBatchMember(k, ui.MemberMerged)
	}
	ui.LiveBatchMember("OR-229", ui.MemberEjected)
	say("OR-229 conflicts with the batch; it returns to the queue")
	demoActivity(w, *step)

	// 3. Testing with NO baseline: the case that used to render as fourteen
	// blank columns and nothing else.
	say("pushed orion/batch, waiting on checks")
	ui.LiveBatchPhase(ui.BatchTesting)
	ui.LiveChecks([]ui.Check{
		{Name: "go (ubuntu)", State: ui.CheckRunning},
		{Name: "go (macos)", State: ui.CheckRunning},
		{Name: "go (windows)", State: ui.CheckRunning},
	})
	demoActivity(w, *step)

	// 4. Testing WITH a baseline, and one check home.
	ui.LiveBatchMedian(11 * time.Minute)
	ui.LiveChecks([]ui.Check{
		{Name: "go (ubuntu)", State: ui.CheckPassed},
		{Name: "go (macos)", State: ui.CheckRunning},
		{Name: "go (windows)", State: ui.CheckRunning},
	})
	demoActivity(w, *step)

	// 5. Isolating: the tree, because the shape of the search is what explains
	// the cost.
	ui.Say(w, "", events.ActorOrion, ui.VerbWarn, "the batch is red; isolating")
	ui.LiveChecks([]ui.Check{
		{Name: "go (ubuntu)", State: ui.CheckPassed},
		{Name: "go (macos)", State: ui.CheckFailed},
		{Name: "go (windows)", State: ui.CheckPassed},
	})
	ui.LiveBatchSplit(demoKeys, false, 0, 1, false)
	demoActivity(w, *step/2)
	ui.LiveBatchSplit(demoKeys[:2], true, 1, 2, false)
	demoActivity(w, *step/2)
	ui.LiveBatchSplit(demoKeys[2:], false, 1, 3, true)
	demoActivity(w, *step)

	// 6. Done: the cost line first, then what became of each member.
	ui.LiveBatchMember("OR-223", ui.MemberLanded)
	ui.LiveBatchMember("OR-224", ui.MemberLanded)
	ui.LiveBatchMember("OR-242", ui.MemberCulprit)
	ui.LiveBatchPhase(ui.BatchDone)
	for _, k := range demoKeys {
		ui.LiveEnd(k)
	}
	ui.LiveChecks(nil)
	demoActivity(w, *step)

	ui.Say(w, "", events.ActorOrion, ui.VerbOK, "demo complete; nothing was started and nothing was written")
}

// actorFor spreads the demo's tickets over more than one actor, so the actor
// column is visibly a column rather than the same word three times.
func actorFor(i int) string {
	switch i % 3 {
	case 0:
		return events.ActorImplementer
	case 1:
		return events.ActorQA
	default:
		return events.ActorFrontend
	}
}

// demoChatter is the agents' running commentary: the stream that fills the
// frozen window and then scrolls out of it.
//
// Without it the window never reaches its five-line floor, so the cap and the
// wall -- the whole subject of the mockup's second state -- cannot be seen.
// A demo that shows an unfilled window demonstrates nothing about a window
// whose entire purpose is to bound an unbounded stream.
var demoChatter = []string{
	"edited internal/collect/batchrun.go",
	"ran go test ./internal/work  (14.2s, ok)",
	"read internal/ui/live.go",
	"edited internal/ui/livebatch.go",
	"ran gofmt -l .  (clean)",
	"pushed orion/or-242  (3 commits)",
	"ran go build ./...  (ok)",
	"read internal/watch/watch.go",
	"edited internal/watch/watch_test.go",
	"ran go vet ./internal/watch  (ok)",
}

// demoActivity holds a state for d, feeding tool calls in so the spinners turn
// and the sparklines move, and emitting chatter so the frozen window fills and
// then starts losing its oldest line. A frozen screen would not show that the
// region is redrawing, which is half of what there is to look at.
func demoActivity(w io.Writer, d time.Duration) {
	deadline := time.Now().Add(d)
	for i := 0; time.Now().Before(deadline); i++ {
		time.Sleep(200 * time.Millisecond)
		// Not every tick, and not every ticket: an even pulse looks like a
		// progress bar rather than like work.
		if i%2 == 0 {
			key := demoKeys[i/2%len(demoKeys)]
			ui.LiveActivity(key, "")
			line := demoChatter[demoChatterAt%len(demoChatter)]
			demoChatterAt++
			// Say, not Trace: Trace is withheld unless --verbose, and a demo
			// whose whole subject is the scrolling window must not depend on a
			// flag to have anything to scroll.
			ui.Say(w, key, actorFor(i/2%len(demoKeys)), ui.VerbWorking, "%s", line)
		}
	}
}

// demoChatterAt walks the chatter so a repeated state does not replay the same
// three lines, which would read as a stuck screen rather than a moving one.
var demoChatterAt int
