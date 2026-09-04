package collect

// The call site for batch integration (OR-236): what collect.Run does with a
// pass when collect.batch_integration is on.
//
// A separate function rather than branches threaded through one(). The
// per-branch path is what every repository uses today and what runs when the
// flag is off, and interleaving the two would make the old path's behaviour
// depend on a feature that is not enabled. Off, nothing here is reached.
//
// IT MERGES, and that is the change OR-253 made. This file used to end by
// reporting which members a green batch WOULD land, handing them back to the
// per-branch path to merge one at a time -- which left every remaining member
// behind the work branch, rebased each one, and bought a CI run per rebase.
// The batch now lands the ref it tested, so the work branch moves exactly once
// and nothing is ever behind it.
//
// The irreversible step still has one implementation: Deps.Merge, the same
// seam the per-branch path uses. What changed is what is handed to it.

import (
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/orion-sdlc/orion/internal/config"
	"github.com/orion-sdlc/orion/internal/events"
	"github.com/orion-sdlc/orion/internal/registry"
	"github.com/orion-sdlc/orion/internal/ui"
	"github.com/orion-sdlc/orion/internal/workspace"
)

// batchTester runs the forge's checks against an assembled ref.
//
// It publishes the ref and waits for the checks the repository already runs.
// Nothing here decides what "green" means: that is the same all-checks-passing
// rule the per-branch path uses, asked about a different ref.
type batchTester struct {
	git    repoGit
	status func(dir, branch string) (PR, error)
	openPR func(dir, branch, title, body, base string) (string, error)
	dir    string
	base   string
	wait   time.Duration
	out    io.Writer
	log    *events.Log
}

// Test publishes ref and reports whether its checks pass.
//
// An empty rollup is NOT passing here, and that is the one place this differs
// from the per-branch path deliberately. cmd/orion/collect.go treats no checks
// as VerdictPassing with the note "no checks are configured on this
// repository" -- right for a repository without CI, and catastrophic for a ref
// whose checks have simply not started yet. Under a merge ref every member
// would read green on no evidence at all, which is precisely how ADR 0015 says
// this gate disappears.
func (t batchTester) Test(ref string) (bool, error) {
	if err := t.git.PushRef(ref); err != nil {
		return false, err
	}

	// The pull request is what makes the run both HAPPEN and be READABLE.
	// `ci.yml` triggers on pull_request; a bare push to an ephemeral ref
	// matches no trigger, so without this nothing builds. And prStatus asks
	// `gh pr view`, so without this the checks cannot be read even when they
	// do run -- which is exactly what happened twice on 2026-08-31: CI green
	// on the ref, Orion reading "no pull requests found" and waiting out the
	// full deadline before refusing to call silence green.
	//
	// Opened before the poll rather than lazily, because the poll's whole
	// premise is that a result will eventually appear, and nothing would
	// produce one.
	if t.openPR != nil {
		title := fmt.Sprintf("batch: %s", ref)
		body := "Assembled by Orion and tested as one set (OR-253). " +
			"This pull request is the batch's single CI run and its review surface."
		if _, err := t.openPR(t.dir, ref, title, body, t.base); err != nil &&
			!strings.Contains(strings.ToLower(err.Error()), "already exists") {
			return false, fmt.Errorf("opening the batch pull request for %s: %w", ref, err)
		}
	}

	// ONE READ, NEVER A WAIT (OR-251). This is called from the watch tick,
	// and the tick's own contract is that the jobs it starts outlive it --
	// "a tick that blocked on the agent could never start a second". A
	// thirty-minute poll loop here broke exactly that: on 2026-08-31 the
	// console reported nothing for the whole of a batch's CI while three
	// agents carried on working, which is the OR-128 silent-hang shape
	// arriving through a door OR-128 could not have known about.
	//
	// The deadline did not go anywhere; it moved to the batch record, which
	// is where a thing that spans ticks belongs.
	pr, err := t.status(t.dir, ref)
	if err != nil {
		return false, fmt.Errorf("reading the checks on %s: %w", ref, err)
	}
	// Pushed, not pulled: this read already happened for the verdict, so the
	// display costs nothing extra and cannot disagree with the decision made
	// two lines below it. Same model as LiveSpend -- a number in the region
	// must not be the most expensive thing on the screen.
	ui.LiveChecks(UIChecks(pr.Checks))
	// From the STATUS read, not from openPR's return: a ref whose pull
	// request already exists gets an error and no URL from openPR, which is
	// the ordinary case on every tick after the first. This read answers on
	// every one of them, and it is the URL a landed member's ticket is
	// commented with (OR-314).
	rememberBatchPR(pr.URL)
	switch {
	case pr.Verdict == VerdictFailing:
		// Remembered for the summary: the culprit's row names the check that
		// actually failed, which is the difference between "CI failed" and a
		// line an operator can act on without opening the forge.
		rememberFailingCheck(pr.Checks)
		rememberBatchDetail(pr.Detail)
		return false, nil
	case pr.Verdict == VerdictPassing && !noChecksYet(pr):
		return true, nil
	}
	// Passing-with-no-checks and everything else are the same answer here:
	// nothing has reported. Silence is not success, and it is not failure
	// either -- it is "come back".
	return false, ErrCheckPending
}

