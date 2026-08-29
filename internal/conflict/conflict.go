// Package conflict verifies that a merge resolution dropped nothing.
//
// WHY A GREEN BUILD IS NOT ENOUGH, with the evidence that made this a ticket.
// On 2026-08-29 a three-way conflict between OR-171, OR-174 and OR-176 was
// hand-resolved. The first resolution BUILT, VETTED and PASSED the package's
// own tests -- and was still wrong: it had reverted OR-171's actor routing
// back to a hardcoded implementer, and separately violated a property OR-176
// had just introduced. It was caught only because OR-176 happened to have
// added a test that failed.
//
// So the suite passing is a FLOOR, not the proof. The failure mode of a
// resolution is not "it breaks", it is "one side of a hunk quietly vanishes",
// and a build cannot tell a line deliberately removed from one that was lost.
//
// What this package proves mechanically:
//
//   - no conflict markers survived into the tree
//   - no .orig or .rej litter was left behind
//   - no file that BOTH sides changed came out byte-identical to one of them
//
// That last one is the interesting check. If both branches edited a file and
// the resolution matches one of them exactly, the other side's edit is not in
// the result. That is occasionally correct -- one change genuinely superseded
// the other -- but it is indistinguishable from a dropped change, so it is
// reported rather than passed. Nothing here decides; it hands whoever must
// answer for the resolution a specific file and a specific claim to check.
package conflict

import (
	"fmt"
	"os/exec"
	"sort"
	"strings"
)

// Finding is one thing worth looking at, with the reason attached. The reason
// travels with it because "check this file" and no claim to test is a request
// to re-derive the analysis rather than to verify it.
type Finding struct {
	File string
	Why  string
}

// Report is what verification found.
type Report struct {
	Markers        []Finding
	Litter         []Finding
	MatchedOneSide []Finding
}

// Safe reports whether the resolution can be pushed without a person looking.
//
// The conjunction lives here rather than in a separate flag so a check added
// later cannot be forgotten at the call site.
func (r Report) Safe() bool {
	return len(r.Markers) == 0 && len(r.Litter) == 0 && len(r.MatchedOneSide) == 0
}

// Findings is everything, in a stable order for printing.
func (r Report) Findings() []Finding {
	out := append([]Finding{}, r.Markers...)
	out = append(out, r.Litter...)
	out = append(out, r.MatchedOneSide...)
	sort.Slice(out, func(i, j int) bool {
		if out[i].File != out[j].File {
			return out[i].File < out[j].File
		}
		return out[i].Why < out[j].Why
	})
	return out
}

func git(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// lines splits git output, dropping the empty tail an empty result produces.
func lines(s string) []string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

// changedFiles lists paths that differ between two commits.
func changedFiles(dir, from, to string) ([]string, error) {
	out, err := git(dir, "diff", "--name-only", from, to)
	if err != nil {
		return nil, fmt.Errorf("diffing %s..%s: %s", from, to, out)
	}
	return lines(out), nil
}

// blob returns a file's contents at a commit, and whether it existed there.
func blob(dir, commit, path string) (string, bool) {
	out, err := git(dir, "show", commit+":"+path)
	if err != nil {
		return "", false
	}
	return out, true
}
