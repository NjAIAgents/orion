package queue

import (
	"strings"
	"testing"
	"time"

	"github.com/orion-sdlc/orion/internal/tracker"
)

func issue(key string) tracker.Issue { return tracker.Issue{Key: key} }

// allResolved is the common case: every blocker named is finished.
func allResolved(string) (bool, bool) { return true, true }

func verdicts(ds []Decision) map[string]Verdict {
	m := map[string]Verdict{}
	for _, d := range ds {
		m[d.Key] = d.Verdict
	}
	return m
}

func TestAReadyQueueIsAdmittedUpToTheFreeSlots(t *testing.T) {
	ds := Plan(Facts{
		Candidates: []tracker.Issue{issue("OR-1"), issue("OR-2"), issue("OR-3")},
		Free:       2, Resolved: allResolved,
	})
	if got := Admitted(ds); len(got) != 2 || got[0] != "OR-1" || got[1] != "OR-2" {
		t.Fatalf("admitted %v, want the first two in the tracker's own order", got)
	}
	if v := verdicts(ds)["OR-3"]; v != Hold {
		t.Errorf("the third ticket is %s, want held", v)
	}
}

// Rank is a person expressing an intention by dragging a ticket. The manager
// decides what is eligible; it must never reorder what remains.
func TestPlanPreservesTheTrackersOwnOrder(t *testing.T) {
	in := []tracker.Issue{issue("OR-9"), issue("OR-1"), issue("OR-5")}
	ds := Plan(Facts{Candidates: in, Free: 3, Resolved: allResolved})
	for i := range in {
		if ds[i].Key != in[i].Key {
			t.Fatalf("decision %d is %s, want %s -- the manager reordered the queue",
				i, ds[i].Key, in[i].Key)
		}
	}
}

// The capacity limit must not speak over a rule the operator can act on. A
// blocked ticket reported as "no free slot" is true and useless, and the next
// pass would say it again with the blocker still unnamed.
func TestCapacityIsAppliedAfterTheRulesNotBeforeThem(t *testing.T) {
	blocked := tracker.Issue{Key: "OR-2", BlockedBy: []string{"OR-99"}}
	ds := Plan(Facts{
		Candidates: []tracker.Issue{issue("OR-1"), blocked, issue("OR-3")},
		Free:       1,
		Resolved:   func(string) (bool, bool) { return false, true },
	})
	m := map[string]Decision{}
	for _, d := range ds {
		m[d.Key] = d
	}
	if m["OR-2"].Rule != "blocked" {
		t.Errorf("OR-2 was reported as %q, want the blocker named", m["OR-2"].Rule)
	}
	if !strings.Contains(m["OR-2"].Reason, "OR-99") {
		t.Errorf("the blocker is not named in %q", m["OR-2"].Reason)
	}
	// OR-3 is the one that genuinely lost on capacity.
	if m["OR-3"].Rule != "capacity" {
		t.Errorf("OR-3 was reported as %q, want capacity", m["OR-3"].Rule)
	}
}

// The case the rule exists for: OR-235 declares it supersedes OR-231, and
// OR-231's own record says nothing at all. Reading only the older ticket's
// side would admit and work an obsolete ticket.
func TestATicketIsEvictedWhenAnotherDeclaresItSuperseded(t *testing.T) {
	older := issue("OR-231")
	newer := tracker.Issue{Key: "OR-235", Supersedes: []string{"OR-231"}}

	ds := Plan(Facts{Candidates: []tracker.Issue{older, newer}, Free: 5, Resolved: allResolved})
	m := verdicts(ds)
	if m["OR-231"] != Evict {
		t.Errorf("OR-231 is %s, want evicted -- the newer ticket declared it superseded "+
			"and nothing edited the older one", m["OR-231"])
	}
	if m["OR-235"] != Admit {
		t.Errorf("OR-235 is %s, want admitted -- it is the superseder, not the superseded",
			m["OR-235"])
	}
	for _, d := range ds {
		if d.Key == "OR-231" && !strings.Contains(d.Reason, "OR-235") {
			t.Errorf("the reason does not name what superseded it: %q", d.Reason)
		}
	}
}

// And the other side, where the older ticket does carry the mirror link.
func TestATicketIsEvictedWhenItsOwnRecordSaysSuperseded(t *testing.T) {
	older := tracker.Issue{Key: "OR-231", SupersededBy: []string{"OR-235"}}
	ds := Plan(Facts{Candidates: []tracker.Issue{older}, Free: 5, Resolved: allResolved})
	if ds[0].Verdict != Evict || !strings.Contains(ds[0].Reason, "OR-235") {
		t.Errorf("got %s / %q, want evicted naming OR-235", ds[0].Verdict, ds[0].Reason)
	}
}

