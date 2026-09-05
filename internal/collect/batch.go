package collect

// Batch integration (OR-236), decided by ADR 0015.
//
// Today every branch is rebased onto develop and tested on its own pull
// request, so landing N tickets costs N rebases and N full CI runs. The
// branches are usually independent and usually green, so most of that work
// re-proves the same thing N times. Measured on this project over
// 2026-08-29..30: 27 rebases across ~26 merges, and a per-branch CI failure
// rate of 0.10.
//
// Instead: collect the branches that are ready, merge them into ONE ephemeral
// ref, test that once, and land the whole set. Agent branches are never
// rewritten, so there is no rebase, no force-push and no landing queue -- the
// machinery this replaces is rebase.go, staleness.go and requests.go.
//
// Two properties carry the design, and both are about failure rather than the
// happy path:
//
//	EJECT AT ASSEMBLY  a branch that will not merge leaves before CI runs, so
//	                   the ref only ever holds branches that combined cleanly
//	                   and a red result is never a merge problem. The ref is
//	                   ephemeral, so a conflict resolved in it would be
//	                   thrown away; the fix has to land on the real branch.
//
//	ISOLATE ON RED     at N=10 and p=0.10 two thirds of batches go red, so
//	                   isolation is the common path, not the error handler.
//	                   Splitting costs ~2*log2(N) runs, not log2(N): both
//	                   halves are tested at each split, not only the failing
//	                   one.
//
// OFF BY DEFAULT. This changes how work merges, and a mistake here mis-lands
// or strands every branch in flight. It is enabled per repository, watched
// the first few times, and only then left alone.

import (
	"errors"
	"fmt"
	"sort"
	"time"
)

// Member is one branch offered to a batch.
type Member struct {
	Key    string // the ticket, e.g. "OR-236"
	Branch string
	Head   string // the commit the forge reported, carried for the record
}

// Outcome is what became of one member.
type Outcome string

const (
	Landed   Outcome = "landed"   // in the batch, and the batch was green
	Ejected  Outcome = "ejected"  // would not merge; never reached CI
	Culprit  Outcome = "culprit"  // isolated as the reason the batch went red
	Deferred Outcome = "deferred" // sound, but the batch it was in failed
)

// Result is one member's outcome and why.
type MemberResult struct {
	Member
	Outcome Outcome
	Reason  string
}

// Batch is the outcome of one assembly-and-test cycle.
type Batch struct {
	Base    string
	Ref     string
	Results []MemberResult
	// Runs is how many CI runs the cycle consumed, including isolation. The
	// number the whole design is justified by, so it is recorded rather than
	// estimated afterwards.
	Runs int

	// The SHAs a landing decision rests on (ADR 0017). Recorded rather than
	// re-derived, because the question they answer -- "was this result proven
	// against the thing it merged into?" -- cannot be answered afterwards
	// from a repository that has already moved on.
	//
	// BaseSHA is the base as it stood when the set was assembled and tested.
	// ValidatedSHA is the ref CI actually proved. LandedSHA is where base
	// ended up. Equal BaseSHA and a later re-read is the precondition on
	// merging; a difference between them is a bug report, not a retry.
	BaseSHA      string
	ValidatedSHA string
	LandedSHA    string

	// Elapsed is wall clock from the first merge into the ephemeral ref to
	// the last member landing, isolation rounds included (OR-250).
	//
	// THE NUMBER THE OPERATOR CAME WITH. Runs is the cost model ADR 0015
	// argued in; this is the minutes between "the agents finished" and "the
	// work is on the work branch", which is what anybody watching is actually
	// asking about. Isolation is inside it rather than timed separately: a
	// batch that went red and bisected cost what it cost.
	Elapsed time.Duration

	// Pending is set when CI has not reported yet (OR-251).
	//
	// A THIRD ANSWER, beside green and red, and the tick needs it: without
	// one, the only way to wait for a build is to block, and a tick that
	// blocks for thirty minutes stops reporting every OTHER ticket the
	// watcher is running. The batch is recorded and resumed on a later pass.
	Pending bool

	// AwaitingApproval is set when the batch is green and proved but a person
	// has not said yes yet. Distinct from a failure in every direction: the
	// members stay unmerged, nothing is blamed, and the next pass asks again.
	AwaitingApproval bool

	// approve gates the merge. Unexported because it is a decision the caller
	// injects, not a fact about the batch worth reporting or serialising.
	approve Approver
}

