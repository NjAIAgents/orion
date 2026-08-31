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
	"fmt"
	"io"
	"strings"
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

	// NOT deleted on the way out any more. The ref has to survive Test for
	// LandRef to merge the thing that was tested; dropping it here was safe
	// only while a green batch handed its members back to the per-branch path
	// to merge individually, which is the behaviour OR-253 removes. Cleanup
	// is the caller's, after landing.
	deadline := time.Now().Add(t.wait)
	for {
		pr, err := t.status(t.dir, ref)
		if err != nil {
			return false, fmt.Errorf("reading the checks on %s: %w", ref, err)
		}
		switch {
		case pr.Verdict == VerdictFailing:
			return false, nil
		case pr.Verdict == VerdictPassing && !noChecksYet(pr):
			return true, nil
		case pr.Verdict == VerdictPassing:
			// Silence is not success. Keep waiting; the deadline below ends
			// this, not an absence of evidence.
		}
		if time.Now().After(deadline) {
			return false, fmt.Errorf(
				"no check result for %s after %s; refusing to read silence as green", ref, t.wait)
		}
		time.Sleep(15 * time.Second)
	}
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

func (liveObserver) Ejected(key, _ string) { ui.LiveBatchMember(key, ui.MemberEjected) }

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
		ui.LiveBatchMember(k, ui.MemberCulprit)
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
		return landResumed(st, members, g, ws, w), true
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
	return landResumed(st, members, g, ws, w), true
}

// landResumed merges a proof recorded on an earlier pass.
//
// The base is re-read one final time even though resumable() just compared
// it: approval is a human-length gap, and the whole point of ADR 0017's
// precondition is that the base can move in exactly such a gap.
func landResumed(st batchState, members []Member, g repoGit,
	ws *workspace.Workspace, w io.Writer) []Result {

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
		WithApproval(batchApprover(cfg, opts, deps, ws, log, w)))

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
	if b.AwaitingApproval {
		// Written now so the next pass resumes instead of re-proving. A
		// failure to record it is reported rather than fatal: the batch is
		// green either way, and the cost of the record being lost is one
		// repeated CI run, not a wrong merge -- resumable() still refuses
		// anything it cannot verify.
		if err := saveBatchState(ws.Dir, batchState{
			Ref: ref, Base: cfg.VCS.WorkBranch, Members: keysOf(members),
			Status: batchValidated, BaseSHA: b.BaseSHA, ValidatedSHA: b.ValidatedSHA,
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
	ui.Say(w, "", events.ActorOrion, ui.VerbOK,
		"the batch cost %d CI run(s) for %d branch(es)", b.Runs, len(members))
	log.Emitf(events.KindNote, events.ActorOrion,
		"batch on %s: %d run(s), landed=%v ejected=%v culprit=%v deferred=%v",
		ref, b.Runs, b.Members(Landed), b.Members(Ejected),
		b.Members(Culprit), b.Members(Deferred))

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
			res.Verdict = VerdictFailing
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
