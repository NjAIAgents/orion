// Package reconcile compares what the repository says against what the
// tracker says, and reports where they disagree.
//
// The tracker is a record of intent, not evidence of what happened, and the
// two drift apart silently. On 2026-08-30 OR-211 read In Progress for hours
// while its finished work sat committed-but-never-pushed in a local worktree:
// develop never received it, the milestone counted it as included, and
// nothing anywhere noticed. Had a release been cut on the tracker's word, it
// would have shipped a changelog claiming a fix that was not in the binary.
//
// That is the failure this exists to catch, and it is the reason OR-116
// (promote automatically once a milestone is complete) blocks on it: an
// automated promotion is only as trustworthy as the signal it triggers on.
//
// What it deliberately does NOT do: change the tracker. Every finding is
// reported with the evidence that produced it, and acting on one is a
// separate decision. A reconciler that silently rewrites status is a second
// source of drift rather than a cure for the first.
package reconcile

import (
	"fmt"
	"os/exec"
	"regexp"
	"sort"
	"strings"
)

// Kind is what sort of disagreement was found.
type Kind string

const (
	// MissingFromBranch is the OR-211 case and the dangerous one: the tracker
	// says this ticket is finished, and the integration branch has no commit
	// for it. The work is somewhere else, or nowhere.
	MissingFromBranch Kind = "tracker-says-done-but-not-merged"

	// OpenButMerged is the mirror: the code landed and nobody moved the
	// ticket. Harmless to the binary, corrosive to the tracker -- it is how a
	// board stops being worth reading.
	OpenButMerged Kind = "merged-but-tracker-still-open"

	// Unversioned is work nobody scheduled. It cannot appear in any release's
	// notes because it belongs to no release.
	Unversioned Kind = "done-but-no-milestone"
)

// Finding is one disagreement, with the evidence for it.
//
// Evidence rather than a score: the operator has to be able to check the
// claim without re-deriving it, and an assertion nobody can verify is how a
// warning channel stops being read (OR-238).
type Finding struct {
	Key      string
	Kind     Kind
	Status   string // what the tracker says
	Evidence string // what the repository says
}

func (f Finding) String() string {
	return fmt.Sprintf("%-8s %-32s tracker=%-12s %s", f.Key, f.Kind, f.Status, f.Evidence)
}

// Ticket is the tracker's view, reduced to what this comparison needs.
type Ticket struct {
	Key         string
	Status      string
	Done        bool
	FixVersions []string
}

// Report is every disagreement found, in key order.
type Report struct {
	Base     string // the branch compared against, e.g. "develop"
	Findings []Finding
	// Checked is how many tickets were compared, so "no findings" can be
	// told apart from "nothing was looked at" -- the distinction OR-238's
	// false-clean reports turned on.
	Checked int
}

// Clean reports whether the tracker and the repository agree.
func (r Report) Clean() bool { return len(r.Findings) == 0 }

// Of returns only the findings of one kind.
func (r Report) Of(k Kind) []Finding {
	var out []Finding
	for _, f := range r.Findings {
		if f.Kind == k {
			out = append(out, f)
		}
	}
	return out
}

// Compare is the whole comparison, and it is pure: callers supply the
// tracker's view and the set of keys the branch carries, so the decision
// logic is testable without a repository or a Jira.
func Compare(base string, tickets []Ticket, landed map[string]bool) Report {
	rep := Report{Base: base, Checked: len(tickets)}
	for _, t := range tickets {
		key := strings.ToUpper(strings.TrimSpace(t.Key))
		if key == "" {
			continue
		}
		switch {
		case t.Done && !landed[key]:
			rep.Findings = append(rep.Findings, Finding{
				Key: key, Kind: MissingFromBranch, Status: t.Status,
				Evidence: "no commit naming it on " + base,
			})
		case !t.Done && landed[key]:
			rep.Findings = append(rep.Findings, Finding{
				Key: key, Kind: OpenButMerged, Status: t.Status,
				Evidence: "a commit naming it is already on " + base,
			})
		}
		if t.Done && len(t.FixVersions) == 0 {
			rep.Findings = append(rep.Findings, Finding{
				Key: key, Kind: Unversioned, Status: t.Status,
				Evidence: "finished, but on no milestone, so it appears in no release",
			})
		}
	}
	sort.Slice(rep.Findings, func(i, j int) bool {
		if rep.Findings[i].Key != rep.Findings[j].Key {
			return rep.Findings[i].Key < rep.Findings[j].Key
		}
		return rep.Findings[i].Kind < rep.Findings[j].Kind
	})
	return rep
}

// Both spellings a key reaches a commit by: written into the message, and
// carried by the branch name Orion cut for it. The second matters because a
// merge commit names the branch whether or not anyone wrote the key.
var (
	keyInText   = regexp.MustCompile(`\b([A-Z][A-Z0-9]+-[0-9]+)\b`)
	keyInBranch = regexp.MustCompile(`(?i)\b[a-z]*/?([a-z][a-z0-9]+-[0-9]+)\b`)
)

// Landed returns the ticket keys the base branch carries.
//
// The whole history of the branch rather than a range: the question is "is
// this ticket's work here", which a range since the last tag cannot answer
// for a ticket that landed two releases ago and was reopened.
func Landed(root, base string) (map[string]bool, error) {
	out, err := git(root, "log", "--format=%s%n%b", base)
	if err != nil {
		return nil, fmt.Errorf("reading the history of %s: %w", base, err)
	}
	seen := map[string]bool{}
	for _, m := range keyInText.FindAllStringSubmatch(out, -1) {
		seen[strings.ToUpper(m[1])] = true
	}
	for _, m := range keyInBranch.FindAllStringSubmatch(out, -1) {
		seen[strings.ToUpper(m[1])] = true
	}
	return seen, nil
}

func git(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	b, err := cmd.Output()
	return strings.TrimSpace(string(b)), err
}