// Members returns the keys with a given outcome, in order.
func (b Batch) Members(o Outcome) []string {
	var out []string
	for _, r := range b.Results {
		if r.Outcome == o {
			out = append(out, r.Key)
		}
	}
	sort.Strings(out)
	return out
}

// Green reports whether every member landed.
func (b Batch) Green() bool {
	for _, r := range b.Results {
		if r.Outcome != Landed {
			return false
		}
	}
	return len(b.Results) > 0
}

// Git is the repository operations a batch needs. An interface so the
// assembly and isolation logic -- the part with the interesting failure
// modes -- is testable without a repository.
type Git interface {
	// CutRef creates ref at base, discarding any previous ref of that name.
	CutRef(ref, base string) error
	// MergeInto merges branch into ref. A non-nil error means it conflicted:
	// the caller ejects rather than resolving.
	MergeInto(ref, branch string) error
	// DropRef removes an ephemeral ref LOCALLY. Best effort.
	DropRef(ref string) error
	// DeleteRemoteRef removes it from the forge (OR-337). Best effort.
	//
	// Separate from DropRef because Test PUSHES these refs -- a split has to
	// reach CI to be tested -- so dropping only the local branch leaves the
	// remote one behind. Twenty-five branches accumulated that way, including
	// pure bisection scratch that nothing would ever read again.
	DeleteRemoteRef(ref string) error

	// SHAOf resolves a ref or branch to a commit. Used to stamp what a
	// result was validated against (ADR 0017), so a merge can refuse a base
	// that has moved since.
	SHAOf(ref string) (string, error)

	// LandRef merges the tested ref into base and reports the commit base
	// ended up at.
	//
	// The one irreversible operation in a batch, and the whole point of it:
	// the thing that was TESTED is the thing that MERGES. Landing members
	// individually instead re-introduces the rebase cascade the batch exists
	// to remove (OR-253), because each merge leaves the rest behind base.
	LandRef(ref, base string) (string, error)
}

// ErrCheckPending means the checks have not reported yet.
//
// A SENTINEL RATHER THAN A THIRD RETURN VALUE, because every existing caller
// and every fake already handles an error, and a bool that means "ignore the
// other bool" is the shape that gets misread. Callers that do not know about
// it treat a pending build as an error and stop, which is the safe reading:
// they will not merge on it.
var ErrCheckPending = errors.New("the checks have not reported yet")

// Tester runs CI against a ref and reports whether it passed.
//
// Returns ErrCheckPending while a build is still running. It must NOT block
// waiting for one: the watch tick calls this, and a tick that waits is a tick
// that has stopped reporting the other tickets in flight (OR-251).
type Tester interface {
	Test(ref string) (bool, error)
}

// Assemble builds ref from base by merging each member in order, ejecting any
// that conflicts.
//
// Order is the caller's: whatever the queue decided. It matters because the

// Observer is how a batch reports what it is doing, without knowing what
// draws it.
//
// An interface rather than a ui import, because everything else in this file
// is a pure function over a Git and a Tester -- that is what lets the whole
// search be tested with no repository and no terminal. A display dependency
// here would make the cheapest tests in the package need a writer.
//
// Every method is optional in practice: nopObserver implements them all as
// no-ops and is substituted for a nil, so a caller that does not care passes
// nothing and every call site stays unguarded.
type Observer interface {
	Assembling(ref, base string, keys []string)
	Merged(key string)
	Ejected(key, reason string)
	Testing(run int)
	Split(keys []string, green bool, depth, runs int, culprit bool)
	Settled(landed, ejected, culprits, deferred []string)
}

type nopObserver struct{}

func (nopObserver) Assembling(string, string, []string)            {}
func (nopObserver) Merged(string)                                  {}
func (nopObserver) Ejected(string, string)                         {}
func (nopObserver) Testing(int)                                    {}
func (nopObserver) Split([]string, bool, int, int, bool)           {}
func (nopObserver) Settled([]string, []string, []string, []string) {}