func TestAnUnscheduledTicketIsHeldWithTheReasonGiven(t *testing.T) {
	ds := Plan(Facts{
		Candidates: []tracker.Issue{issue("OR-1")},
		Free:       5, Resolved: allResolved,
		Scheduled: func(tracker.Issue) string { return "no milestone; nobody scheduled this" },
	})
	if ds[0].Verdict != Hold || ds[0].Rule != "unversioned" {
		t.Fatalf("got %s / %s, want held as unversioned", ds[0].Verdict, ds[0].Rule)
	}
	if !strings.Contains(ds[0].Reason, "milestone") {
		t.Errorf("the reason is not the scheduler's own sentence: %q", ds[0].Reason)
	}
}

// Held, not evicted. An unscheduled ticket becomes workable the moment
// somebody puts it on a milestone, and evicting it would write a state that
// has to be undone by hand for a fix that takes one click.
func TestAnUnscheduledTicketIsHeldRatherThanEvicted(t *testing.T) {
	ds := Plan(Facts{
		Candidates: []tracker.Issue{issue("OR-1")}, Free: 5, Resolved: allResolved,
		Scheduled: func(tracker.Issue) string { return "no milestone" },
	})
	if ds[0].Verdict == Evict {
		t.Error("an unscheduled ticket was evicted; adding a fixVersion is one click, " +
			"and an eviction has to be undone by hand")
	}
}

func TestEachEvictionSignalFiresAndNamesItself(t *testing.T) {
	for _, tc := range []struct {
		name string
		f    Facts
		rule string
		says string
	}{
		{"breaker", Facts{MaxTrips: 2, Trips: func(string) (int, bool) { return 2, true }},
			"breaker", "tripped"},
		{"rounds", Facts{MaxFixRounds: 3, FixRounds: func(string) (int, bool) { return 3, true }},
			"rounds", "ceiling"},
		{"stranded", Facts{MaxStranded: 2, Stranded: func(string) (int, bool) { return 2, true }},
			"stranded", "settled"},
	} {
		f := tc.f
		f.Candidates = []tracker.Issue{issue("OR-1")}
		f.Free = 5
		f.Resolved = allResolved
		ds := Plan(f)
		if ds[0].Verdict != Evict {
			t.Errorf("%s: got %s, want evicted", tc.name, ds[0].Verdict)
			continue
		}
		if ds[0].Rule != tc.rule {
			t.Errorf("%s: rule %q, want %q", tc.name, ds[0].Rule, tc.rule)
		}
		if !strings.Contains(ds[0].Reason, tc.says) {
			t.Errorf("%s: reason %q does not say what happened", tc.name, ds[0].Reason)
		}
	}
}

// Unknown is not zero. A worktree that has been cleaned up says nothing about
// how many rounds a ticket spent, and reading that silence as "none" is how a
// ticket that failed twice gets a third run.
func TestAnUnreadableSignalDoesNotEvictAndDoesNotCountAsZero(t *testing.T) {
	ds := Plan(Facts{
		Candidates:   []tracker.Issue{issue("OR-1")},
		Free:         5,
		Resolved:     allResolved,
		MaxFixRounds: 1,
		FixRounds:    func(string) (int, bool) { return 0, false },
	})
	if ds[0].Verdict != Admit {
		t.Errorf("got %s, want admitted: an unreadable signal is no evidence, "+
			"not evidence of nothing", ds[0].Verdict)
	}
}

// A limit of zero disables its rule, which is the honest default for a caller
// that cannot measure that signal at all.
func TestAZeroLimitDisablesItsRule(t *testing.T) {
	ds := Plan(Facts{
		Candidates: []tracker.Issue{issue("OR-1")}, Free: 5, Resolved: allResolved,
		MaxTrips: 0, Trips: func(string) (int, bool) { return 99, true },
	})
	if ds[0].Verdict != Admit {
		t.Errorf("got %s, want admitted: a zero limit means the rule is off", ds[0].Verdict)
	}
}

// Two attempts at anything, then a person. The third attempt at something
// that has failed the same way twice spends money to learn nothing.
func TestATicketEvictedTwiceEscalatesInsteadOfBeingEvictedAgain(t *testing.T) {
	var l Ledger
	now := time.Now()
	l.Record(Eviction{Key: "OR-1", Rule: "rounds", Reason: "the fix-round ceiling of 3 is spent"}, now)
	l.Record(Eviction{Key: "OR-1", Rule: "rounds", Reason: "the fix-round ceiling of 3 is spent"}, now)

	ds := Plan(Facts{
		Candidates: []tracker.Issue{issue("OR-1")}, Free: 5, Resolved: allResolved,
		MaxFixRounds: 3, FixRounds: func(string) (int, bool) { return 3, true },
		Ledger: l,
	})
	if ds[0].Verdict != Escalate {
		t.Fatalf("got %s, want escalate on the third strike", ds[0].Verdict)
	}
	if !strings.Contains(ds[0].Reason, "a person should look") {
		t.Errorf("the escalation does not say a person is needed: %q", ds[0].Reason)
	}
	if !strings.Contains(ds[0].Reason, "last time") {
		t.Errorf("the escalation does not carry what happened before: %q", ds[0].Reason)
	}
}

