package ui

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

// collapsedState is a region with a batch and several ticket rows: the shape
// that gets too tall to fit, which is what collapsing is for.
func collapsedState() liveState {
	start := time.Now().Add(-5 * time.Minute)
	return liveState{
		rows: []liveRun{
			{key: "OR-1", stage: "implementing", started: start},
			{key: "OR-2", stage: "implementing", started: start},
			{key: "OR-3", stage: "qa", started: start},
		},
	}
}

// Collapsed drops the per-ticket rows, because those are what grow with
// max_concurrent_tickets, and keeps the header, because a status line that can
// vanish is not a status line.
func TestCollapsedHidesTheRowsAndKeepsTheStatusLine(t *testing.T) {
	var w bytes.Buffer
	st := collapsedState()
	now := time.Now()

	expanded := strings.Join(renderRegionAt(&w, st, now, 100, false), "\n")
	collapsed := strings.Join(renderRegionAt(&w, st, now, 100, true), "\n")

	for _, key := range []string{"OR-1", "OR-2", "OR-3"} {
		if !strings.Contains(expanded, key) {
			t.Fatalf("expanded region is missing %s; the fixture is wrong", key)
		}
		if strings.Contains(collapsed, key) {
			t.Errorf("collapsed region still shows %s: the ticket rows are the thing "+
				"that grows with the cap, so they are what has to go", key)
		}
	}
	if !strings.Contains(collapsed, "OR") {
		t.Error("collapsed region lost the status line; it is pinned and must survive " +
			"every state, or collapsing answers 'too many rows' with 'no information'")
	}
	if len(renderRegionAt(&w, st, now, 100, true)) >= len(expanded) {
		t.Error("collapsing did not make the region shorter")
	}
}

// Hidden runs are SAID to be hidden. Someone who collapsed the region ten
// minutes ago and comes back to a quiet screen must not read it as finished.
func TestCollapsedSaysHowManyRunsAreHidden(t *testing.T) {
	var w bytes.Buffer
	got := strings.Join(renderRegionAt(&w, collapsedState(), time.Now(), 100, true), "\n")

	if !strings.Contains(got, "3 run(s) hidden") {
		t.Errorf("collapsed region does not say what it is hiding:\n%s", got)
	}
	if !strings.Contains(got, "ctrl-o") {
		t.Errorf("collapsed region does not say how to get the rows back:\n%s", got)
	}
}

// The control must be reversible. The key handling this replaces dropped the
// window's cap on ANY keystroke, permanently -- a stray arrow key ended the
// frozen window for the rest of the run, with no way back.
func TestCollapsingIsAToggleAndNotAOneWayDoor(t *testing.T) {
	l := &Live{w: &bytes.Buffer{}, cursor: true}

	l.ToggleCollapsed()
	if !l.collapsed {
		t.Fatal("first toggle did not collapse")
	}
	l.ToggleCollapsed()
	if l.collapsed {
		t.Error("second toggle did not expand: a control an operator cannot reverse " +
			"is one they learn not to touch")
	}
}

// Only the two keys do anything. Anything else typed at the terminal -- an
// arrow, a paste, a stray return from the shell behind the watcher -- must
// leave the display exactly as it was.
func TestOnlyTheBoundKeysChangeTheDisplay(t *testing.T) {
	for _, tc := range []struct {
		name          string
		in            []byte
		wantCollapsed bool
		wantFull      bool
	}{
		{"ctrl-o collapses", []byte{keyCollapse}, true, false},
		{"ctrl-r drops the cap", []byte{keyFullLog}, false, true},
		{"a letter does nothing", []byte("q"), false, false},
		{"return does nothing", []byte{'\n'}, false, false},
		{"an arrow key does nothing", []byte{0x1b, '[', 'A'}, false, false},
		{"both keys in one read are both answered",
			[]byte{keyCollapse, keyFullLog}, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			l := &Live{w: &bytes.Buffer{}, cursor: true}
			l.watchInput(bytes.NewReader(tc.in))

			if l.collapsed != tc.wantCollapsed {
				t.Errorf("collapsed = %v, want %v", l.collapsed, tc.wantCollapsed)
			}
			if l.full != tc.wantFull {
				t.Errorf("full = %v, want %v", l.full, tc.wantFull)
			}
		})
	}
}

// Off a terminal there is nobody to press anything, and no cursor control to
// redraw with. Toggling must be inert rather than corrupting a log file with
// a half-drawn region.
func TestCollapsingDoesNothingWithoutCursorControl(t *testing.T) {
	var w bytes.Buffer
	l := &Live{w: &w, cursor: false}

	l.ToggleCollapsed()
	if l.collapsed {
		t.Error("collapsed off a terminal; there is no region to collapse")
	}
	if w.Len() != 0 {
		t.Errorf("wrote %q to a non-terminal", w.String())
	}
}