// observe substitutes the no-op for a nil, so no call site needs a guard.
func observe(o Observer) Observer {
	if o == nil {
		return nopObserver{}
	}
	return o
}

func keysOf(members []Member) []string {
	out := make([]string, 0, len(members))
	for _, m := range members {
		out = append(out, m.Key)
	}
	return out
}

// first branch in never conflicts, so the order chooses who pays when two
// branches disagree -- and that is the queue's decision to make, not this
// function's.
func Assemble(g Git, ref, base string, members []Member, o Observer) (kept []Member, ejected []MemberResult, err error) {
	ob := observe(o)
	if err := g.CutRef(ref, base); err != nil {
		return nil, nil, fmt.Errorf("cutting %s from %s: %w", ref, base, err)
	}
	for _, m := range members {
		if err := g.MergeInto(ref, m.Branch); err != nil {
			reason := "conflicts with the batch: " + firstLine(err.Error())
			ejected = append(ejected, MemberResult{
				Member: m, Outcome: Ejected, Reason: reason,
			})
			ob.Ejected(m.Key, reason)
			continue
		}
		kept = append(kept, m)
		ob.Merged(m.Key)
	}
	return kept, ejected, nil
}

// Isolate finds the members responsible for a red batch by halving.
//
// Both halves are tested at each split rather than only the failing one,
// because a batch can hold more than one culprit and stopping at the first
// would land the second. That is also why the cost is ~2*log2(N): the naive
// log2(N) figure assumes exactly one bad member and no confirmation of the
// good half.
//
// runs counts every CI run the search consumed, so the caller can report what
// the batch actually cost rather than what the model predicted.
// PRECONDITION: members is already known to be red. Isolate never re-tests
// the set it was handed, it splits immediately -- re-confirming a result the
// caller already has is a whole CI run bought for nothing, and at the top of
// every search it is the single most expensive mistake available here.
func Isolate(t Tester, g Git, refPrefix, base string, members []Member, o Observer) (culprits []Member, runs int, err error) {
	c, r, _, e := isolateProving(t, g, refPrefix, base, members, observe(o), 0, new(int))
	return c, r, e
}

// proven is a ref the search TESTED GREEN, and exactly which members were in
// it.
//
// Kept because the sound remainder of a red batch often is one of these, and
// when it is, landing it costs no run at all: the set was already proved, on
// this same base, moments ago. The alternative is reassembling the same
// members into a new ref and paying CI to learn what is already known.
//
// The ref is NOT dropped while it might still be reused, which is the one
// place the search holds a resource past its own use. Cleanup is the caller's.
type proven struct {
	ref  string
	keys []string
}

// matches reports whether this proven set is exactly want, order-insensitively.
//
// EXACTLY, both directions. A proven superset would land a member the search
// blamed; a proven subset would silently drop sound work. Neither is a
// near-miss worth accepting, so the comparison is equality and nothing else.
func (p proven) matches(want []string) bool {
	if len(p.keys) != len(want) {
		return false
	}
	have := make(map[string]bool, len(p.keys))
	for _, k := range p.keys {
		have[k] = true
	}
	for _, k := range want {
		if !have[k] {
			return false
		}
	}
	return true
}

// isolate carries the depth and a shared run counter so the observer can draw
// the search as the tree it is. depth is the indent; total is shared across
// the whole recursion rather than summed on the way out, because the display
// needs the run number DURING the search, not after it.
// dropScratch removes an ephemeral ref from BOTH the clone and the forge
// (OR-337).
//
// Test pushes every ref it tests -- a split cannot reach CI otherwise -- so
// dropping the local branch alone left the remote one behind for good. The
// isolation refs are the clearest case: orion/batch-iso-2-0 and -2-1 outlived
// the search that created them by days, and nothing will ever read them
// again. Best effort on both, as DropRef always was: a ref that cannot be
// deleted is untidy, never incorrect.
func dropScratch(g Git, ref string) {
	_ = g.DropRef(ref)
	_ = g.DeleteRemoteRef(ref)
}

