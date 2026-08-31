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
//
// WHERE A NOTE LIVES DEPENDS ON WHETHER THE VERSION HAS SHIPPED. Collation
// writes the fragments into CHANGELOG.md and DELETES them, because a fragment
// that survives ships twice. So `.changelog.d/` is where an UNRELEASED
// version's notes are, and the one place a RELEASED version's notes are
// guaranteed not to be. Looking only there made every shipped release report
// all of its tickets as undocumented, permanently (OR-211). The question is
// "is this documented", not "is there a file in that directory", so both
// places count.

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

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

	// Collated says the version's notes are already in CHANGELOG.md, so the
	// fragment directory is the wrong place to look for them.
	Collated bool

	// TicketsWithoutFragment is about to ship with no release note, while the
	// version's notes are still uncollated fragments. There is nowhere else
	// the note could be, so this is a finding to act on before the release.
	TicketsWithoutFragment []string

	// TicketsNotNamedInChangelog is done, the version is already collated, and
	// the section does not name the ticket. Weaker evidence than the above:
	// a note that mentions no key reads exactly like this, and OR-105's work
	// shipped inside v0.8.0 folded into another ticket's bullet. Worth a look,
	// never a reason to refuse -- a released version cannot be corrected by
	// blocking it, and a permanent blocker on every past release is how a gate
	// gets bypassed by habit.
	TicketsNotNamedInChangelog []string

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

// Reconcile compares a milestone's tickets against the notes that exist for
// it -- the uncollated fragments on disk, and the collated section already in
// CHANGELOG.md. Either location documents a ticket.
//
// Only tickets that are DONE are expected to have a note. An unfinished
// ticket has not shipped anything yet, so demanding one for it would
// report a problem on every release with work still in progress -- noise that
// trains a reader to skip the report, which is how a real missing note gets
// through.
func Reconcile(version string, frags []Fragment, tickets []Ticket, col Collated) Reconciliation {
	r := Reconciliation{Version: version, Collated: col.Found}

	haveFragment := make(map[string]bool, len(frags))
	for _, f := range frags {
		haveFragment[f.Key] = true
	}
	inVersion := make(map[string]bool, len(tickets))

	for _, t := range tickets {
		inVersion[t.Key] = true
		if !t.Done {
			r.NotDone = append(r.NotDone, t.Key)
			continue
		}
		r.Done = append(r.Done, t.Key)
		switch {
		case haveFragment[t.Key] || col.Names(t.Key):
			// Documented. Which of the two places it is in does not matter.
		case col.Found:
			r.TicketsNotNamedInChangelog = append(r.TicketsNotNamedInChangelog, t.Key)
		default:
			r.TicketsWithoutFragment = append(r.TicketsWithoutFragment, t.Key)
		}
	}

	for _, f := range frags {
		if !inVersion[f.Key] {
			r.FragmentsWithoutTicket = append(r.FragmentsWithoutTicket, f.Key)
		}
	}

	sort.Strings(r.Done)
	sort.Strings(r.NotDone)
	sort.Strings(r.TicketsWithoutFragment)
	sort.Strings(r.TicketsNotNamedInChangelog)
	sort.Strings(r.FragmentsWithoutTicket)
	return r
}

// Collated is what CHANGELOG.md already says about one version: whether it has
// a `## <version>` section at all, and which ticket keys that section names.
//
// Found is the whole mode switch, and it is deliberately read from the file
// rather than from the tracker's released flag: it needs no network call, so
// the check answers the same offline, and it is the state that actually
// matters -- fragments are gone the moment collation runs, whether or not
// anyone has marked the version released yet.
type Collated struct {
	Found bool
	Keys  map[string]bool
}

// Names reports whether the collated section mentions a ticket.
func (c Collated) Names(key string) bool { return c.Keys[strings.ToUpper(strings.TrimSpace(key))] }

var keyInNote = regexp.MustCompile(`\b([A-Z][A-Z0-9]+-[0-9]+)\b`)

// LoadCollated reads CHANGELOG.md and reports what its `## <version>` section
// says.
//
// A missing file or a missing section is not an error: it means the version
// has not been collated, which is the ordinary state of the version being
// prepared. The caller distinguishes the two states by Found.
func LoadCollated(root, version string) (Collated, error) {
	version = strings.TrimSpace(version)
	b, err := os.ReadFile(filepath.Join(root, "CHANGELOG.md"))
	if os.IsNotExist(err) {
		return Collated{}, nil
	}
	if err != nil {
		return Collated{}, err
	}

	var body []string
	in := false
	for _, line := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(line, "## ") {
			if in {
				break
			}
			in = strings.EqualFold(strings.TrimSpace(strings.TrimPrefix(line, "## ")), version)
			continue
		}
		if in {
			body = append(body, line)
		}
	}
	if !in {
		return Collated{}, nil
	}

	// The keys a section happens to name. Notes are written for a reader
	// deciding whether to upgrade, not as an index, so most bullets carry no
	// key at all -- which is exactly why an unnamed ticket is reported as
	// something to look at rather than as a missing note.
	keys := map[string]bool{}
	for _, m := range keyInNote.FindAllStringSubmatch(strings.Join(body, "\n"), -1) {
		keys[m[1]] = true
	}
	return Collated{Found: true, Keys: keys}, nil
}