// UIChecks converts this package's checks into the display's.
//
// Two types rather than one shared: internal/ui cannot import this package
// (it is imported BY it, for rendering), and a display type that had to
// track a forge's vocabulary would drag GitHub's dozen conclusions into a
// file whose whole job is to draw three states.
//
// Exported because the batch is no longer the only caller: internal/watch
// pushes the same rows for an ordinary run's tickets (OR-310), and a second
// copy of this switch is how the two paths would start disagreeing about
// what "running" looks like.
func UIChecks(in []Check) []ui.Check {
	out := make([]ui.Check, 0, len(in))
	for _, c := range in {
		state := ui.CheckPassed
		switch c.State {
		case CheckFailed:
			state = ui.CheckFailed
		case CheckRunning:
			state = ui.CheckRunning
		}
		out = append(out, ui.Check{Name: c.Name, State: state})
	}
	return out
}

// noChecksYet reports the empty rollup that cmd/orion/collect.go turns into a
// PASSING verdict with this exact note.
//
// Matching the note rather than a count because PR carries no count -- and it
// is worth the fragility, because the alternative is reading "no checks have
// started" as "every check passed". Under a merge ref that would mark every
// member green on no evidence, which ADR 0015 names as the way this gate
// disappears the day it ships.
func noChecksYet(pr PR) bool {
	return strings.Contains(strings.ToLower(pr.Detail), "no checks are configured")
}

// lastFailingCheck is the check that most recently went red on a batch ref.
//
// Package state rather than a field on batchTester because the reader is the
// observer, which is constructed separately and has nothing threaded to it.
// One batch runs at a time (see liveBatch), so there is one answer.
var lastFailingCheck struct {
	mu   sync.Mutex
	name string
}

func rememberFailingCheck(checks []Check) {
	for _, c := range checks {
		if c.State == CheckFailed {
			lastFailingCheck.mu.Lock()
			lastFailingCheck.name = c.Name
			lastFailingCheck.mu.Unlock()
			return
		}
	}
}

func failingCheck() string {
	lastFailingCheck.mu.Lock()
	defer lastFailingCheck.mu.Unlock()
	return lastFailingCheck.name
}

// lastBatchPR is the pull request the batch ref was last read through.
//
// Package state for the same reason lastFailingCheck is: Test learns it and
// runBatch needs it, with Land() in between carrying neither. One batch runs
// at a time (see liveBatch), so there is one answer.
//
// It is ALSO written to the batch record, because a batch waiting on an
// approver spans ticks and may span processes -- and the URL is what a landed
// member's ticket is commented with, which must survive a restart rather than
// silently become "the batch" with no address (OR-314).
var lastBatchPR struct {
	mu     sync.Mutex
	url    string
	detail string // the failure's why, as the last status read put it (OR-322)
}

// rememberBatchDetail keeps the last status read's Detail, which is what the
// culprit's ticket is commented with and what the fix agent is handed.
func rememberBatchDetail(detail string) {
	lastBatchPR.mu.Lock()
	lastBatchPR.detail = detail
	lastBatchPR.mu.Unlock()
}

func batchDetail() string {
	lastBatchPR.mu.Lock()
	defer lastBatchPR.mu.Unlock()
	return lastBatchPR.detail
}

func rememberBatchPR(url string) {
	if url == "" {
		return
	}
	lastBatchPR.mu.Lock()
	lastBatchPR.url = url
	lastBatchPR.mu.Unlock()
}

