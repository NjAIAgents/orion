package main

import (
	"strings"
	"testing"

	"github.com/orion-sdlc/orion/internal/tracker"
)

func TestFieldPairsAreParsedInOrder(t *testing.T) {
	got, err := parseIdeaFields([]string{
		"--field", "Theme", "--value", "Delight users",
		"--field", "Roadmap", "--value", "Next",
	})
	if err != nil {
		t.Fatalf("parseIdeaFields: %v", err)
	}
	want := []tracker.IdeaField{
		{Name: "Theme", Value: "Delight users"},
		{Name: "Roadmap", Value: "Next"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d pairs, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("pair %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

// A mismatched pair must be caught rather than silently pairing the wrong
// name with the wrong value -- which would write a real value into a field
// nobody named.
func TestAMismatchedPairIsRefused(t *testing.T) {
	for _, args := range [][]string{
		{"--field", "Theme", "--field", "Roadmap", "--value", "Next"},
		{"--field", "Theme"},
		{"--value", "Next"},
		{"--field", "Theme", "--value"},
		{"Theme", "Next"},
	} {
		if _, err := parseIdeaFields(args); err == nil {
			t.Errorf("%v was accepted", args)
		}
	}
}

func TestTheFirstSentenceIsTheOriginatorsOwnOpening(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"Build a thing. Then another thing.", "Build a thing."},
		{"One line only", "One line only"},
		{"First line\nsecond line", "First line"},
		{"From PRIOR-3 (url)\n\nThe real opening. More.", "The real opening."},
		{"", ""},
	} {
		if got := firstSentence(tc.in); got != tc.want {
			t.Errorf("firstSentence(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// Jira rejects a summary over 255 characters outright, and this field has the
// same limit.
func TestALongOpeningIsTruncatedRatherThanRejected(t *testing.T) {
	got := firstSentence(strings.Repeat("x", 400))
	if len(got) > 255 {
		t.Errorf("length %d exceeds Jira's limit", len(got))
	}
	if !strings.HasSuffix(got, "...") {
		t.Error("a truncated line does not show that it was cut")
	}
}
