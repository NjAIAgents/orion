package promote

import (
	"strings"
	"testing"
)

// green is a set of inputs with nothing wrong, so each test can spoil exactly
// one thing and the assertion is about that thing alone.
func green() Inputs {
	return Inputs{
		Version:    "v0.9.0",
		HeadSHA:    "abcdef1234567890",
		BuildSHA:   "abcdef1234567890",
		BuildState: "success",
	}
}

func TestCleanMilestonePasses(t *testing.T) {
	v := Verify(green())
	if v.Blocking() {
		t.Fatalf("a clean milestone was blocked: %+v", v.Blockers())
	}
	if len(v.Checks) != 0 {
		t.Errorf("a clean milestone produced findings: %+v", v.Checks)
	}
}

// The distinction the whole gate rests on. Unfinished work is normal and must
// not stop a release: ship what is done, roll the rest. A gate that refused
// here would be bypassed within two releases.
func TestUnfinishedTicketsWarnButDoNotBlock(t *testing.T) {
	in := green()
	in.NotDone = []string{"OR-2"}

	v := Verify(in)
	if v.Blocking() {
		t.Fatal("an unfinished ticket blocked the release; one stuck ticket must never " +
			"hold the tag hostage")
	}
	if len(v.Warnings()) != 1 {
		t.Fatalf("unfinished work was not even reported: %+v", v.Checks)
	}
}

// A shipped change with no release note IS blocking, because the notes would
// silently under-report what shipped and no reader could tell.
func TestShippedTicketWithNoFragmentBlocks(t *testing.T) {
	in := green()
	in.TicketsWithoutFragment = []string{"OR-3"}

	v := Verify(in)
	if !v.Blocking() {
		t.Fatal("a done ticket with no changelog fragment did not block; the release " +
			"would ship it unmentioned")
	}
}

// Once the version is collated the fragments are deleted by design, so a done
// ticket the section does not name by key proves much less -- OR-105's work
// shipped inside v0.8.0 folded into another ticket's bullet. Blocking here
// made every already-published release permanently unpromotable, which is the
// opposite of what this check exists to say (OR-211).
func TestTicketNotNamedInACollatedChangelogOnlyWarns(t *testing.T) {
	in := green()
	in.Version = "v0.8.1"
	in.TicketsNotNamedInChangelog = []string{"OR-105"}

	v := Verify(in)
	if v.Blocking() {
		t.Fatalf("a shipped, collated release was reported unsafe to promote: %+v", v.Blockers())
	}
	if len(v.Warnings()) != 1 {
		t.Fatalf("the ticket was not reported at all: %+v", v.Checks)
	}
}

// The other direction must NOT block: a fragment staged early for the next
// release is legitimate and deleting it is the worse error.
func TestFragmentWithoutTicketOnlyWarns(t *testing.T) {
	in := green()
	in.FragmentsWithoutTicket = []string{"OR-99"}

	if v := Verify(in); v.Blocking() {
		t.Fatal("a fragment for another release blocked this one")
	}
}

// "Green recently" is not the check. A build that passed on a different
// commit says nothing about the tree being promoted.
func TestGreenBuildForADifferentCommitBlocks(t *testing.T) {
	in := green()
	in.BuildSHA = "0000000000000000"

	v := Verify(in)
	if !v.Blocking() {
		t.Fatal("a green build for a DIFFERENT sha was accepted; the verdict being " +
			"trusted was produced by other code")
	}
	found := false
	for _, c := range v.Blockers() {
		if strings.Contains(c.Detail, "promoting") {
			found = true
		}
	}
	if !found {
		t.Errorf("the finding does not say which sha is being promoted: %+v", v.Blockers())
	}
}

func TestMissingOrFailingBuildBlocks(t *testing.T) {
	for name, state := range map[string]string{
		"no build at all": "",
		"failed":          "failure",
		"cancelled":       "cancelled",
	} {
		t.Run(name, func(t *testing.T) {
			in := green()
			in.BuildState = state
			if v := Verify(in); !v.Blocking() {
				t.Fatalf("build state %q did not block the release", state)
			}
		})
	}
}

// A tag cut while a pull request is one click from landing produces a release
// that is missing it, and the fix costs a minute.
func TestOpenPullRequestsBlock(t *testing.T) {
	in := green()
	in.OpenPullRequests = []string{"#73"}

	if v := Verify(in); !v.Blocking() {
		t.Fatal("an open pull request against the integration branch did not block")
	}
}

// Deliberately a warning. Hand-pushed docs and changelog assembly are normal,
// and blocking on them would make the gate a nuisance that gets bypassed --
// which costs more than the check saves.
func TestUnattributedCommitsWarnButDoNotBlock(t *testing.T) {
	in := green()
	in.UnattributedCommits = []string{"76419df docs: assemble changelog"}

	v := Verify(in)
	if v.Blocking() {
		t.Fatal("a hand-pushed commit blocked the release")
	}
	if len(v.Warnings()) == 0 {
		t.Fatal("an unattributed commit was not reported at all")
	}
}

// Every blocker must name what to do something about; a gate that says only
// "blocked" gets overridden without being read.
func TestEveryCheckCarriesDetail(t *testing.T) {
	in := green()
	in.NotDone = []string{"OR-2"}
	in.TicketsWithoutFragment = []string{"OR-3"}
	in.FragmentsWithoutTicket = []string{"OR-99"}
	in.BuildState = "failure"
	in.OpenPullRequests = []string{"#73"}
	in.UnattributedCommits = []string{"abc123 chore: tidy"}

	v := Verify(in)
	if len(v.Checks) != 6 {
		t.Fatalf("expected all six checks to fire, got %d: %+v", len(v.Checks), v.Checks)
	}
	for _, c := range v.Checks {
		if strings.TrimSpace(c.Name) == "" || strings.TrimSpace(c.Detail) == "" {
			t.Errorf("check has no name or no detail: %+v", c)
		}
	}
}
