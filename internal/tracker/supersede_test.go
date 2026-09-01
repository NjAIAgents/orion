package tracker

import "testing"

// OR-243. Supersession is read from BOTH sides, unlike blocking, and the
// reason is about how people write the link rather than about Jira.
//
// Jira returns, for each link on issue X, whichever end is not X. So a link
// carrying inwardIssue means "X <inward> that issue", and one carrying
// outwardIssue means "X <outward> that issue".

func link(inward, outward, inKey, outKey string) issueLink {
	var l issueLink
	l.Type.Inward, l.Type.Outward = inward, outward
	if inKey != "" {
		l.InwardIssue = &struct{ Key string }{Key: inKey}
	}
	if outKey != "" {
		l.OutwardIssue = &struct{ Key string }{Key: outKey}
	}
	return l
}

func TestTheInwardSideNamesWhatSupersedesThisTicket(t *testing.T) {
	got := supersededByOf([]issueLink{
		link("is superseded by", "supersedes", "OR-235", ""),
	})
	if len(got) != 1 || got[0] != "OR-235" {
		t.Fatalf("supersededByOf = %v, want [OR-235]", got)
	}
}

// The case the rule exists for: OR-235 is drafted saying "supersedes OR-231",
// and nobody edits OR-231. Read from OR-235's record, the outward side is the
// only place that fact lives.
func TestTheOutwardSideNamesWhatThisTicketSupersedes(t *testing.T) {
	got := supersedesOf([]issueLink{
		link("is superseded by", "supersedes", "", "OR-231"),
	})
	if len(got) != 1 || got[0] != "OR-231" {
		t.Fatalf("supersedesOf = %v, want [OR-231]", got)
	}
}

// The two must not read each other's side. Getting this backwards evicts the
// NEWER ticket, permanently, with a reason that sounds right.
func TestTheTwoSidesAreNotConfusedForEachOther(t *testing.T) {
	l := []issueLink{link("is superseded by", "supersedes", "", "OR-231")}
	if got := supersededByOf(l); len(got) != 0 {
		t.Errorf("supersededByOf read the outward side and returned %v; that would evict "+
			"the ticket doing the superseding", got)
	}

	l = []issueLink{link("is superseded by", "supersedes", "OR-235", "")}
	if got := supersedesOf(l); len(got) != 0 {
		t.Errorf("supersedesOf read the inward side and returned %v", got)
	}
}

// A site whose outward description is the passive form is describing the
// opposite relationship.
func TestAPassiveOutwardDescriptionIsNotReadAsSupersedes(t *testing.T) {
	got := supersedesOf([]issueLink{
		link("supersedes", "is superseded by", "", "OR-235"),
	})
	if len(got) != 0 {
		t.Errorf("supersedesOf = %v; an outward description reading \"is superseded by\" "+
			"means this ticket is the obsolete one, not the superseder", got)
	}
}

// Blocking still reads one side only, and supersession links must not leak
// into it. A ticket superseded by another is obsolete, not blocked, and
// reporting it as blocked would have it wait for a blocker to clear.
func TestSupersessionLinksAreNotReadAsBlockers(t *testing.T) {
	l := []issueLink{
		link("is superseded by", "supersedes", "OR-235", ""),
		link("is blocked by", "blocks", "OR-224", ""),
	}
	got := blockersOf(l)
	if len(got) != 1 || got[0] != "OR-224" {
		t.Errorf("blockersOf = %v, want only the blocking link", got)
	}
}

func TestALinkWithNoCounterpartKeyIsIgnored(t *testing.T) {
	l := []issueLink{link("is superseded by", "supersedes", "", "")}
	if got := supersededByOf(l); len(got) != 0 {
		t.Errorf("supersededByOf = %v for a link with no keys", got)
	}
	if got := supersedesOf(l); len(got) != 0 {
		t.Errorf("supersedesOf = %v for a link with no keys", got)
	}
}

// Unrelated link types say nothing about obsolescence. "relates to" and
// "duplicates" are the ones people reach for when they mean "see also".
func TestUnrelatedLinkTypesAreIgnored(t *testing.T) {
	l := []issueLink{
		link("relates to", "relates to", "OR-1", ""),
		link("duplicates", "is duplicated by", "", "OR-2"),
		link("clones", "is cloned by", "OR-3", ""),
	}
	if got := supersededByOf(l); len(got) != 0 {
		t.Errorf("supersededByOf = %v, want nothing", got)
	}
	if got := supersedesOf(l); len(got) != 0 {
		t.Errorf("supersedesOf = %v, want nothing", got)
	}
}
