package tracker

import (
	"strings"
	"testing"
)

func issue(key string, blockedBy ...string) Issue {
	return Issue{Key: key, Status: "To Do", StatusCategory: "new", BlockedBy: blockedBy}
}

func keysIn(is []Issue) string {
	var out []string
	for _, i := range is {
		out = append(out, i.Key)
	}
	return strings.Join(out, ",")
}

// The cases OR-95 names, each a different way of getting queue order wrong.
func TestReadySeparatesWhatCanBeWorkedFromWhatIsWaiting(t *testing.T) {
	for _, tc := range []struct {
		name        string
		candidates  []Issue
		resolved    map[string]bool
		wantReady   string
		wantBlocked map[string]string // key -> a phrase its reason must contain
	}{
		{
			name:       "no links: everything is workable",
			candidates: []Issue{issue("OR-1"), issue("OR-2")},
			wantReady:  "OR-1,OR-2",
		},
		{
			name:        "an open blocker in the queue holds it",
			candidates:  []Issue{issue("OR-49"), issue("OR-50", "OR-49")},
			wantReady:   "OR-49",
			wantBlocked: map[string]string{"OR-50": "blocked by OR-49"},
		},
		{
			name:       "a resolved blocker does not",
			candidates: []Issue{issue("OR-50", "OR-49")},
			resolved:   map[string]bool{"OR-49": true},
			wantReady:  "OR-50",
		},
		{
			name: "an open blocker outside the queue still holds it",
			// The blocker is not labelled, so it is not a candidate. Being
			// absent from the queue says nothing about being finished.
			candidates:  []Issue{issue("OR-50", "OR-49")},
			resolved:    map[string]bool{"OR-49": false},
			wantReady:   "",
			wantBlocked: map[string]string{"OR-50": "blocked by OR-49"},
		},
		{
			name: "an unknown blocker does not block",
			// Another project, or one the token cannot see. Refusing to work
			// a ticket because of a reference nobody can inspect produces
			// work that can never start.
			candidates: []Issue{issue("OR-50", "OTHER-9")},
			wantReady:  "OR-50",
		},
		{
			name: "mixed: one open blocker among resolved ones is enough",
			candidates: []Issue{
				issue("OR-51", "OR-49", "OR-50"),
				issue("OR-50"),
			},
			resolved:    map[string]bool{"OR-49": true},
			wantReady:   "OR-50",
			wantBlocked: map[string]string{"OR-51": "blocked by OR-50"},
		},
		{
			name:       "a two-node cycle works neither, and names both",
			candidates: []Issue{issue("OR-1", "OR-2"), issue("OR-2", "OR-1")},
			wantReady:  "",
			wantBlocked: map[string]string{
				"OR-1": "cycle with OR-2",
				"OR-2": "cycle with OR-1",
			},
		},
		{
			name: "a cycle does not hold back unrelated tickets",
			candidates: []Issue{
				issue("OR-1", "OR-2"), issue("OR-2", "OR-1"), issue("OR-7"),
			},
			wantReady: "OR-7",
			wantBlocked: map[string]string{
				"OR-1": "cycle", "OR-2": "cycle",
			},
		},
		{
			name:        "a ticket blocked by itself says so rather than printing nothing",
			candidates:  []Issue{issue("OR-3", "OR-3")},
			wantReady:   "",
			wantBlocked: map[string]string{"OR-3": "OR-3"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var lookup func(string) (bool, bool)
			if tc.resolved != nil {
				lookup = func(k string) (bool, bool) {
					done, known := tc.resolved[k]
					return done, known
				}
			}
			ready, blocked := Ready(tc.candidates, lookup)

			if got := keysIn(ready); got != tc.wantReady {
				t.Errorf("ready = %q, want %q", got, tc.wantReady)
			}
			if tc.wantBlocked == nil {
				if len(blocked) != 0 {
					t.Errorf("blocked = %v, want none", blocked)
				}
				return
			}
			seen := map[string]string{}
			for _, b := range blocked {
				seen[b.Key] = b.Reason()
			}
			for key, phrase := range tc.wantBlocked {
				got, ok := seen[key]
				if !ok {
					t.Errorf("%s is not reported as blocked", key)
					continue
				}
				if !strings.Contains(got, phrase) {
					t.Errorf("%s reason = %q, want it to contain %q", key, got, phrase)
				}
			}
			// A blocked ticket that is also reported ready would be claimed
			// AND held, which is the one outcome worse than either.
			for _, r := range ready {
				if _, blockedToo := seen[r.Key]; blockedToo {
					t.Errorf("%s is reported both ready and blocked", r.Key)
				}
			}
		})
	}
}

// Rank is what people curate by dragging tickets. Dependencies decide
// eligibility; they must not reorder what remains.
func TestReadyPreservesTheQueuesOwnOrder(t *testing.T) {
	in := []Issue{issue("OR-9"), issue("OR-3"), issue("OR-7")}
	ready, blocked := Ready(in, nil)
	if len(blocked) != 0 {
		t.Fatalf("nothing is blocked here: %v", blocked)
	}
	if got := keysIn(ready); got != "OR-9,OR-3,OR-7" {
		t.Errorf("ready = %q, want the input order preserved", got)
	}
}

// A blocker that is in the queue AND finished is not a blocker. The status on
// the candidate itself answers it, with no lookup needed.
func TestAFinishedBlockerInTheQueueDoesNotHold(t *testing.T) {
	done := Issue{Key: "OR-49", Status: "Done", StatusCategory: "done"}
	ready, blocked := Ready([]Issue{done, issue("OR-50", "OR-49")}, nil)
	if len(blocked) != 0 {
		t.Fatalf("blocked = %v, want none: OR-49 is finished", blocked)
	}
	if got := keysIn(ready); got != "OR-49,OR-50" {
		t.Errorf("ready = %q, want both", got)
	}
}

// Jira stores a link once, from whichever side created it. Reading only one
// direction would see every dependency in one project and none in another.
func TestBlockersAreReadFromTheInwardSideOnly(t *testing.T) {
	links := []issueLink{
		{InwardIssue: &struct{ Key string }{Key: "OR-49"}},
		{InwardIssue: &struct{ Key string }{Key: "OR-12"}},
		{OutwardIssue: &struct{ Key string }{Key: "OR-99"}},
	}
	links[0].Type.Inward = "is blocked by"
	links[1].Type.Inward = "relates to"
	links[2].Type.Outward = "blocks"

	got := blockersOf(links)
	if len(got) != 1 || got[0] != "OR-49" {
		t.Errorf("blockersOf = %v, want [OR-49]: only 'is blocked by' orders work, "+
			"and a ticket this one BLOCKS is not a ticket that blocks it", got)
	}
}
