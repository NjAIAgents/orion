package work

import "strings"

// Splitting the derived case list across authoring subagents (OR-305).
//
// WHY THIS IS SAFE HERE AND NOT FOR IMPLEMENTATION. ADR 0016 fixes the
// fan-out unit at the Go PACKAGE and rejects per-file splitting, for two
// named hazards: builds are not isolated, so a subagent running tests
// compiles against its peers' half-written files; and signature coupling,
// where one agent writes a call site against a signature another is still
// changing.
//
// Neither applies to test authoring. Test files do not define APIs that
// other test files import, and QA is not changing signatures -- it writes
// against an implementation that is already finished and committed (runQA
// captures preQA before its first turn precisely because that is true). The
// coupling that justified the package unit is absent, so the natural unit
// for THIS stage is the case group.
//
// The children still do not RUN anything: execution is a process Orion owns
// (OR-306), which is also what keeps the "builds are not isolated" hazard
// out of reach -- nobody compiles until every writer has finished.

// caseGroups splits a derived case list into at most n groups.
//
// The list is free-form text from the analyst, so the parsing is deliberately
// forgiving: countCases already treats a non-empty line that does not end in
// ":" as a case, and this agrees with it by construction rather than by a
// second rule that could drift.
//
// HEADINGS RIDE WITH THEIR CASES. A line ending in ":" is a heading, and a
// group that receives cases without the heading they sat under has lost the
// context that says what they are about. So a heading is carried into every
// group that gets a case following it -- duplicated on purpose, because two
// groups each seeing "Authentication:" is right and one group seeing a bare
// list is not.
//
// Returns nil when there is nothing to split or only one group's worth, which
// is the caller's signal to take the single-agent path unchanged.
func caseGroups(cases string, n int) []string {
	if n < 2 {
		return nil
	}
	type item struct {
		heading string
		text    string
	}
	var items []item
	heading := ""
	for _, line := range strings.Split(cases, "\n") {
		t := strings.TrimSpace(line)
		if t == "" {
			continue
		}
		if strings.HasSuffix(t, ":") {
			heading = t
			continue
		}
		items = append(items, item{heading: heading, text: line})
	}
	if len(items) < 2 {
		return nil
	}
	if n > len(items) {
		n = len(items)
	}

	// Even split with the remainder spread over the first groups, so no group
	// is more than one case larger than any other. A fan whose last child got
	// every leftover would finish when that child finished, which is the
	// serial cost this exists to remove.
	groups := make([]string, 0, n)
	per, extra := len(items)/n, len(items)%n
	at := 0
	for g := 0; g < n; g++ {
		size := per
		if g < extra {
			size++
		}
		var b strings.Builder
		lastHeading := ""
		for _, it := range items[at : at+size] {
			if it.heading != "" && it.heading != lastHeading {
				b.WriteString(it.heading + "\n")
				lastHeading = it.heading
			}
			b.WriteString(it.text + "\n")
		}
		groups = append(groups, strings.TrimRight(b.String(), "\n"))
		at += size
	}
	return groups
}
