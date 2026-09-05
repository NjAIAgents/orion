package tracker

// Reading the declared scope back off a ticket (OR-260).
//
// Planning writes down the packages, directories or files a ticket is expected
// to touch, while the shape of the work is still in view; this is the other
// half, and it is deliberately the dullest possible contract: one line in the
// description that starts with `Files:` and lists paths separated by commas.
//
//	Files: internal/watch/watch.go, internal/queue
//
// A LINE IN THE DESCRIPTION rather than a label or a custom field, for two
// reasons. `orion decompose` already writes exactly this line on every task it
// creates (internal/decompose/tasks.go), so the convention exists and has
// output in the tracker already -- inventing a second one would leave two
// places to look and one of them stale. And a description survives every
// tracker this repo might grow a backend for, while a custom field is a
// migration per tracker and a label per path is unreadable on a board.
//
// EVERY MATCHING LINE, unioned. An epic body that quotes its children, or a
// description edited to add a second line, both mean "and also these" -- and
// taking only the first would silently narrow a scope, which is the direction
// that produces a collision nobody predicted.

import (
	"regexp"
	"strings"
)

// filesLine matches the declared-scope line, allowing the markdown decoration
// a description picks up in passing: a list bullet, bold, a leading `#`
// heading marker. Anchored at the start of the line, so a sentence that merely
// contains the word is not a declaration.
// A list bullet needs whitespace after it, so the first `*` of a bold `**` is
// not eaten as one; the three optional `**` runs cover `**Files:**`,
// `**Files**:` and the plain line.
var filesLine = regexp.MustCompile(`(?i)^\s*(?:[-*+]\s+)?\*{0,2}\s*files\s*\*{0,2}\s*:\s*\*{0,2}\s*(.*)$`)

// DeclaredScope is the paths this ticket's description declares it expects to
// touch, in the order they were written, or nothing when it declares none.
//
// ABSENCE IS THE NORMAL CASE and it means unknown, never "touches nothing".
// Every caller treats an empty result as "no prediction to judge": the queue
// admits, `pick` falls back to its area heuristic. A ticket cannot be held
// back for failing to say (OR-243, OR-95).
func (i Issue) DeclaredScope() []string { return DeclaredScope(i.Description) }

// DeclaredScope reads the declaration out of arbitrary text, so a caller
// holding a body it has not made an Issue of can ask the same question of it.
func DeclaredScope(text string) []string {
	var out []string
	seen := map[string]bool{}
	for _, line := range strings.Split(text, "\n") {
		m := filesLine.FindStringSubmatch(strings.TrimRight(line, " \t\r"))
		if m == nil {
			continue
		}
		for _, p := range strings.Split(m[1], ",") {
			p = strings.TrimSpace(strings.Trim(strings.TrimSpace(p), "`\"'"))
			// A path with a space in it is prose that landed on the wrong
			// line -- "the whole of the fix loop" -- and a wrong path is worse
			// than a missing one here: it is read as a claim on ground the
			// ticket never meant, which holds a different ticket back for a
			// collision that does not exist. The same judgement
			// decompose.pathsIn makes about its own extraction.
			if p == "" || strings.ContainsAny(p, " \t") || seen[p] {
				continue
			}
			seen[p] = true
			out = append(out, p)
		}
	}
	return out
}
