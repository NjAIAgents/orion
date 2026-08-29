package changelog

// Reconciling a release milestone against the changelog fragments.
//
// Before this, a release was "tag whatever is on develop, then work out what
// went in". The v0.7.10 notes were assembled by hand-matching nine fragments
// to nine tickets; a missing fragment would have gone unnoticed and the
// release would have silently under-reported what shipped.
//
// Fragments stay the MECHANISM -- OR-113 introduced .changelog.d/ so two
// tickets never edit CHANGELOG.md and conflict, and that reason still holds.
// The version is the GROUPING. This reconciles the two, in BOTH directions,
// because each direction catches a different mistake:
//
//   - a ticket with no fragment is a change that shipped with no release note
//   - a fragment with no ticket is a note for something that did not ship
//
// Mismatches are REPORTED, never resolved. Resolving would mean either
// inventing a note or discarding one, and both are worse than telling a
// person what does not line up.

import "sort"

// Ticket is the minimum this package needs to know about a tracker issue.
//
// Deliberately not tracker.Issue: reconciliation is a pure comparison of two
// sets of keys, and importing the tracker to do it would make this untestable
// without a Jira, for no gain.
type Ticket struct {
	Key  string
	Done bool
}

// Reconciliation is what a release check found. Every field is a list of
// things to LOOK AT; none of them is automatically fatal.
type Reconciliation struct {
	Version string

	// Done and NotDone partition the version's tickets.
	Done    []string
	NotDone []string

	// TicketsWithoutFragment shipped, or is about to, with no release note.
	TicketsWithoutFragment []string

	// FragmentsWithoutTicket describes something not in this milestone. Often
	// legitimate -- a fragment for the NEXT release sitting in the directory
	// early -- which is exactly why it is reported rather than deleted.
	FragmentsWithoutTicket []string
}

// Complete reports whether every ticket in the milestone is finished.
//
// Separate from Clean on purpose. A version that has not completed MUST NOT
// block a release: the right behaviour is to ship what is done and roll the
// unfinished forward, because one stuck ticket holding the tag hostage is
// worse than a smaller release.
func (r Reconciliation) Complete() bool { return len(r.NotDone) == 0 }

// Clean reports whether the fragments and the milestone agree.
func (r Reconciliation) Clean() bool {
	return len(r.TicketsWithoutFragment) == 0 && len(r.FragmentsWithoutTicket) == 0
}

// Reconcile compares a milestone's tickets against the fragments on disk.
//
// Only tickets that are DONE are expected to have a fragment. An unfinished
// ticket has not shipped anything yet, so demanding a note for it would
// report a problem on every release with work still in progress -- noise that
// trains a reader to skip the report, which is how a real missing note gets
// through.
func Reconcile(version string, frags []Fragment, tickets []Ticket) Reconciliation {
	r := Reconciliation{Version: version}

	haveFragment := make(map[string]bool, len(frags))
	for _, f := range frags {
		haveFragment[f.Key] = true
	}
	inVersion := make(map[string]bool, len(tickets))

	for _, t := range tickets {
		inVersion[t.Key] = true
		if t.Done {
			r.Done = append(r.Done, t.Key)
			if !haveFragment[t.Key] {
				r.TicketsWithoutFragment = append(r.TicketsWithoutFragment, t.Key)
			}
			continue
		}
		r.NotDone = append(r.NotDone, t.Key)
	}

	for _, f := range frags {
		if !inVersion[f.Key] {
			r.FragmentsWithoutTicket = append(r.FragmentsWithoutTicket, f.Key)
		}
	}

	sort.Strings(r.Done)
	sort.Strings(r.NotDone)
	sort.Strings(r.TicketsWithoutFragment)
	sort.Strings(r.FragmentsWithoutTicket)
	return r
}