func batchPR() string {
	lastBatchPR.mu.Lock()
	defer lastBatchPR.mu.Unlock()
	return lastBatchPR.url
}

// liveObserver forwards a batch's progress to the pinned region (OR-246).
//
// A thin adapter with no state of its own: everything it knows it was just
// told, and everything it decides -- glyphs, colour, whether the terminal can
// take cursor control -- belongs to ui. That is what keeps this file free of
// rendering and batch.go free of both.
type liveObserver struct{}

func (liveObserver) Assembling(ref, base string, keys []string) {
	ui.LiveBatchStart(ref, base, keys)
	ui.LiveBatchPhase(ui.BatchAssembling)
}

func (liveObserver) Merged(key string) { ui.LiveBatchMember(key, ui.MemberMerged) }

// The reason rides along: it names the file the branch conflicted on, which
// is the difference between "go and look" and "go and look at THIS".
func (liveObserver) Ejected(key, reason string) {
	ui.LiveBatchMemberDetail(key, ui.MemberEjected, conflictFile(reason))
}

// conflictFile pulls the path out of a merge error, or returns the reason
// unchanged. git says "CONFLICT (content): Merge conflict in <path>", and the
// path is the only part of that sentence a person acts on.
func conflictFile(reason string) string {
	if i := strings.LastIndex(reason, "Merge conflict in "); i >= 0 {
		return strings.TrimSpace(reason[i+len("Merge conflict in "):])
	}
	return reason
}

func (liveObserver) Testing(int) { ui.LiveBatchPhase(ui.BatchTesting) }

func (liveObserver) Split(keys []string, green bool, depth, runs int, culprit bool) {
	ui.LiveBatchPhase(ui.BatchIsolating)
	ui.LiveBatchSplit(keys, green, depth, runs, culprit)
}

// Settled draws the durable summary and leaves it: the phase moves to done
// rather than the batch being closed, because the cost line and the per-member
// outcomes are the part worth still being on screen when the tick ends.
// runBatch closes it once it has reported.
func (liveObserver) Settled(landed, ejected, culprits, deferred []string) {
	for _, k := range landed {
		ui.LiveBatchMember(k, ui.MemberLanded)
	}
	for _, k := range ejected {
		ui.LiveBatchMember(k, ui.MemberEjected)
	}
	for _, k := range culprits {
		ui.LiveBatchMemberDetail(k, ui.MemberCulprit, failingCheck())
	}
	// Deferred is deliberately left as it was. A deferred branch is sound and
	// comes back, which is what "merged" already conveys here; giving it a
	// state of its own would put a fourth mark on screen for a distinction the
	// operator cannot act on differently from an ejection.
	_ = deferred
	ui.LiveBatchPhase(ui.BatchDone)
}

// resumeBatch acts on a batch that was already proved green and is waiting on
// an approver, without spending CI again.
//
// Returns done=false whenever anything at all has changed -- a different set
// of members, a base that moved, a record from another work branch -- so the
// caller assembles and tests from scratch. The bar for reusing a proof is
// that NOTHING relevant differs, because the failure mode on the other side
// is merging a set nobody tested.
//
// The record is cleared on every path that does not resume, so a stale file
// never survives to be compared against a second time.
func resumeBatch(ref string, members []Member, cfg config.Config, opts Options,
	deps Deps, g repoGit, ws *workspace.Workspace, log *events.Log, w io.Writer) ([]Result, bool) {

	st, ok := loadBatchState(ws.Dir)
	if !ok {
		return nil, false
	}
	base := cfg.VCS.WorkBranch
	baseSHA, err := g.SHAOf(base)

	// A TESTING batch is resumed before the validated-batch gate below, and
	// the order is the whole of OR-261.
	//
	// resumable() answers "may this PROOF still be used to merge?", so it
	// requires a validated status by construction. A batch whose CI is still
	// running has no proof yet -- and asking that question of it returned
	// false, cleared the state and reassembled. Every tick. The testing branch
	// twenty lines down was unreachable from the day it was written, which is
	// exactly the re-proving OR-251 exists to prevent, and the reassembly then
	// failed on the ref's own leftover worktree so nothing ever landed.
	//
	// The checks that DO apply to a testing batch are made here rather than
	// borrowed from resumable(): same base branch, same members, and a base
	// that has not moved. A moved base is not merely stale here, it means the
	// build being waited on is testing a tree that no longer exists.
	if err == nil && st.Status == batchTesting &&
		st.Base == base && st.BaseSHA != "" && st.BaseSHA == baseSHA &&
		sameMembers(st.Members, members) {
		return resumeTesting(st, members, cfg, opts, deps, g, ws, log, w), true
	}

	if err != nil || !st.resumable(base, baseSHA, members) {
		if st.BaseSHA != "" && baseSHA != "" && st.BaseSHA != baseSHA {
			ui.Warn(w, "%s moved since the batch was proved green; "+
				"reassembling and testing again rather than merging a result "+
				"that was not proved against it", base)
		}
		clearBatchState(ws.Dir)
		return nil, false
	}

	approve := batchApprover(cfg, opts, deps, ws, log, w)
	if approve == nil {
		// The gate was removed while a batch waited on it. Landing without
		// asking is the configured behaviour now, and the proof still stands.
		return landResumed(st, members, cfg, deps, g, ws, w), true
	}
	okd, err := approve(st.Ref, st.Members)
	if err != nil {
		return []Result{{Err: err}}, true
	}
	if !okd {
		// Still waiting, or declined. Either way nothing merges and the ref
		// stays exactly as the approver last saw it.
		var out []Result
		for _, m := range members {
			out = append(out, Result{Key: m.Key, Verdict: VerdictStale})
		}
		return out, true
	}
	return landResumed(st, members, cfg, deps, g, ws, w), true
}

