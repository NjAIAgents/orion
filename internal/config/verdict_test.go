package config

import "testing"

// The budget scales with the run it resumes, and never drops below the floor.
//
// OR-248 is the case that matters: a thirty-minute QA session, re-asked under
// a flat five-minute cap, killed before it could answer. The change reached a
// pull request with no QA opinion at all -- neither a verdict nor a fix
// round, which is the worst of the three outcomes available.
func TestTheVerdictBudgetScalesWithTheRunItResumes(t *testing.T) {
	for _, tc := range []struct {
		name          string
		configured    int
		parentMinutes int
		want          int
	}{
		{"a short run gets the floor", 0, 4, 5},
		{"a run at the floor's worth still gets the floor", 0, 25, 5},
		{"OR-248's thirty-minute run gets six", 0, 30, 6},
		{"a very long run scales with it", 0, 90, 18},
		{"an unknown parent falls back to the floor", 0, 0, 5},
		{"a configured floor replaces the default", 12, 4, 12},
		{"a configured floor is still a floor, not a cap", 12, 90, 18},
		{"a configured floor wins when scaling is smaller", 12, 30, 12},
	} {
		t.Run(tc.name, func(t *testing.T) {
			q := QA{VerdictMinutes: tc.configured}
			if got := q.VerdictBudget(tc.parentMinutes); got != tc.want {
				t.Errorf("VerdictBudget(%d) = %d, want %d", tc.parentMinutes, got, tc.want)
			}
		})
	}
}

// The cheap case must stay cheap. The old comment's reasoning -- one line
// asked of a session that already did the work, and the short cap is what
// makes a re-ask cheaper than the fix round it replaces -- is still right for
// an ordinary run, and this is the assertion that keeps it true.
func TestAnOrdinaryRunsReAskIsStillShort(t *testing.T) {
	q := QA{}
	if got := q.VerdictBudget(10); got != 5 {
		t.Errorf("a ten-minute run's re-ask got %d minutes, want the 5 minute floor: "+
			"scaling must not make the common case expensive", got)
	}
}

// Zero means the built-in default, matching every other bound in this file.
func TestAnUnsetFloorIsTheBuiltInDefault(t *testing.T) {
	if got := (QA{}).VerdictFloor(); got != defaultVerdictMinutes {
		t.Errorf("VerdictFloor = %d, want the built-in %d", got, defaultVerdictMinutes)
	}
	if got := (QA{VerdictMinutes: 9}).VerdictFloor(); got != 9 {
		t.Errorf("VerdictFloor = %d, want the configured 9", got)
	}
}
