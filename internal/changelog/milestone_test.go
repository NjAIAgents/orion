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
		[]Ticket{{Key: "OR-1", Done: true}, {Key: "OR-2", Done: true}},
		Collated{})

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
		[]Ticket{{Key: "OR-1", Done: true}}, Collated{})

	if got := r.FragmentsWithoutTicket; !reflect.DeepEqual(got, []string{"OR-99"}) {
		t.Errorf("FragmentsWithoutTicket = %v, want [OR-99]", got)
	}
}

// An unfinished ticket has shipped nothing, so demanding a note for it would
// fire on every release with work in progress. That noise is how a real
// missing note gets waved through.
func TestUnfinishedTicketIsNotAskedForAFragment(t *testing.T) {
	r := Reconcile("v0.9.0", nil, []Ticket{{Key: "OR-1", Done: false}}, Collated{})

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
		[]Ticket{{Key: "OR-1", Done: true}, {Key: "OR-2", Done: false}},
		Collated{})

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
		[]Ticket{{Key: "OR-1", Done: true}, {Key: "OR-2", Done: true}},
		Collated{})

	if !r.Clean() || !r.Complete() {
		t.Errorf("a matching milestone was not clean and complete: %+v", r)
	}
	if !reflect.DeepEqual(r.Done, []string{"OR-1", "OR-2"}) {
		t.Errorf("Done = %v", r.Done)
	}
}

// Collation writes the fragments into CHANGELOG.md and DELETES them, so after
// a release the fragment directory is the one place the notes are guaranteed
// not to be. Looking only there reported every ticket in every shipped release
// as undocumented, which is the opposite of the truth (OR-211).
func TestCollatedVersionFindsItsNotesInTheChangelog(t *testing.T) {
	root := repo(t, `# Changelog

## Unreleased

## v0.8.1

### Added

- The QA stage derives its test cases before the verification run (OR-182).
- Stage boundaries now print their own line (OR-189).

## v0.8.0

### Added

- Something older (OR-172).
`, nil)

	col, err := LoadCollated(root, "v0.8.1")
	if err != nil {
		t.Fatal(err)
	}
	r := Reconcile("v0.8.1", nil, []Ticket{
		{Key: "OR-182", Done: true},
		{Key: "OR-189", Done: true},
	}, col)

	if len(r.TicketsWithoutFragment) != 0 {
		t.Errorf("demanded a fragment for a ticket already documented in CHANGELOG.md: %v; "+
			"a shipped release would report every one of its tickets as undocumented",
			r.TicketsWithoutFragment)
	}
	if len(r.TicketsNotNamedInChangelog) != 0 {
		t.Errorf("TicketsNotNamedInChangelog = %v, want none", r.TicketsNotNamedInChangelog)
	}
	if !r.Clean() {
		t.Errorf("a fully documented shipped release was not clean: %+v", r)
	}
}

// A section belongs to one version. Reading past its end would let v0.8.0's
// keys vouch for v0.8.1's tickets, which is a check that passes for the wrong
// reason.
func TestCollatedSectionStopsAtTheNextVersion(t *testing.T) {
	root := repo(t, `# Changelog

## v0.8.1

- This one (OR-182).

## v0.8.0

- The one before (OR-172).
`, nil)

	col, err := LoadCollated(root, "v0.8.1")
	if err != nil {
		t.Fatal(err)
	}
	if !col.Names("OR-182") {
		t.Error("did not find the key in the version's own section")
	}
	if col.Names("OR-172") {
		t.Error("read past the section end; the previous release's keys vouched for this one")
	}
}