// landResumed merges a proof recorded on an earlier pass.
//
// The base is re-read one final time even though resumable() just compared
// it: approval is a human-length gap, and the whole point of ADR 0017's
// precondition is that the base can move in exactly such a gap.
func landResumed(st batchState, members []Member, cfg config.Config, deps Deps,
	g repoGit, ws *workspace.Workspace, w io.Writer) []Result {

	now, err := g.SHAOf(st.Base)
	if err != nil || now != st.BaseSHA {
		ui.Warn(w, "%s moved between the approval and the merge; "+
			"nothing was merged and the batch will be assembled again", st.Base)
		clearBatchState(ws.Dir)
		var out []Result
		for _, m := range members {
			out = append(out, Result{Key: m.Key, Verdict: VerdictStale})
		}
		return out
	}
	if _, err := g.LandRef(st.Ref, st.Base); err != nil {
		return []Result{{Err: fmt.Errorf("landing the approved batch %s: %w", st.Ref, err)}}
	}
	clearBatchState(ws.Dir)
	_ = g.DropRef(st.Ref)
	_ = g.DeleteRemoteRef(st.Ref)

	ui.Say(w, "", events.ActorOrion, ui.VerbOK,
		"landed %d approved branch(es) as one, with no further CI run", len(members))

	// Every member of a resumed batch landed: the proof covers the recorded
	// set, and LandRef merged that set whole. The URL comes from the record
	// first -- an approval is a human-length gap and this may be a different
	// process from the one that opened the pull request (OR-314).
	prURL := st.PRURL
	if prURL == "" {
		prURL = batchPR()
	}
	closeLanded(keysOf(members), prURL, cfg.Tracker.QueueLabel, deps, w)

	var out []Result
	for _, m := range members {
		out = append(out, Result{Key: m.Key, Verdict: VerdictMerged, Changed: true})
	}
	return out
}

