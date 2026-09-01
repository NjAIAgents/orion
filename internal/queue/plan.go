package queue

// The queue manager: admit, order and evict by rule (OR-243).
//
// WHY THIS IS ORION AND NOT AN AGENT. Deciding what enters the queue is
// sequencing across stages, which ADR 0001 makes Orion's. An agent that
// admitted and evicted tickets would own control flow across stages, which the
// precedence rule declines. Every rule below is decidable from data Orion or
// Jira already holds, and returns the same answer every time it is asked --
// which is the other half of the argument: a queue that orders differently on
// identical input is a bug nobody can reproduce.
//
// A PURE FUNCTION over facts, deliberately. Plan touches no tracker, no
// filesystem and no clock it was not handed. That is what lets the interesting
// cases -- a ticket superseded by one that is itself superseded, a second
// eviction, a ticket starved for six passes -- be tested without a Jira.
//
// WHAT IT DOES NOT DO:
//
//	IT DOES NOT REORDER. Priority DESC then Rank ASC is Jira's answer and it
//	is already in the claim query; tracker.Ready preserves it and so does
//	this. The manager decides what is ELIGIBLE. Rank decides what goes first,
//	because Rank is a person expressing an intention by dragging a ticket, and
//	silently overruling that is how a queue stops being trusted.
//
//	IT DOES NOT DEPRIORITISE. There is no rule here that pushes a ticket down.
//	A ticket is admitted, held with a named reason, or evicted with a named
//	reason and a record. Anything held for too many consecutive passes is
//	REPORTED BY NAME, so nothing rots at the bottom unnoticed -- the failure
//	mode a self-reordering queue has and a hand-maintained one does not.

import (
	"fmt"
	"sort"
	"strings"

	"github.com/orion-sdlc/orion/internal/tracker"
)

// Verdict is what the manager decided about one ticket.
type Verdict string

const (
	// Admit: eligible, and within the concurrency limit.
	Admit Verdict = "admit"
	// Hold: eligible later. Nothing is written to the ticket; it is simply
	// not claimed this pass, and the reason is reported.
	Hold Verdict = "hold"
	// Evict: not eligible until something changes that Orion cannot do. The
	// ticket moves to a named state carrying the reason.
	Evict Verdict = "evict"
	// Escalate: evicted before, and evicting again would be the third
	// attempt at something that has failed the same way twice.
	Escalate Verdict = "escalate"
)

// Decision is one ticket's outcome and why, in the words the report uses.
type Decision struct {
	Key     string
	Verdict Verdict
	Rule    string // the rule that fired, for counting by cause
	Reason  string // the sentence an operator reads
}

// Line renders one decision as the single line the per-pass report prints.
//
// The reason carries no ticket key and no count, so identical decisions
// collapse into one line with a count in the console (OR-217). That is why
// "blocked by OR-224" belongs in Reason and "OR-116" does not: the grouping is
// by REASON, and a reason that names its own ticket can never group.
func (d Decision) Line() string { return string(d.Verdict) + ": " + d.Reason }

// Facts is everything Plan needs, gathered by the caller.
//
// A struct rather than eight parameters because the callers differ in what
// they can supply -- `orion queue` has no worktrees to inspect -- and a zero
// value for a fact must mean "no evidence", never "evidence of zero".
type Facts struct {
	// Candidates are the queued tickets, in the tracker's own order.
	Candidates []tracker.Issue
	// Free is how many may be admitted this pass.
	Free int

	// Resolved answers whether a blocker is finished, and whether it is
	// known at all. Same contract as tracker.Ready: unknown does not block,
	// because a link to a ticket nobody can see must not strand work
	// forever.
	Resolved func(key string) (done, known bool)

	// Scheduled reports whether a ticket is on an open milestone, and the
	// sentence to say when it is not. Empty reason means scheduled.
	Scheduled func(i tracker.Issue) string

	// FixRounds, Trips and Stranded are the three eviction signals, each
	// returning a count and whether it could be read at all. Unreadable is
	// NOT zero: a worktree that has been cleaned up says nothing about how
	// many rounds a ticket spent, and reading that silence as "none" is how
	// a ticket that failed twice gets a third run.
	FixRounds func(key string) (n int, known bool)
	Trips     func(key string) (n int, known bool)
	Stranded  func(key string) (passes int, known bool)

	// Limits on the three eviction signals. Zero disables that rule, which
	// is the honest default for a caller that cannot measure it.
	MaxFixRounds int
	MaxTrips     int
	MaxStranded  int

	// StarveAfter is how many consecutive unheld passes before a ticket is
	// named in the report. Zero disables the warning.
	StarveAfter int

	// Ledger is the eviction history. Read-only here: Plan decides, the
	// caller records, so a dry run can produce the whole plan and write
	// nothing.
	Ledger Ledger
}