func TestOneEvictionIsStillAnEviction(t *testing.T) {
	var l Ledger
	l.Record(Eviction{Key: "OR-1", Rule: "rounds", Reason: "spent"}, time.Now())
	ds := Plan(Facts{
		Candidates: []tracker.Issue{issue("OR-1")}, Free: 5, Resolved: allResolved,
		MaxFixRounds: 3, FixRounds: func(string) (int, bool) { return 3, true },
		Ledger: l,
	})
	if ds[0].Verdict != Evict {
		t.Errorf("got %s, want a second eviction before escalating", ds[0].Verdict)
	}
}

// Eviction outranks the admission refusals: a blocked-and-also-spent ticket
// reported only as blocked would wait forever for a blocker whose clearing
// would not help it.
func TestASpentTicketIsEvictedEvenWhenItIsAlsoBlocked(t *testing.T) {
	i := tracker.Issue{Key: "OR-1", BlockedBy: []string{"OR-99"}}
	ds := Plan(Facts{
		Candidates: []tracker.Issue{i}, Free: 5,
		Resolved:     func(string) (bool, bool) { return false, true },
		MaxFixRounds: 1, FixRounds: func(string) (int, bool) { return 5, true },
	})
	if ds[0].Verdict != Evict || ds[0].Rule != "rounds" {
		t.Errorf("got %s / %s, want the eviction to win over the block",
			ds[0].Verdict, ds[0].Rule)
	}
}

// Six tickets held on one missing milestone is one fact. Six lines saying it
// is how the one line that matters gets buried.
func TestDecisionsAreGroupedByReasonNotByTicket(t *testing.T) {
	ds := []Decision{
		{Key: "OR-1", Verdict: Hold, Rule: "unversioned", Reason: "no milestone"},
		{Key: "OR-2", Verdict: Hold, Rule: "unversioned", Reason: "no milestone"},
		{Key: "OR-3", Verdict: Admit, Rule: "ready", Reason: "ready"},
		{Key: "OR-4", Verdict: Evict, Rule: "rounds", Reason: "spent"},
	}
	gs := Grouped(ds)
	if len(gs) != 2 {
		t.Fatalf("got %d groups, want 2 (admissions excluded)", len(gs))
	}
	for _, g := range gs {
		if g.Reason == "no milestone" && len(g.Keys) != 2 {
			t.Errorf("the shared reason has %d keys, want both tickets", len(g.Keys))
		}
		if g.Verdict == Admit {
			t.Error("admissions are reported by the dispatch, not by the manager; " +
				"repeating them doubles every started ticket in the log")
		}
	}
}

// A reason that names its own ticket can never group, which defeats the
// console's collapsing of repeated lines.
func TestAReasonDoesNotNameTheTicketItIsAbout(t *testing.T) {
	ds := Plan(Facts{
		Candidates: []tracker.Issue{issue("OR-231"), issue("OR-232")},
		Free:       0, Resolved: allResolved,
	})
	for _, d := range ds {
		if strings.Contains(d.Reason, d.Key) {
			t.Errorf("%s's reason names itself (%q), so two tickets held for the same "+
				"reason would print two lines instead of one", d.Key, d.Reason)
		}
	}
}

func TestStarvationIsCountedPerPassAndClearedOnAdmission(t *testing.T) {
	var l Ledger
	for i := 0; i < 3; i++ {
		l.Missed("OR-1")
	}
	l.Missed("OR-2")

	if got := l.Starved(3); len(got) != 1 || got[0] != "OR-1" {
		t.Fatalf("starved = %v, want only OR-1 at a threshold of 3", got)
	}
	l.Admitted("OR-1")
	if got := l.Starved(3); len(got) != 0 {
		t.Errorf("starved = %v after admission; the count measures neglect, not age", got)
	}
}

// A person who re-queues a ticket by hand has made a decision. The manager's
// job is not to overrule it on the next pass by escalating on a count they
// meant to clear.
func TestForgettingATicketClearsItsHistoryEntirely(t *testing.T) {
	var l Ledger
	now := time.Now()
	l.Record(Eviction{Key: "OR-1", Reason: "a"}, now)
	l.Record(Eviction{Key: "OR-2", Reason: "b"}, now)
	l.Record(Eviction{Key: "OR-1", Reason: "c"}, now)
	l.Missed("OR-1")

	l.Forget("OR-1")
	if l.Count("OR-1") != 0 {
		t.Errorf("OR-1 still has %d evictions after being forgotten", l.Count("OR-1"))
	}
	if l.Count("OR-2") != 1 {
		t.Errorf("forgetting OR-1 dropped OR-2's history too")
	}
	if len(l.Starved(1)) != 0 {
		t.Error("forgetting a ticket left its neglect count behind")
	}
}

func TestAnEmptyQueueDecidesNothing(t *testing.T) {
	if ds := Plan(Facts{Free: 5}); len(ds) != 0 {
		t.Errorf("got %d decisions for an empty queue", len(ds))
	}
	if gs := Grouped(nil); len(gs) != 0 {
		t.Errorf("got %d groups for no decisions; a pass that changed nothing prints nothing",
			len(gs))
	}
}