// runBatch lands the pass as one set.
//
// Returns a Result per ticket so the caller's contract is unchanged: the
// watcher reports the same shape whether the batch path or the per-branch path
// produced it.
func runBatch(pass []string, cfg config.Config, opts Options, deps Deps,
	ws *workspace.Workspace, log *events.Log, w io.Writer) []Result {

	// The concurrency limit IS the batch size. Nothing can be in a batch
	// that did not finish, and nothing finishes that was not allowed to run.
	size := cfg.Limits.ConcurrentTickets()

	// Built before the membership loop, which now asks git what is ready
	// rather than asking the forge whether a pull request exists.
	g := repoGit{ws: ws, merge: deps.Merge, openPR: deps.OpenPR}

	// The CI bar's reference, pushed once per batch. Read here rather than in
	// liveObserver because the observer has no workspace to read the log
	// from, and pulled once rather than per redraw for the reason
	// internal/cost states: a number in the region must not be the most
	// expensive thing on the screen. Zero samples leaves it unset, and the
	// bar then says "no baseline yet" instead of inventing one (OR-250).
	ui.LiveBatchMedian(batchBaseline(events.Path(ws.Dir)).Median)

	var members []Member
	for _, key := range pass {
		if len(members) >= size {
			break
		}
		// The branch a job actually RECORDED, never the one convention
		// predicts: AddWorktree suffixes a retry (orion/or-156-2) to keep it
		// off a prior attempt's open pull request, and recomputing the name
		// polls a branch that does not exist (OR-173).
		branch, recorded := workspace.BranchOf(ws, key)
		if !recorded {
			branch = branchFor(cfg.VCS.BranchPrefix, key)
		}
		// READINESS IS THE BRANCH BEING ON THE REMOTE, not a pull request
		// existing (OR-253).
		//
		// Requiring a pull request was what forced the work pipeline to open
		// one per ticket, which is what bought N CI runs before the batch had
		// tested anything. The branch being pushed is the same fact without
		// the cost: the agent finished, QA gave its verdict, and the commits
		// are somewhere the batch can merge from.
		//
		// A branch that resolves nowhere is simply not ready yet -- the agent
		// may still be running, or the push may have failed and been
		// reported elsewhere. Skipped in silence here, and the per-branch
		// path still has something to say about it.
		head, err := g.SHAOf(branch)
		if err != nil || head == "" {
			continue
		}
		// Already in the base is not a member, it is history. Without this a
		// merged branch would be assembled into every later batch, each time
		// contributing nothing and each time widening the set that has to be
		// bisected when something else fails.
		if merged, err := g.ContainsRef(cfg.VCS.WorkBranch, branch); err == nil && merged {
			continue
		}
		members = append(members, Member{Key: key, Branch: branch, Head: head})
	}
	if len(members) == 0 {
		return nil
	}

	ref := "orion/batch"
	if opts.DryRun {
		ui.Ok(w, "would", "assemble %d branch(es) into %s and test once: %s",
			len(members), ref, strings.Join(pass[:len(members)], " "))
		return nil
	}

	// RESUME BEFORE REASSEMBLING. A batch that is green and waiting on a
	// person is finished with CI: re-cutting the ref would force-push a new
	// merge commit, replace the pull request the approver is reading, and buy
	// another CI run to re-prove what is already proved -- once per tick, for
	// as long as they take to look.
	if res, done := resumeBatch(ref, members, cfg, opts, deps, g, ws, log, w); done {
		return res
	}

	ui.Say(w, "", events.ActorOrion, ui.VerbWorking,
		"assembling %d branch(es) into %s", len(members), ref)

	t := batchTester{git: g, status: deps.Status, openPR: deps.OpenPR,
		dir: ws.CloneDir(), base: cfg.VCS.WorkBranch,
		wait: 30 * time.Minute, out: w, log: log}
	b, err := Land(g, t, ref, cfg.VCS.WorkBranch, members, liveObserver{},
		WithApproval(batchApprover(cfg, opts, deps, ws, log, w)),
		WithIsolationWait(t.wait))

	// Local ref and its worktree, then the published branch. Both, and only
	// here: Test used to drop the remote ref as it returned, which cannot
	// stand now that LandRef merges the ref Test proved. Asked for explicitly
	// on 2026-08-31 -- a surviving orion/batch is force-pushed over by the
	// next batch and misleads anyone reading the remote in between.
	//
	// EXCEPT WHILE A PERSON IS BEING ASKED. A batch waiting on approval is
	// finished with CI and not finished with the ref: dropping it here would
	// have the next pass reassemble the same members, open another pull
	// request and buy another CI run to re-prove what is already proved --
	// and it would delete the very pull request the approver is looking at.
	// STILL BUILDING: record it and let the tick go (OR-251). The ref, its
	// pull request and the run they belong to all survive; the next pass
	// reads the checks once more. Every other ticket keeps being reported in
	// the meantime, which is the whole point.
	if b.Pending {
		if err := saveBatchState(ws.Dir, batchState{
			Ref: ref, Base: cfg.VCS.WorkBranch, Members: keysOf(members),
			Status: batchTesting, BaseSHA: b.BaseSHA, PRURL: batchPR(),
			TestingSince: time.Now(),
		}); err != nil {
			ui.Warn(w, "the batch is building but its record could not be written "+
				"(%v); the next pass will assemble it again", err)
		}
		ui.Say(w, "", events.ActorOrion, ui.VerbOK,
			"%d branch(es) assembled into %s; CI is running and the next tick reads it",
			len(members), ref)
		var out []Result
		for _, m := range members {
			out = append(out, Result{Key: m.Key, Verdict: VerdictPending})
		}
		return out
	}

	if b.AwaitingApproval {
		// Written now so the next pass resumes instead of re-proving. A
		// failure to record it is reported rather than fatal: the batch is
		// green either way, and the cost of the record being lost is one
		// repeated CI run, not a wrong merge -- resumable() still refuses
		// anything it cannot verify.
		if err := saveBatchState(ws.Dir, batchState{
			Ref: ref, Base: cfg.VCS.WorkBranch, Members: keysOf(members),
			Status: batchValidated, BaseSHA: b.BaseSHA, ValidatedSHA: b.ValidatedSHA,
			PRURL: batchPR(),
		}); err != nil {
			ui.Warn(w, "the batch is green but its record could not be written (%v); "+
				"the next pass will test it again", err)
		}
	} else {
		clearBatchState(ws.Dir)
		_ = g.DropRef(ref)
		_ = g.DeleteRemoteRef(ref)
	}

	// Closed after the report is written, so the summary the observer left on
	// screen is the last thing the region showed before the scrollback takes
	// over.
	defer ui.LiveBatchEnd()

	for _, line := range b.Describe() {
		fmt.Fprintf(w, "          %s\n", ui.Dim(w, line))
	}
	// The baseline is read from THIS repository's own history, not modelled
	// (OR-250). Past per-branch landings are in the log as a push followed by
	// a merge; their median is what the old path actually cost here, on this
	// machine, with this CI.
	base := perBranchBaseline(events.Path(ws.Dir))
	landed := b.Members(Landed)

	// A BATCH THAT LANDED NOTHING IS NOT A COST, IT IS A FAILURE (OR-261).
	//
	// This printed "the batch cost 0 CI runs for 2 branches, in 1s" with a
	// green tick, once a minute all night, while develop never moved and both
	// members sat unmerged. Zero runs in one second is the shape of a cycle
	// that did no work; rendering it as an accomplishment is how a stall reads
	// as success to somebody scrolling past.
	if len(landed) == 0 {
		ui.Warn(w, "the batch landed nothing: %s",
			costLine(b.Runs, len(members), b.Elapsed, base))
	} else {
		ui.Say(w, "", events.ActorOrion, ui.VerbOK,
			"the batch cost %s", costLine(b.Runs, len(members), b.Elapsed, base))
	}

	// The note is emitted either way. A batch that landed nothing is still a
	// batch that happened, and the dashboard reads this sentence to count
	// runs spent -- dropping it would hide the cost of exactly the cycles
	// worth seeing.
	//
	// Built by events.BatchNote rather than by a Printf here, because the
	// dashboard reads it back and the two used to agree only by coincidence
	// (OR-258). One format, one place, tested against itself.
	log.Emitf(events.KindNote, events.ActorOrion, "%s", events.BatchNote{
		Ref: ref, Runs: b.Runs, Elapsed: b.Elapsed,
		Landed: landed, Ejected: b.Members(Ejected),
		Culprit: b.Members(Culprit), Deferred: b.Members(Deferred),
		Median: base.Median, Samples: base.Samples,
	})

	// A MERGE PER MEMBER, because the batch merged them (OR-258).
	//
	// The batch lands the ref rather than merging ticket by ticket -- that is
	// the whole of OR-253 and why the rebase cascade is gone -- so no member
	// ever emitted the merge event the per-branch path emits. Anything
	// counting merges per key therefore saw them stop at `push` and stay
	// there: the dashboard reported four tickets waiting to integrate that
	// had landed days earlier, which pinned the one signal it exists to give.
	//
	// Emitted here rather than fixed in the dashboard because the dashboard is
	// not the only reader of the log, and the next thing to count merges would
	// have inherited the same hole.
	for _, key := range landed {
		log.Emit(events.Event{Kind: events.KindMerge, Actor: events.ActorOrion,
			Key: key, Msg: "landed in the batch on " + ref})
	}

	// AND THE TRACKER, which knew none of this (OR-314). The batch names its
	// landed members exactly -- it just told the screen and the log about them
	// and stopped there, so three tickets merged in one pull request sat In
	// Progress carrying orion-ready until somebody closed them by hand, and
	// the label brought them back into the queue in the meantime.
	closeLanded(landed, batchPR(), cfg.Tracker.QueueLabel, deps, w)

	var out []Result
	for _, r := range b.Results {
		res := Result{Key: r.Key, Changed: true}
		switch r.Outcome {
		case Landed:
			// ALREADY MERGED, as part of the set that was tested. Not
			// VerdictPassing any more: that meant "green, now let the
			// per-branch path merge it", and merging members one at a time is
			// what left every other member behind the work branch and paid
			// for a rebase and a fresh CI run each (OR-253). The thing that
			// was tested is the thing that merged, so there is nothing left
			// to do with it.
			res.Verdict = VerdictMerged
		case Culprit:
			// INTO THE FIX LOOP, as a per-branch failure would be (OR-322).
			//
			// runBatch's results are returned directly; the per-ticket path
			// that relabels, comments, notifies and dispatches the fix agent
			// is never reached for a batch member. So a convicted culprit was
			// left orion-ready, collected into the next batch, and convicted
			// again -- with the row saying "fix round 1 of 3" about a loop
			// that did not exist.
			res = failCulprit(res, r.Member, cfg, opts, deps, ws, log, w)
		default:
			// Ejected and deferred are not failures: the branch is sound and
			// will be offered to the next batch. Saying "stale" reuses the
			// verdict the watcher already renders as waiting.
			res.Verdict = VerdictStale
			res.Changed = false
		}
		out = append(out, res)
	}
	if err != nil {
		ui.Warn(w, "the batch did not complete: %v", err)
		out = append(out, Result{Err: err})
	}
	return out
}