func isolateProving(t Tester, g Git, refPrefix, base string, members []Member,
	o Observer, depth int, total *int) (culprits []Member, runs int, green []proven, err error) {

	if len(members) <= 1 {
		// Already narrowed, and already known red. Reported as a leaf so the
		// tree names the branch the search settled on.
		if len(members) == 1 {
			o.Split(keysOf(members), false, depth, *total, true)
		}
		return members, 0, nil, nil
	}

	half := len(members) / 2
	for i, side := range [][]Member{members[:half], members[half:]} {
		ref := fmt.Sprintf("%s-%d-%d", refPrefix, len(members), i)
		kept, _, aerr := Assemble(g, ref, base, side, nil)
		if aerr != nil {
			return nil, runs, green, aerr
		}
		ok, terr := t.Test(ref)
		runs++
		*total++
		if terr != nil {
			dropScratch(g, ref)
			return nil, runs, green, terr
		}
		o.Split(keysOf(kept), ok, depth, *total, false)
		if ok {
			// KEPT, not dropped. This ref is a tested-green set on this base,
			// and the sound remainder of the batch is frequently exactly it
			// -- one culprit, cleanly on one side of one split. Reusing it
			// then costs nothing, where reassembling the same members costs a
			// whole CI run to re-learn what was just proved. The caller drops
			// these once it has chosen.
			green = append(green, proven{ref: ref, keys: keysOf(kept)})
			continue // this half is sound; the fault is in the other
		}
		// A red ref is worth nothing to anybody: dropped immediately, as
		// before. Its NAME lives on as the prefix for the next level, which
		// is why the drop is safe here.
		dropScratch(g, ref)
		// Both halves are examined rather than stopping at the first red
		// one: a batch can hold more than one culprit, and stopping early
		// would land the second.
		sub, r, subGreen, serr := isolateProving(t, g, ref, base, kept, o, depth+1, total)
		runs += r
		green = append(green, subGreen...)
		if serr != nil {
			return nil, runs, green, serr
		}
		culprits = append(culprits, sub...)
	}
	return culprits, runs, green, nil
}

// Land runs one full cycle: assemble, test once, and on red isolate the
// culprits and report the rest as deferred.
//
// It does not merge anything. Deciding to act on a green batch is the
// caller's, which keeps this function total and testable, and keeps the
// irreversible step in one place.
// Approver decides whether a green batch may merge.
//
// Asked ONLY after checks pass, never before: a person asked to approve
// something whose tests have not finished learns to approve on the strength
// of being asked, which is the rubber stamp the whole gate exists to prevent.
// approvalFlow states the same rule for the per-branch path.
//
// Returning false is not an error. It means "not yet": the answer has been
// requested and has not arrived, and the batch will be offered again on a
// later pass rather than merging or failing.
type Approver func(ref string, members []string) (bool, error)

// LandOption configures a landing without changing Land's signature for the
// callers and tests that do not care.
type LandOption func(*landOpts)

type landOpts struct {
	approve Approver
	// now is the clock, injectable so a test can assert an elapsed figure
	// without sleeping for it. Defaults to time.Now via landOpts.clock.
	clock func() time.Time
	// isoWait is how long an isolation run waits for its check to report
	// before giving up (OR-321). Zero means an isolation run that has not
	// reported is an error at once -- the behaviour that tore a red batch
	// down and re-assembled it every pass, restarting CI each time.
	isoWait time.Duration
	// sleep is what the wait sleeps with; injectable so the test does not.
	sleep func(time.Duration)
	// knownRed says the whole set's run has already come back red, so the
	// first test is skipped and the search begins at once (OR-324).
	knownRed bool
}

// now reads the clock, defaulting to the real one. A method rather than a
// field set at construction, so every option combination gets it without each
// caller remembering to.
func (o landOpts) now() time.Time {
	if o.clock == nil {
		return time.Now()
	}
	return o.clock()
}

// WithClock replaces the clock. For tests: elapsed is a headline number now,
// so it needs a test, and a test that sleeps to produce one is a test nobody
// runs.
func WithClock(f func() time.Time) LandOption {
	return func(o *landOpts) { o.clock = f }
}

