// Package promote decides whether a milestone is safe to release.
//
// "Thorough verification" is a word rather than a gate unless the checks are
// named, so they are named here and each exists because of a specific way a
// release has gone wrong or could:
//
//  1. Every ticket in the version is Done.
//  2. Fragments and the version reconcile in both directions (OR-187).
//  3. The integration branch is green ON THE EXACT SHA being promoted.
//  4. No open pull requests target the integration branch.
//  5. Every commit in the promotion range is attributable to some ticket.
//
// CHECK 5 WILL FIRE ON REAL WORK, and that is intended rather than a defect
// to tune away. On 2026-08-29 a workflow fix and two changelog assemblies
// were pushed by hand, outside any ticket. An unattributed commit is a thing
// to LOOK AT, not necessarily a thing to stop for.
//
// The split between blocking and warning is the whole design. A gate that
// refuses for everything trains an operator to bypass it, and a gate that
// warns about everything is not a gate. So: anything that would publish
// something WRONG blocks; anything merely worth knowing warns.
package promote

import (
	"fmt"
	"sort"
	"strings"
)

// Check is one verification result.
type Check struct {
	Name string
	// Blocking means the release must not proceed. Reserved for evidence
	// that the release would publish something incorrect.
	Blocking bool
	Detail   string
}

// Verdict is every check that had something to say. No checks is clean.
type Verdict struct {
	Version string
	Checks  []Check
}

// Blocking reports whether anything refuses the release.
func (v Verdict) Blocking() bool {
	for _, c := range v.Checks {
		if c.Blocking {
			return true
		}
	}
	return false
}

// Blockers are the checks that stop the release.
func (v Verdict) Blockers() []Check { return v.filter(true) }

// Warnings are worth reading but do not stop the release.
func (v Verdict) Warnings() []Check { return v.filter(false) }

func (v Verdict) filter(blocking bool) []Check {
	var out []Check
	for _, c := range v.Checks {
		if c.Blocking == blocking {
			out = append(out, c)
		}
	}
	return out
}

// Inputs is everything the decision needs, gathered by the caller.
//
// Gathering is separate from deciding so the decision is testable without a
// git repository, a Jira and a forge -- which is what makes it possible to
// assert the blocking/warning split at all.
type Inputs struct {
	Version string

	NotDone                []string
	TicketsWithoutFragment []string
	FragmentsWithoutTicket []string

	// TicketsNotNamedInChangelog is done work the already-collated section of
	// CHANGELOG.md does not mention by key. Kept separate from
	// TicketsWithoutFragment because it is weaker evidence and gets a weaker
	// answer (OR-211).
	TicketsNotNamedInChangelog []string

	// HeadSHA is the commit being promoted; BuildSHA is the commit the build
	// actually ran on, and BuildState is its conclusion as the forge gave it.
	HeadSHA    string
	BuildSHA   string
	BuildState string

	OpenPullRequests    []string
	UnattributedCommits []string
}