// closeLanded finishes every ticket the batch actually landed.
//
// THE MEMBERS IT IS GIVEN AND NO OTHERS. An ejected member did not land -- its
// branch is still waiting and will be offered to the next batch -- and a
// culprit is the reason the batch went red. Closing either would report work
// as delivered that is not on the trunk, which is the worse half of the bug
// this fixes: OR-314's symptom was tickets left open, but a batch that closed
// its ejections would strand branches nobody was looking for any more.
//
// Every failure is a warning. The merge HAS happened by the time this runs --
// the ref is on the work branch -- so turning a tracker hiccup into a failed
// collect would report a successful merge as a failure. Same judgement, and
// for the same reason, as closeChildren's.
func closeLanded(landed []string, prURL, queueLabel string, deps Deps, w io.Writer) {
	if deps.Jira == nil {
		return
	}
	for _, key := range landed {
		if err := closeTicket(key, prURL, queueLabel, deps, w); err != nil {
			ui.Warn(w, "%s: landed in the batch, but its labels could not be "+
				"cleared: %v. It will be collected again until they are.", key, err)
		}
	}
}

// batchContext resolves the workspace, log and config a batch needs.
//
// From the FIRST ticket in the pass. A batch is assembled in one repository's
// sandbox, so a pass spanning two projects has no single batch to build; using
// one project's config for the set is therefore accurate rather than merely
// convenient, and the members loop skips anything whose branch does not
// resolve in that workspace.
//
// Config comes from entry.Source -- the user's checkout -- never the sandbox
// clone, for the reason one() gives: read work_branch from a stale sandbox and
// you sync the sandbox to the stale branch, which makes the value that decides
// where things land depend on where things last landed.
func batchContext(pass []string, opts Options, deps Deps) (
	*workspace.Workspace, *events.Log, io.Writer, config.Config, bool) {

	w := opts.Out
	if len(pass) == 0 {
		return nil, nil, w, config.Config{}, false
	}
	entry, err := registry.Lookup(opts.Home, pass[0])
	if err != nil {
		return nil, nil, w, config.Config{}, false
	}
	ws, err := workspace.Open(entry.Workspace)
	if err != nil {
		return nil, nil, w, config.Config{}, false
	}
	cfg := config.Load(entry.Source)
	// The same shape one() uses: events.Open returns a usable log either way,
	// and a batch without a written record is still a batch worth running --
	// the console reports it regardless. Refusing over bookkeeping would lose
	// the work to protect the note about it.
	// NOT closed here. The caller writes the batch's record through it after
	// this returns, so closing on the way out would shut the log before the
	// only thing that uses it has run.
	log, _ := events.Open(events.Path(ws.Dir), events.Event{
		Project: registry.ProjectOf(pass[0]), Actor: events.ActorOrion,
	})
	return ws, log, w, cfg, true
}

