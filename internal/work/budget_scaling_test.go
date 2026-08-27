package work

import "testing"

// A story's allowance grows with its sub-tasks.
//
// An earlier version REFUSED a story above a cap, reasoning that a large one
// would exhaust the turn ceiling and leave a branch half-finished. The
// constraint was real and the remedy was wrong: stories with twenty-five
// tasks are ordinary, and a tool that will not work them does not fit how
// people decompose. Give the agent room instead.
func TestABigStoryGetsRoomToFinish(t *testing.T) {
	flat := turnsFor(0, 0)
	if flat != 0 {
		t.Errorf("a childless ticket must keep the supervisor's own default (got %d)", flat)
	}

	// The number that motivated this: 25 tasks on 120 turns is ~5 turns per
	// task, which does not cover reading a file, changing it and running the
	// suite even once.
	big := turnsFor(0, 25)
	if perTask := big / 25; perTask < 15 {
		t.Errorf("%d turns for 25 sub-tasks is %d per task; too few to do the work",
			big, perTask)
	}
	if big <= 120 {
		t.Errorf("a 25-task story got %d turns, no more than a single ticket", big)
	}

	// More tasks, more room -- monotonically, or the scaling is a lie.
	if turnsFor(0, 25) <= turnsFor(0, 5) {
		t.Error("a bigger story did not get a bigger allowance")
	}
	if minutesFor(0, 25) <= minutesFor(0, 5) {
		t.Error("wall clock did not scale with the work")
	}
}

// An explicit bound is a decision, not a suggestion.
//
// Somebody passing --max-turns is bounding THIS run deliberately, often
// because they are testing something or watching spend. Silently raising it
// would make the flag advisory, which is worse than not offering it.
func TestAnExplicitBoundIsNeverRaised(t *testing.T) {
	if got := turnsFor(40, 25); got != 40 {
		t.Errorf("--max-turns 40 became %d on a 25-task story", got)
	}
	if got := minutesFor(10, 25); got != 10 {
		t.Errorf("--max-minutes 10 became %d on a 25-task story", got)
	}
}

// Unbounded growth is not a ceiling. Past a point the wall clock, the budget
// checkpoint and the plan's own rate limit are the real brakes, and a turn
// ceiling in the thousands stops being one.
func TestTheAllowanceIsStillBounded(t *testing.T) {
	huge := turnsFor(0, 500)
	if huge > 600 {
		t.Errorf("turns grew to %d; that is not a ceiling", huge)
	}
	if m := minutesFor(0, 500); m > 180 {
		t.Errorf("wall clock grew to %d minutes; that is not a ceiling", m)
	}
}