// Verify applies the five checks.
func Verify(in Inputs) Verdict {
	v := Verdict{Version: in.Version}

	// 1. Unfinished work. NOT blocking, deliberately: releasing what is done
	// and rolling the rest forward is standard, and one stuck ticket holding
	// the tag hostage is worse than a smaller release.
	if len(in.NotDone) > 0 {
		v.Checks = append(v.Checks, Check{
			Name:   "milestone complete",
			Detail: fmt.Sprintf("%d ticket(s) not done, will roll forward: %s", len(in.NotDone), list(in.NotDone)),
		})
	}

	// 2. A shipped change with no note BLOCKS. Notes that under-report what
	// shipped are wrong, and silently so: nobody reading them can tell.
	if len(in.TicketsWithoutFragment) > 0 {
		v.Checks = append(v.Checks, Check{
			Name:     "every shipped ticket has a release note",
			Blocking: true,
			Detail:   fmt.Sprintf("done with no changelog fragment: %s", list(in.TicketsWithoutFragment)),
		})
	}
	// Once the version is collated the fragments are gone by design, so the
	// same absence proves much less: the note is in CHANGELOG.md, and whether
	// it names the ticket depends on how it was written. OR-105's work shipped
	// inside v0.8.0 folded into another ticket's bullet. Blocking on that would
	// make every past release permanently unpromotable, which is the opposite
	// of what this check exists to say (OR-211).
	if len(in.TicketsNotNamedInChangelog) > 0 {
		v.Checks = append(v.Checks, Check{
			Name: "every shipped ticket is named in the release notes",
			Detail: fmt.Sprintf("%s already has a CHANGELOG.md section, and it does not name: %s",
				in.Version, list(in.TicketsNotNamedInChangelog)),
		})
	}

	// The other direction warns: a fragment staged early for the next release
	// is legitimate, and deleting it would be the worse error.
	if len(in.FragmentsWithoutTicket) > 0 {
		v.Checks = append(v.Checks, Check{
			Name:   "every release note has a ticket",
			Detail: fmt.Sprintf("fragments not in %s: %s", in.Version, list(in.FragmentsWithoutTicket)),
		})
	}

	// 3. Green on the exact SHA. The two failures are reported separately
	// because they mean different things: a failing build is a broken tree,
	// while a build for a DIFFERENT SHA means the verdict being trusted was
	// produced by code other than the code about to ship.
	switch {
	case in.BuildState == "":
		v.Checks = append(v.Checks, Check{
			Name: "integration branch is green", Blocking: true,
			Detail: "no build result found for " + short(in.HeadSHA),
		})
	case !strings.EqualFold(in.BuildState, "success"):
		v.Checks = append(v.Checks, Check{
			Name: "integration branch is green", Blocking: true,
			Detail: fmt.Sprintf("build on %s reported %s", short(in.BuildSHA), in.BuildState),
		})
	case in.BuildSHA != in.HeadSHA:
		v.Checks = append(v.Checks, Check{
			Name: "the green build is for the commit being promoted", Blocking: true,
			Detail: fmt.Sprintf("build ran on %s, promoting %s", short(in.BuildSHA), short(in.HeadSHA)),
		})
	}

	// 4. Racing work. A tag cut while a pull request is one click from
	// landing produces a release missing it, and the fix is to wait.
	if len(in.OpenPullRequests) > 0 {
		v.Checks = append(v.Checks, Check{
			Name: "nothing is about to land", Blocking: true,
			Detail: fmt.Sprintf("%d open pull request(s) target the integration branch: %s",
				len(in.OpenPullRequests), list(in.OpenPullRequests)),
		})
	}

	// 5. Attribution warns by design: hand-pushed docs and changelog assembly
	// are normal, and blocking on them makes the gate a nuisance that gets
	// bypassed, which costs more than it saves.
	//
	// The question is whether a commit names A ticket, not whether it names one
	// of THIS version's (OR-238). Work keyed to an earlier milestone is
	// attributed work; reporting it made the warning fire on every promotion,
	// which is how a channel stops being read.
	if len(in.UnattributedCommits) > 0 {
		v.Checks = append(v.Checks, Check{
			Name: "every commit belongs to a ticket",
			Detail: fmt.Sprintf("%d commit(s) in the promotion range carry no ticket key: %s",
				len(in.UnattributedCommits), list(in.UnattributedCommits)),
		})
	}

	return v
}

func list(items []string) string {
	s := append([]string{}, items...)
	sort.Strings(s)
	if len(s) > 6 {
		return strings.Join(s[:6], ", ") + fmt.Sprintf(" and %d more", len(s)-6)
	}
	return strings.Join(s, ", ")
}

func short(sha string) string {
	if sha == "" {
		return "(unknown)"
	}
	if len(sha) > 8 {
		return sha[:8]
	}
	return sha
}