// WithIsolationWait lets an isolation run wait for its check (OR-321).
//
// The first test of a batch returns at once on silence and the next pass
// resumes it, because there is a record to resume from. Isolation has no
// such record: it is a search, and a search interrupted is a search started
// over -- which is what happened: the first split's check had not reported,
// the batch was declared not to have completed, its ref was deleted, and the
// next pass assembled it again and restarted CI. So an isolation run polls,
// bounded by d, and only then gives up.
func WithIsolationWait(d time.Duration) LandOption {
	return func(o *landOpts) { o.isoWait = d }
}

// WithKnownRed starts at isolation: the previous pass already ran the whole
// set and it was red (OR-324). Without it the next pass re-cut the ref, pushed
// fresh merge commits, and waited six minutes to learn what it already knew --
// and, since the record was cleared, did so on every pass, never isolating.
func WithKnownRed() LandOption {
	return func(o *landOpts) { o.knownRed = true }
}

// WithSleeper replaces the wait's sleep. For tests.
func WithSleeper(f func(time.Duration)) LandOption {
	return func(o *landOpts) { o.sleep = f }
}

// isolationPoll is how often a waiting isolation run re-reads its check.
const isolationPoll = 30 * time.Second

// waitingTester re-reads a pending check until it reports or the wait runs
// out. Wraps the batch's tester for isolation only.
type waitingTester struct {
	t  Tester
	lo landOpts
}

func (w waitingTester) Test(ref string) (bool, error) {
	started := w.lo.now()
	for {
		ok, err := w.t.Test(ref)
		if !errors.Is(err, ErrCheckPending) {
			return ok, err
		}
		if waited := w.lo.now().Sub(started); waited >= w.lo.isoWait {
			return false, fmt.Errorf("no check result on %s after waiting %s: %w",
				ref, waited.Round(time.Second), ErrCheckPending)
		}
		sleep := w.lo.sleep
		if sleep == nil {
			sleep = time.Sleep
		}
		sleep(isolationPoll)
	}
}

// WithApproval gates the merge on a human. Absent, a green batch lands as
// soon as it is proved -- which is correct for an unattended pipeline and
// wrong for a repository whose operator wants to look first.
func WithApproval(a Approver) LandOption {
	return func(o *landOpts) { o.approve = a }
}