// The mode switch is a file read, never a tracker call: the check answers the
// same offline, and the fragments are gone the moment collation runs whether
// or not anyone has marked the version released yet.
func TestUncollatedVersionIsRecognisedFromTheFileAlone(t *testing.T) {
	root := repo(t, `# Changelog

## Unreleased

## v0.8.1

- Already shipped (OR-182).
`, nil)

	col, err := LoadCollated(root, "v0.9.0")
	if err != nil {
		t.Fatal(err)
	}
	if col.Found {
		t.Fatal("called v0.9.0 collated when CHANGELOG.md has no such section")
	}

	// So the pre-release behaviour is exactly as before: no fragment, and
	// nowhere else the note could be, is a finding to act on.
	r := Reconcile("v0.9.0", nil, []Ticket{{Key: "OR-211", Done: true}}, col)
	if !reflect.DeepEqual(r.TicketsWithoutFragment, []string{"OR-211"}) {
		t.Errorf("TicketsWithoutFragment = %v, want [OR-211]; an unreleased version still "+
			"expects its fragments", r.TicketsWithoutFragment)
	}
	if r.Clean() {
		t.Error("reported clean while an unshipped change had no release note anywhere")
	}
}

// The case worth catching survives: done, and named in neither place. It is
// still reported -- but as the weaker finding, because a note whose bullet
// mentions no key reads identically, and OR-105's work shipped inside v0.8.0
// folded into another ticket's bullet. A released version cannot be corrected
// by refusing it.
func TestDoneTicketInNeitherPlaceIsReportedButDoesNotBlock(t *testing.T) {
	root := repo(t, `# Changelog

## v0.8.1

- One entry, naming nobody.
`, nil)

	col, err := LoadCollated(root, "v0.8.1")
	if err != nil {
		t.Fatal(err)
	}
	r := Reconcile("v0.8.1", nil, []Ticket{{Key: "OR-105", Done: true}}, col)

	if !reflect.DeepEqual(r.TicketsNotNamedInChangelog, []string{"OR-105"}) {
		t.Errorf("TicketsNotNamedInChangelog = %v, want [OR-105]; the case worth catching "+
			"stopped being reported at all", r.TicketsNotNamedInChangelog)
	}
	if len(r.TicketsWithoutFragment) != 0 {
		t.Errorf("a collated version demanded a fragment: %v; that is a permanent block on "+
			"every past release", r.TicketsWithoutFragment)
	}
	if !r.Clean() {
		t.Error("a shipped release stayed permanently unclean over a note that may be folded " +
			"into another entry")
	}
}

// A leftover fragment still counts. Either location documents the ticket; the
// question is whether the change is written down, not which file holds it.
func TestFragmentStillCountsAfterTheVersionIsCollated(t *testing.T) {
	root := repo(t, "# Changelog\n\n## v0.8.1\n\n- Nothing by key.\n", nil)
	col, err := LoadCollated(root, "v0.8.1")
	if err != nil {
		t.Fatal(err)
	}

	r := Reconcile("v0.8.1", frags("OR-190"), []Ticket{{Key: "OR-190", Done: true}}, col)
	if len(r.TicketsNotNamedInChangelog)+len(r.TicketsWithoutFragment) != 0 {
		t.Errorf("a ticket with an uncollated fragment was reported undocumented: %+v", r)
	}
}

// Against this repository's own CHANGELOG.md, not a fixture. v0.8.1 is tagged,
// published to three channels and marked released; `release verify v0.8.1`
// reported it unsafe to promote, and would have gone on doing so forever. The
// section is historical, so this stays a fixed target.
func TestThisRepositorysShippedReleaseIsNotBlocked(t *testing.T) {
	col, err := LoadCollated("../..", "v0.8.1")
	if err != nil {
		t.Fatal(err)
	}
	if !col.Found {
		t.Fatal("did not find the v0.8.1 section in this repository's CHANGELOG.md")
	}

	r := Reconcile("v0.8.1", nil, []Ticket{
		{Key: "OR-182", Done: true}, // named in the section
		{Key: "OR-189", Done: true}, // shipped inside another entry, named nowhere
	}, col)

	if len(r.TicketsWithoutFragment) != 0 {
		t.Errorf("a released version demanded fragments that collation deleted: %v",
			r.TicketsWithoutFragment)
	}
	if !r.Clean() {
		t.Errorf("a tagged, published, released version reported as unsafe to promote: %+v", r)
	}
	if !reflect.DeepEqual(r.TicketsNotNamedInChangelog, []string{"OR-189"}) {
		t.Errorf("TicketsNotNamedInChangelog = %v, want [OR-189]; a ticket documented "+
			"nowhere must still be visible", r.TicketsNotNamedInChangelog)
	}
}