// resumeTesting reads the checks on a batch that was already published, once.
//
// The deadline is enforced HERE rather than inside a loop, from the record
// rather than from a local variable. Same refusal as before -- silence is
// never green, however long it lasts -- and the same thirty minutes; the only
// thing that changed is that nobody sits and waits for it.
func resumeTesting(st batchState, members []Member, cfg config.Config, opts Options,
	deps Deps, g repoGit, ws *workspace.Workspace, log *events.Log, w io.Writer) []Result {

	t := batchTester{git: g, status: deps.Status, openPR: deps.OpenPR,
		dir: ws.CloneDir(), base: st.Base, out: w, log: log}

	ok, err := t.Test(st.Ref)
	switch {
	case errors.Is(err, ErrCheckPending):
		if st.waitedOut(time.Now(), batchCheckDeadline) {
			// Reported and cleared, so the next tick assembles rather than
			// waiting on a build that is never going to report.
			ui.Warn(w, "no check result for %s after %s; refusing to read silence "+
				"as green. The batch will be assembled again.", st.Ref, batchCheckDeadline)
			clearBatchState(ws.Dir)
			_ = g.DropRef(st.Ref)
			_ = g.DeleteRemoteRef(st.Ref)
			return []Result{{Err: fmt.Errorf(
				"no check result for %s after %s", st.Ref, batchCheckDeadline)}}
		}
		waited := time.Since(st.TestingSince).Round(time.Minute)
		ui.Ok(w, "ci", "%s: %d branch(es), %s elapsed", st.Ref, len(members), waited)
		return pendingResults(members)

	case err != nil:
		return []Result{{Err: err}}

	case !ok:
		// Red. Cleared so the next pass reassembles and bisects, which needs
		// the full Land path rather than this one.
		clearBatchState(ws.Dir)
		ui.Warn(w, "%s went red; the next pass will isolate the cause", st.Ref)
		return pendingResults(members)
	}

	// Green. The approval gate and the merge are exactly the ones a
	// first-pass green batch goes through; nothing here is a second copy of
	// that decision.
	st.Status, st.ValidatedSHA = batchValidated, st.Ref
	// Test just read the pull request, so this is the freshest the record will
	// get before an approver is asked to look at it.
	if url := batchPR(); url != "" {
		st.PRURL = url
	}
	if err := saveBatchState(ws.Dir, st); err != nil {
		ui.Warn(w, "the batch is green but its record could not be updated (%v)", err)
	}
	approve := batchApprover(cfg, opts, deps, ws, log, w)
	if approve == nil {
		return landResumed(st, members, cfg, deps, g, ws, w)
	}
	okd, err := approve(st.Ref, st.Members)
	if err != nil {
		return []Result{{Err: err}}
	}
	if !okd {
		return pendingResults(members)
	}
	return landResumed(st, members, cfg, deps, g, ws, w)
}

