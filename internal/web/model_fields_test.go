package web

import (
	"testing"
	"time"
)

// Started is the timestamp of the first event in the session, not a launch
// time or a zero value -- the reader that fills this field has nothing else
// to set it from.
func TestSessionStartedIsTheFirstEventTimestamp(t *testing.T) {
	first := time.Date(2026, 9, 9, 13, 31, 4, 0, time.UTC)
	s := Session{Started: first}
	if !s.Started.Equal(first) {
		t.Errorf("Started = %s, want %s", s.Started, first)
	}
}

// Last is the timestamp of the most recent event, independent of Started --
// it is the only evidence a live view has that a session is still alive.
func TestSessionLastIsTheMostRecentEventTimestamp(t *testing.T) {
	first := time.Date(2026, 9, 9, 13, 31, 4, 0, time.UTC)
	mostRecent := first.Add(4 * time.Minute)
	s := Session{Started: first, Last: mostRecent}
	if !s.Last.Equal(mostRecent) {
		t.Errorf("Last = %s, want %s", s.Last, mostRecent)
	}
}

// Done says the run ended. False is the default for a session still going;
// true is set once, by whatever reads the terminating event.
func TestSessionDoneIndicatesWhetherTheSessionHasCompleted(t *testing.T) {
	running := Session{}
	if running.Done {
		t.Errorf("a fresh Session has Done = true, want false")
	}
	finished := Session{Done: true}
	if !finished.Done {
		t.Errorf("Done = false after being set true, want true")
	}
}

// Model is what actually ran, which can differ from what was requested after
// a fallback -- so this field holds the agent's own report, not a config
// value the reader already had lying around.
func TestSessionModelRepresentsTheModelActuallyUsed(t *testing.T) {
	requested := "claude-opus-5"
	actuallyUsed := "claude-sonnet-5"
	s := Session{Model: actuallyUsed}
	if s.Model != actuallyUsed {
		t.Errorf("Model = %q, want %q (the model actually used)", s.Model, actuallyUsed)
	}
	if s.Model == requested {
		t.Errorf("Model = %q, want it to differ from the requested model %q", s.Model, requested)
	}
}

// Steps counts the steps taken so far, and it is a running count, not a
// fixed or capped number -- a card re-drawn later reflects more steps than
// one drawn earlier in the same run.
func TestSessionStepsCorrectlyCountsTheNumberOfStepsTaken(t *testing.T) {
	early := Session{Steps: 3}
	later := Session{Steps: 3 + 1}
	if early.Steps != 3 {
		t.Errorf("Steps = %d, want 3", early.Steps)
	}
	if later.Steps != 4 {
		t.Errorf("Steps = %d, want 4", later.Steps)
	}
}

// Activity holds the agent's own words for what it is doing right now, and
// is empty when it is not saying -- not a placeholder string standing in for
// "unknown".
func TestSessionActivityContainsTheCurrentActivityDescription(t *testing.T) {
	s := Session{Activity: "editing internal/web/board.go"}
	if want := "editing internal/web/board.go"; s.Activity != want {
		t.Errorf("Activity = %q, want %q", s.Activity, want)
	}
	if silent := (Session{}).Activity; silent != "" {
		t.Errorf("Activity on a session saying nothing = %q, want empty", silent)
	}
}
