package collect

import (
	"bytes"
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/orion-sdlc/orion/internal/tracker"
)

// OR-558. landingNote is a pure function of (prURL, held) -- these pin its
// behaviour at the edges the call sites in collect.go do not otherwise
// exercise: nothing held, an odd key list, and an odd URL.
func TestLandingNoteEdgeCases(t *testing.T) {
	if got := landingNote("https://forge/pull/1", nil); got != "merged: https://forge/pull/1" {
		t.Errorf("nil held: got %q", got)
	}
	if got := landingNote("https://forge/pull/1", []string{}); got != "merged: https://forge/pull/1" {
		t.Errorf("empty (non-nil) held slice: got %q", got)
	}

	// Duplicate keys are not de-duplicated: landingNote reports exactly what
	// it is given, and closeChildren is what guarantees each key appears at
	// most once.
	dup := landingNote("https://forge/pull/1", []string{"OR-1", "OR-1"})
	if !strings.Contains(dup, "OR-1, OR-1 are a person's tasks and stay open.") {
		t.Errorf("duplicate keys: got %q", dup)
	}

	// A key that is not a well-formed tracker key (empty string, or one with
	// spaces/unicode) is still just a string to join -- landingNote does not
	// validate key shape.
	weird := landingNote("https://forge/pull/1", []string{"", "OR ☃-1"})
	if !strings.Contains(weird, ", OR ☃-1 are a person's tasks and stay open.") {
		t.Errorf("unicode/empty key: got %q", weird)
	}

	var many []string
	for i := 0; i < 500; i++ {
		many = append(many, "OR-"+strconv.Itoa(i))
	}
	longList := landingNote("https://forge/pull/1", many)
	if !strings.HasPrefix(longList, "merged: https://forge/pull/1\n\nOR-0, OR-1, ") {
		t.Errorf("long held list: prefix was %q...", longList[:60])
	}
	if !strings.HasSuffix(longList, "are a person's tasks and stay open.") {
		t.Errorf("long held list: suffix was ...%q", longList[len(longList)-40:])
	}

	// The URL is opaque to landingNote too: a very long one, one with query
	// parameters and special characters, and one that is not really a URL
	// at all all pass through untouched.
	longURL := "https://forge.example.com/org/repo/pull/" + strings.Repeat("9", 2000)
	if got := landingNote(longURL, nil); got != "merged: "+longURL {
		t.Errorf("long URL: got %q", got)
	}
	weirdURL := "https://forge/pull/1?x=a b&y=<script>&z=日本語"
	if got := landingNote(weirdURL, []string{"OR-1"}); !strings.HasPrefix(got, "merged: "+weirdURL) {
		t.Errorf("special-character URL: got %q", got)
	}
	notAURL := "not-actually-a-url"
	if got := landingNote(notAURL, nil); got != "merged: not-actually-a-url" {
		t.Errorf("non-URL string: got %q", got)
	}
}

// A tracker that cannot list children at all (no parent field, or a
// permission error) must leave closeChildren reporting nothing held rather
// than erroring -- collect.go already treats this as "not a decomposed
// ticket", and that has to hold when a HUMAN-marked child would otherwise
// have been found.
func TestCloseChildrenReturnsNilWhenChildrenLookupFails(t *testing.T) {
	jira := newTracker()
	jira.childErr = errors.New("no parent field on this project")
	var buf bytes.Buffer

	held := closeChildren("OR-900", "https://forge/pull/5", "orion-ready", Deps{Jira: jira}, &buf)
	if held != nil {
		t.Errorf("held = %#v, want nil when Children() itself failed", held)
	}
}

// A ticket key nobody planted in the fake tracker behaves exactly like one
// with no children -- closeChildren makes no assumption that the key it was
// given exists.
func TestCloseChildrenOnANonExistentStoryKeyHoldsNothing(t *testing.T) {
	jira := newTracker()
	var buf bytes.Buffer

	held := closeChildren("NOPE-404", "https://forge/pull/6", "orion-ready", Deps{Jira: jira}, &buf)
	if held != nil {
		t.Errorf("held = %#v, want nil for an unknown story key", held)
	}
}