// batchCheckDeadline is how long a batch may wait for its checks to report.
//
// The same thirty minutes the poll loop used, moved to where a wait that
// spans ticks belongs. It is a DEADLINE, not a delay: a batch whose CI
// reports in nine minutes is read on the next tick after that.
const batchCheckDeadline = 30 * time.Minute

func pendingResults(members []Member) []Result {
	var out []Result
	for _, m := range members {
		out = append(out, Result{Key: m.Key, Verdict: VerdictPending})
	}
	return out
}

// failCulprit hands a convicted member to the same failure path a per-branch
// red build takes: orion-failed, a comment naming where and why, a
// notification, and -- with auto_fix -- the fix agent on the member's own
// branch. The pull request it cites is the BATCH's, because that is where
// the failure is; the branch it fixes is the member's, because that is where
// the fault is.
func failCulprit(res Result, m Member, cfg config.Config, opts Options, deps Deps,
	ws *workspace.Workspace, log *events.Log, w io.Writer) Result {

	res.Verdict = VerdictFailing
	if deps.Jira == nil {
		return res
	}
	detail := batchDetail()
	if detail == "" {
		detail = "the batch went red"
		if c := failingCheck(); c != "" {
			detail += " on " + c
		}
	}
	pr := PR{URL: batchPR(), Verdict: VerdictFailing, Head: m.Head,
		Detail: "convicted by the batch's isolation: " + detail}
	return failing(res, m.Key, pr, cfg, m.Branch, opts, deps, ws, log, w)
}