// NAMED RESULTS, because the elapsed timer is a deferred write.
//
// With unnamed results, `return b, nil` copies b into the result before the
// deferred function runs, so a defer that sets b.Elapsed sets it on a value
// nobody sees. Every caller would read zero, and the test that caught it is
// the only reason this comment exists rather than a silently missing number.
func Land(g Git, t Tester, ref, base string, members []Member, o Observer,
	opts ...LandOption) (out Batch, err error) {

	var lo landOpts
	for _, opt := range opts {
		opt(&lo)
	}
	ob := observe(o)
	b := Batch{Base: base, Ref: ref, approve: lo.approve}

	// Started before assembly, not before testing: merging N branches into the
	// ref is part of what a batch costs, and a timer that skipped it would
	// report a number smaller than the thing being measured. Stopped on every
	// return, including the failures -- a batch that died after twenty minutes
	// cost twenty minutes, and hiding that would only flatter the feature.
	started := lo.now()
	defer func() { out.Elapsed = lo.now().Sub(started) }()
	if len(members) == 0 {
		return b, nil
	}
	ob.Assembling(ref, base, keysOf(members))

	kept, ejected, err := Assemble(g, ref, base, members, ob)
	b.Results = append(b.Results, ejected...)
	if err != nil {
		return b, err
	}
	if len(kept) == 0 {
		return b, nil // everything conflicted; nothing to test
	}

	// The base as it stood when this set was assembled, recorded BEFORE the
	// run rather than read again after it (ADR 0017). What CI proves is "this
	// ref, on top of this base"; a merge into any other base is a result
	// carried somewhere it was never earned.
	baseSHA, err := g.SHAOf(base)
	if err != nil {
		return b, fmt.Errorf("reading %s before testing the batch: %w", base, err)
	}
	b.BaseSHA = baseSHA

	ob.Testing(1)
	var ok bool
	if lo.knownRed {
		// The run was spent by the previous pass; it counts, and its
		// verdict is the one being acted on.
		ok, err = false, nil
	} else {
		ok, err = t.Test(ref)
	}
	if errors.Is(err, ErrCheckPending) {
		// NOT A FAILURE, AND NOT A RUN SPENT. CI is still going; the caller
		// records the batch and comes back on a later tick (OR-251). Counting
		// a run here would inflate the number the whole design is justified
		// by, once per tick, for as long as the build takes.
		b.Pending = true
		return b, nil
	}
	b.Runs++
	if err != nil {
		return b, err
	}
	if ok {
		b.ValidatedSHA, _ = g.SHAOf(ref)
		if err := b.land(g, ref, base, kept, ob); err != nil {
			return b, err
		}
		b.report(ob)
		return b, nil
	}

	// The search waits for its checks where the first test did not: it has
	// no record to resume from, so silence must be waited out rather than
	// treated as a verdict or an error (OR-321).
	var iso Tester = t
	if lo.isoWait > 0 {
		iso = waitingTester{t: t, lo: lo}
	}
	culprits, runs, green, err := isolateProving(iso, g, ref+"-iso", base, kept, ob, 0, new(int))
	b.Runs += runs
	// Whatever happens next, the search's green refs are litter once the
	// choice below is made. Dropped here rather than inside the search, which
	// is what lets one of them be reused.
	defer func() {
		for _, p := range green {
			dropScratch(g, p.ref)
		}
	}()
	if err != nil {
		return b, err
	}
	bad := map[string]bool{}
	for _, c := range culprits {
		bad[c.Key] = true
	}
	var innocent []Member
	for _, m := range kept {
		if bad[m.Key] {
			b.Results = append(b.Results, MemberResult{Member: m, Outcome: Culprit,
				Reason: "isolated as a reason the batch failed"})
			continue
		}
		innocent = append(innocent, m)
	}

	// The innocent members land now rather than waiting for a later batch
	// (OR-253). Deferring them was the old behaviour and it is what made a
	// single bad branch hold four good ones hostage: the culprit goes back to
	// the coding queue for a fix, and the rest are not punished for having
	// been assembled next to it.
	//
	// WHY THIS COSTS ONE MORE RUN, and why that is not the bisection's
	// results being thrown away. The search proves HALVES, not the innocent
	// set: [A,B,C,D] red can split to [A,B] green and [C,D] red, then [C]
	// green and [D] culprit -- so [A,B] and [C] are each proven and [A,B,C]
	// never was. Landing them on the strength of separate proofs would merge
	// [C] onto a base containing [A,B] that it was never tested against,
	// which is the same unvalidated-base merge ADR 0017's SHA check exists to
	// refuse. One confirming run buys the guarantee the whole design rests
	// on, and it is spent only on batches that were already red.
	if len(innocent) > 0 {
		if err := b.landInnocent(g, t, ref, base, innocent, green, ob); err != nil {
			return b, err
		}
	}
	b.report(ob)
	return b, nil
}