// Plan decides every candidate, in the order they were given.
//
// Decisions come back for ALL candidates, not only the admitted ones. A queue
// that reports what it started and stays quiet about what it refused is the
// thing this replaces: the refusals are the part a person cannot reconstruct.
func Plan(f Facts) []Decision {
	out := make([]Decision, 0, len(f.Candidates))

	// The set of keys any candidate declares obsolete. Built across the whole
	// set first, because supersession is written on the NEWER ticket and the
	// older one's own record may say nothing at all -- see
	// tracker.supersedesOf. One pass to learn it, then one to decide.
	obsolete := map[string]string{} // superseded key -> the ticket that says so
	for _, i := range f.Candidates {
		for _, k := range i.Supersedes {
			obsolete[norm(k)] = i.Key
		}
	}

	admitted := 0
	for _, i := range f.Candidates {
		d := decide(f, i, obsolete)

		// The concurrency limit is applied LAST, and only to tickets that
		// were otherwise eligible. Ordering it before the rules would report
		// a blocked ticket as "no free slot", which is true and useless: the
		// slot was never its problem, and the next pass would say the same
		// thing again with the blocker still unnamed.
		if d.Verdict == Admit {
			if admitted >= f.Free {
				d = Decision{Key: i.Key, Verdict: Hold, Rule: "capacity",
					Reason: "no free slot this pass"}
			} else {
				admitted++
			}
		}
		out = append(out, d)
	}
	return out
}

// decide applies the rules to one ticket, in refusal order.
//
// ORDER MATTERS AND IS NOT ARBITRARY. Eviction is checked before admission
// refusals because an evicted ticket needs a state written to it, and a
// blocked-and-also-spent ticket that reported only "blocked" would sit in the
// queue forever waiting for a blocker to clear that would not help it. Within
// the admission refusals the order is cheapest-to-fix first, so the sentence
// an operator gets is the one they can act on soonest.
func decide(f Facts, i tracker.Issue, obsolete map[string]string) Decision {
	if d, yes := evictionFor(f, i); yes {
		// Escalate rather than evict again on the third strike. The manager
		// gets two attempts at anything, the same cap the orchestration
		// conventions put on fix rounds.
		if f.Ledger.Count(i.Key) >= EscalateAfter {
			prior := ""
			if last, ok := f.Ledger.Last(i.Key); ok {
				prior = ", last time: " + last.Reason
			}
			return Decision{Key: i.Key, Verdict: Escalate, Rule: d.Rule,
				Reason: fmt.Sprintf("evicted %d times already%s; a person should look "+
					"rather than Orion trying again", f.Ledger.Count(i.Key), prior)}
		}
		return d
	}

	if by, dead := obsolete[norm(i.Key)]; dead {
		return Decision{Key: i.Key, Verdict: Evict, Rule: "superseded",
			Reason: "superseded by " + by}
	}
	if len(i.SupersededBy) > 0 {
		return Decision{Key: i.Key, Verdict: Evict, Rule: "superseded",
			Reason: "superseded by " + strings.Join(i.SupersededBy, ", ")}
	}

	if f.Scheduled != nil {
		if why := f.Scheduled(i); why != "" {
			return Decision{Key: i.Key, Verdict: Hold, Rule: "unversioned", Reason: why}
		}
	}

	if f.Resolved != nil {
		if b, blocked := blockedBy(i, f.Resolved); blocked {
			return Decision{Key: i.Key, Verdict: Hold, Rule: "blocked", Reason: b.Reason()}
		}
	}

	return Decision{Key: i.Key, Verdict: Admit, Rule: "ready", Reason: "ready"}
}

