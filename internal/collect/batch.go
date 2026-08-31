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
	"fmt"
	"sort"
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
	// DropRef removes an ephemeral ref. Best effort.
	DropRef(ref string) error
}

// Tester runs CI against a ref and reports whether it passed.
type Tester interface {
	Test(ref string) (bool, error)
}

// Assemble builds ref from base by merging each member in order, ejecting any
// that conflicts.
//
// Order is the caller's: whatever the queue decided. It matters because the
// first branch in never conflicts, so the order chooses who pays when two
// branches disagree -- and that is the queue's decision to make, not this
// function's.
func Assemble(g Git, ref, base string, members []Member) (kept []Member, ejected []MemberResult, err error) {
	if err := g.CutRef(ref, base); err != nil {
		return nil, nil, fmt.Errorf("cutting %s from %s: %w", ref, base, err)
	}
	for _, m := range members {
		if err := g.MergeInto(ref, m.Branch); err != nil {
			ejected = append(ejected, MemberResult{
				Member: m, Outcome: Ejected,
				Reason: "conflicts with the batch: " + firstLine(err.Error()),
			})
			continue
		}
		kept = append(kept, m)
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
func Isolate(t Tester, g Git, refPrefix, base string, members []Member) (culprits []Member, runs int, err error) {
	if len(members) <= 1 {
		return members, 0, nil // already narrowed, and already known red
	}

	half := len(members) / 2
	for i, side := range [][]Member{members[:half], members[half:]} {
		ref := fmt.Sprintf("%s-%d-%d", refPrefix, len(members), i)
		kept, _, aerr := Assemble(g, ref, base, side)
		if aerr != nil {
			return nil, runs, aerr
		}
		ok, terr := t.Test(ref)
		runs++
		_ = g.DropRef(ref)
		if terr != nil {
			return nil, runs, terr
		}
		if ok {
			continue // this half is sound; the fault is in the other
		}
		// Both halves are examined rather than stopping at the first red
		// one: a batch can hold more than one culprit, and stopping early
		// would land the second.
		sub, r, serr := Isolate(t, g, ref, base, kept)
		runs += r
		if serr != nil {
			return nil, runs, serr
		}
		culprits = append(culprits, sub...)
	}
	return culprits, runs, nil
}

// Land runs one full cycle: assemble, test once, and on red isolate the
// culprits and report the rest as deferred.
//
// It does not merge anything. Deciding to act on a green batch is the
// caller's, which keeps this function total and testable, and keeps the
// irreversible step in one place.
func Land(g Git, t Tester, ref, base string, members []Member) (Batch, error) {
	b := Batch{Base: base, Ref: ref}
	if len(members) == 0 {
		return b, nil
	}

	kept, ejected, err := Assemble(g, ref, base, members)
	b.Results = append(b.Results, ejected...)
	if err != nil {
		return b, err
	}
	if len(kept) == 0 {
		return b, nil // everything conflicted; nothing to test
	}

	ok, err := t.Test(ref)
	b.Runs++
	if err != nil {
		return b, err
	}
	if ok {
		for _, m := range kept {
			b.Results = append(b.Results, MemberResult{Member: m, Outcome: Landed,
				Reason: "the batch was green"})
		}
		return b, nil
	}

	culprits, runs, err := Isolate(t, g, ref+"-iso", base, kept)
	b.Runs += runs
	if err != nil {
		return b, err
	}
	bad := map[string]bool{}
	for _, c := range culprits {
		bad[c.Key] = true
	}
	for _, m := range kept {
		if bad[m.Key] {
			b.Results = append(b.Results, MemberResult{Member: m, Outcome: Culprit,
				Reason: "isolated as a reason the batch failed"})
			continue
		}
		b.Results = append(b.Results, MemberResult{Member: m, Outcome: Deferred,
			Reason: "sound, but batched with a failure; offer it again"})
	}
	return b, nil
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