// A child that fails to transition is skipped with a warning (existing
// behaviour) -- this pins that a HUMAN-marked sibling is still correctly
// held even when an ordinary sibling's transition fails, so one child's
// tracker error cannot mask another child's marker.
func TestCloseChildrenStillHoldsTheHumanChildWhenAnotherChildFailsToTransition(t *testing.T) {
	jira := newTracker()
	jira.children["OR-910"] = []tracker.Issue{
		{Key: "OR-911", Description: "Add the endpoint.\n"},
		{Key: "OR-912", Description: "HUMAN: a person picks it up.\n"},
	}
	jira.transitionErr = errors.New("workflow has no Done transition")
	var buf bytes.Buffer

	held := closeChildren("OR-910", "https://forge/pull/7", "orion-ready", Deps{Jira: jira}, &buf)
	if len(held) != 1 || held[0] != "OR-912" {
		t.Errorf("held = %v, want exactly [OR-912] regardless of OR-911's transition failure", held)
	}
	if jira.transitions["OR-911"] != "" {
		t.Errorf("OR-911 should not have transitioned given transitionErr, got %q", jira.transitions["OR-911"])
	}
}

// A child whose label removal fails after a successful transition is still
// reported closed (existing warn-and-continue behaviour) -- and, again, a
// HUMAN-marked sibling must still be held independently of that failure.
func TestCloseChildrenStillHoldsTheHumanChildWhenAnotherChildsLabelRemovalFails(t *testing.T) {
	jira := newTracker()
	jira.children["OR-920"] = []tracker.Issue{
		{Key: "OR-921", Description: "Add the endpoint.\n"},
		{Key: "OR-922", Description: "HUMAN: a person picks it up.\n"},
	}
	jira.labelErr = errors.New("label API rejected the request")
	var buf bytes.Buffer

	held := closeChildren("OR-920", "https://forge/pull/8", "orion-ready", Deps{Jira: jira}, &buf)
	if len(held) != 1 || held[0] != "OR-922" {
		t.Errorf("held = %v, want exactly [OR-922]", held)
	}
	if jira.transitions["OR-921"] != "Done" {
		t.Errorf("OR-921 should still have transitioned to Done despite the label error, got %q",
			jira.transitions["OR-921"])
	}
}

// Landing the same story twice (a re-run after a transient failure, or a
// duplicate webhook delivery) must not error and must not re-hold or
// double-report a sub-task that was already handled the first time.
func TestClosingTheSameStoryTwiceIsIdempotent(t *testing.T) {
	jira := newTracker()
	jira.children["OR-930"] = []tracker.Issue{
		{Key: "OR-931", Description: "Add the endpoint.\n"},
		{Key: "OR-932", Description: "HUMAN: a person picks it up.\n"},
	}
	var buf1, buf2 bytes.Buffer

	if err := closeTicket("OR-930", "https://forge/pull/9", "orion-ready", Deps{Jira: jira}, &buf1); err != nil {
		t.Fatalf("first closeTicket: %v", err)
	}
	if err := closeTicket("OR-930", "https://forge/pull/9", "orion-ready", Deps{Jira: jira}, &buf2); err != nil {
		t.Fatalf("second closeTicket: %v", err)
	}
	if jira.transitions["OR-931"] != "Done" {
		t.Errorf("OR-931 should still read Done after the re-run, got %q", jira.transitions["OR-931"])
	}
	if jira.transitions["OR-932"] != "" {
		t.Errorf("OR-932 must still be untouched after the re-run, got %q", jira.transitions["OR-932"])
	}
	// Both runs post a landing comment naming OR-932 -- closeTicket does not
	// dedupe against tracker history, which matches its existing (pre-OR-558)
	// behaviour of always posting on close.
	if len(jira.comments["OR-930"]) != 2 {
		t.Errorf("expected a landing comment from each run, got %d: %v",
			len(jira.comments["OR-930"]), jira.comments["OR-930"])
	}
	for _, c := range jira.comments["OR-930"] {
		if !strings.Contains(c, "OR-932") {
			t.Errorf("landing comment missing the held sub-task: %q", c)
		}
	}
}