// blockedBy asks tracker.Ready about one issue.
//
// Through Ready rather than by re-reading BlockedBy here, so there is exactly
// one implementation of what "blocked" means -- including the cycle detection
// and the unknown-does-not-block rule, both of which are easy to get subtly
// wrong a second time (OR-95).
func blockedBy(i tracker.Issue, resolved func(string) (bool, bool)) (tracker.Blocked, bool) {
	ready, blocked := tracker.Ready([]tracker.Issue{i}, resolved)
	if len(ready) > 0 || len(blocked) == 0 {
		return tracker.Blocked{}, false
	}
	return blocked[0], true
}

// evictionFor reports the first eviction signal that has tripped.
//
// Each signal is skipped when its limit is zero or its reading is unknown.
// Unknown is not zero: a cleaned-up worktree says nothing about how many
// rounds a ticket spent, and reading that silence as "none" would evict
// nothing and admit everything, which is the failure that looks like success.
func evictionFor(f Facts, i tracker.Issue) (Decision, bool) {
	if f.MaxTrips > 0 && f.Trips != nil {
		if n, known := f.Trips(i.Key); known && n >= f.MaxTrips {
			return Decision{Key: i.Key, Verdict: Evict, Rule: "breaker",
				Reason: fmt.Sprintf("the breaker tripped %d times without landing", n)}, true
		}
	}
	if f.MaxFixRounds > 0 && f.FixRounds != nil {
		if n, known := f.FixRounds(i.Key); known && n >= f.MaxFixRounds {
			return Decision{Key: i.Key, Verdict: Evict, Rule: "rounds",
				Reason: fmt.Sprintf("the fix-round ceiling of %d is spent", f.MaxFixRounds)}, true
		}
	}
	if f.MaxStranded > 0 && f.Stranded != nil {
		if n, known := f.Stranded(i.Key); known && n >= f.MaxStranded {
			return Decision{Key: i.Key, Verdict: Evict, Rule: "stranded",
				Reason: fmt.Sprintf("its worktree could not be settled after %d passes", n)}, true
		}
	}
	return Decision{}, false
}

// Admitted returns the keys to claim, in the order Plan saw them.
func Admitted(ds []Decision) []string {
	var out []string
	for _, d := range ds {
		if d.Verdict == Admit {
			out = append(out, d.Key)
		}
	}
	return out
}

// Report groups decisions by their sentence, the way the watch tick already
// groups held tickets.
//
// GROUPED BY REASON, NOT BY TICKET. Six tickets held on one missing milestone
// is one fact and should read as one line; six lines saying the same thing is
// how the one line that matters gets buried. The keys are carried so the line
// can name them, which is the other half of "nothing rots unnoticed".
type Group struct {
	Verdict Verdict
	Rule    string
	Reason  string
	Keys    []string
}

// Grouped collapses decisions into one entry per distinct reason, admissions
// excluded -- those are reported by the dispatch itself, and repeating them
// here would double every started ticket in the log.
//
// Sorted by verdict then reason so a pass that decided the same things prints
// the same lines in the same order, which is what lets a reader skim for the
// one that changed.
func Grouped(ds []Decision) []Group {
	idx := map[string]int{}
	var out []Group
	for _, d := range ds {
		if d.Verdict == Admit {
			continue
		}
		k := string(d.Verdict) + "\x00" + d.Reason
		if at, seen := idx[k]; seen {
			out[at].Keys = append(out[at].Keys, d.Key)
			continue
		}
		idx[k] = len(out)
		out = append(out, Group{Verdict: d.Verdict, Rule: d.Rule,
			Reason: d.Reason, Keys: []string{d.Key}})
	}
	sort.SliceStable(out, func(a, b int) bool {
		if out[a].Verdict != out[b].Verdict {
			return out[a].Verdict < out[b].Verdict
		}
		return out[a].Reason < out[b].Reason
	})
	return out
}

func norm(k string) string { return strings.ToUpper(strings.TrimSpace(k)) }
