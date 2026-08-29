package changelog

import (
	"reflect"
	"testing"
)

func frags(keys ...string) []Fragment {
	out := make([]Fragment, 0, len(keys))
	for _, k := range keys {
		out = append(out, Fragment{Key: k})
	}
	return out
}

// The direction that matters most: a change shipped and its release note is
// missing. Nothing else in the pipeline notices this -- the v0.7.10 notes
// were hand-matched, and a missing fragment would simply have been absent.
func TestDoneTicketWithNoFragmentIsReported(t *testing.T) {
	r := Reconcile("v0.9.0",
		frags("OR-1"),
		[]Ticket{{Key: "OR-1", Done: true}, {Key: "OR-2", Done: true}})

	if got := r.TicketsWithoutFragment; !reflect.DeepEqual(got, []string{"OR-2"}) {
		t.Errorf("TicketsWithoutFragment = %v, want [OR-2]; a shipped change would "+
			"go unmentioned in the release notes", got)
	}
	if r.Clean() {
		t.Error("reported clean while a shipped ticket had no release note")
	}
}

// The other direction: a note for something that did not ship. Reported
// rather than deleted, because a fragment staged early for the NEXT release
// is legitimate and destroying it would be the worse error.
func TestFragmentWithNoTicketInVersionIsReported(t *testing.T) {
	r := Reconcile("v0.9.0",
		frags("OR-1", "OR-99"),
		[]Ticket{{Key: "OR-1", Done: true}})

	if got := r.FragmentsWithoutTicket; !reflect.DeepEqual(got, []string{"OR-99"}) {
		t.Errorf("FragmentsWithoutTicket = %v, want [OR-99]", got)
	}
}

// An unfinished ticket has shipped nothing, so demanding a note for it would
// fire on every release with work in progress. That noise is how a real
// missing note gets waved through.
func TestUnfinishedTicketIsNotAskedForAFragment(t *testing.T) {
	r := Reconcile("v0.9.0", nil, []Ticket{{Key: "OR-1", Done: false}})

	if len(r.TicketsWithoutFragment) != 0 {
		t.Errorf("asked for a release note from unfinished work: %v", r.TicketsWithoutFragment)
	}
	if !reflect.DeepEqual(r.NotDone, []string{"OR-1"}) {
		t.Errorf("NotDone = %v, want [OR-1]", r.NotDone)
	}
}

// Incomplete and unclean are different questions, and conflating them is how
// one stuck ticket ends up holding a release hostage. A milestone with
// unfinished work is still releasable: ship what is done, roll the rest.
func TestIncompleteIsNotTheSameAsUnclean(t *testing.T) {
	r := Reconcile("v0.9.0",
		frags("OR-1"),
		[]Ticket{{Key: "OR-1", Done: true}, {Key: "OR-2", Done: false}})

	if r.Complete() {
		t.Error("called the milestone complete with an unfinished ticket in it")
	}
	if !r.Clean() {
		t.Errorf("called it unclean because of an UNFINISHED ticket; that would block "+
			"a release for work that never claimed to have shipped: %+v", r)
	}
}

func TestEverythingAgreeing(t *testing.T) {
	r := Reconcile("v0.9.0",
		frags("OR-1", "OR-2"),
		[]Ticket{{Key: "OR-1", Done: true}, {Key: "OR-2", Done: true}})

	if !r.Clean() || !r.Complete() {
		t.Errorf("a matching milestone was not clean and complete: %+v", r)
	}
	if !reflect.DeepEqual(r.Done, []string{"OR-1", "OR-2"}) {
		t.Errorf("Done = %v", r.Done)
	}
}