// landInnocent lands the members no culprit was found in.
//
// FREE WHEN THE SEARCH ALREADY PROVED THIS EXACT SET, which is the common
// shape: one culprit, cleanly inside one side of one split, leaving the other
// side as a tested-green ref holding precisely the sound remainder. Landing
// that ref costs no CI run, because the run was already spent proving it.
//
// One confirming run otherwise, and the reason is not caution for its own
// sake. Bisection proves HALVES: [A,B] green and [C] green does not prove
// [A,B,C], so merging on those two results would put C on a base containing
// A and B that C was never tested against -- the unvalidated-base merge ADR
// 0016's SHA check exists to refuse. The confirming run buys the guarantee
// the design rests on, and only on batches that were already red.
//
// A failure here is reported against the members rather than returned as the
// batch's error: the culprits are already identified and recorded, and losing
// that finding because the follow-up run failed would send the operator back
// to a bisection that has already been paid for.
func (b *Batch) landInnocent(g Git, t Tester, ref, base string,
	innocent []Member, green []proven, ob Observer) error {

	want := keysOf(innocent)
	for _, p := range green {
		if !p.matches(want) {
			continue
		}
		// Proved, on this base, during the search. Nothing left to learn.
		b.ValidatedSHA, _ = g.SHAOf(p.ref)
		return b.land(g, p.ref, base, innocent, ob)
	}

	clean := ref + "-clean"
	kept, ejected, err := Assemble(g, clean, base, innocent, ob)
	b.Results = append(b.Results, ejected...)
	if err != nil {
		return fmt.Errorf("reassembling the sound members: %w", err)
	}
	if len(kept) == 0 {
		return nil
	}
	defer func() { dropScratch(g, clean) }()

	ob.Testing(b.Runs + 1)
	ok, err := t.Test(clean)
	b.Runs++
	if err != nil {
		return err
	}
	if !ok {
		// The set the search said was sound is not. That is a finding, not a
		// crash: it means the fault is an interaction the halving did not
		// separate, and the members go back rather than landing on a red run.
		for _, m := range kept {
			b.Results = append(b.Results, MemberResult{Member: m, Outcome: Deferred,
				Reason: "sound in isolation, red together; offer it again"})
		}
		return nil
	}
	b.ValidatedSHA, _ = g.SHAOf(clean)
	return b.land(g, clean, base, kept, ob)
}

// land merges the tested ref into base, once, and records every member as
// landed.
//
// THE BASE IS RE-READ AND COMPARED FIRST (ADR 0017). CI proved this ref on
// top of the base recorded before the run; if base has moved since -- which,
// with a single integration worker, means a person pushed directly -- then
// the green result belongs to a tree that no longer exists. Merging anyway is
// how a validated result silently becomes an unvalidated one, and it is
// exactly the race nobody is watching for. Rebuilding costs one run; being
// wrong here costs a broken base nobody can attribute.
//
// Members are marked Landed only AFTER the merge returns. Marking first and
// merging second would report work as landed that a failed merge left behind.
func (b *Batch) land(g Git, ref, base string, kept []Member, ob Observer) error {
	// The gate, after green and before the merge. A "no" here is not a
	// refusal of the work: it means the answer has been asked for and has not
	// come back, so the batch waits and is offered again rather than merging
	// unapproved or being reported as failed.
	if b.approve != nil {
		ok, err := b.approve(ref, keysOf(kept))
		if err != nil {
			return fmt.Errorf("asking for approval to land %s: %w", ref, err)
		}
		if !ok {
			b.AwaitingApproval = true
			for _, m := range kept {
				b.Results = append(b.Results, MemberResult{Member: m, Outcome: Deferred,
					Reason: "green and waiting for approval to merge"})
			}
			return nil
		}
	}
	now, err := g.SHAOf(base)
	if err != nil {
		return fmt.Errorf("re-reading %s before landing the batch: %w", base, err)
	}
	if now != b.BaseSHA {
		return fmt.Errorf(
			"%s moved from %s to %s while the batch was testing, so the green result "+
				"was not proven against what it would merge into; reassemble and test again",
			base, short(b.BaseSHA), short(now))
	}
	landedAt, err := g.LandRef(ref, base)
	if err != nil {
		return fmt.Errorf("landing %s into %s: %w", ref, base, err)
	}
	b.LandedSHA = landedAt
	for _, m := range kept {
		b.Results = append(b.Results, MemberResult{Member: m, Outcome: Landed,
			Reason: "the batch was green and landed as one"})
	}
	return nil
}

// short trims a SHA for a message. Full SHAs in prose are unreadable and the
// first seven identify a commit in every repository this will ever run on.
func short(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

// report tells the observer what became of every member, once, at the end.
func (b Batch) report(o Observer) {
	o.Settled(b.Members(Landed), b.Members(Ejected), b.Members(Culprit), b.Members(Deferred))
}

// Describe is the one-line report for each member, for the watch log.
func (b Batch) Describe() []string {
	out := make([]string, 0, len(b.Results))
	for _, r := range b.Results {
		out = append(out, fmt.Sprintf("%-8s %-9s %s", r.Key, r.Outcome, r.Reason))
	}
	sort.Strings(out)
	return out
}
