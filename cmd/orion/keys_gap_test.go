package main

// QA pass on OR-222: a couple of boundary/formatting cases the spec called
// out that keys_test.go did not yet cover.

import (
	"fmt"
	"strings"
	"testing"
)

// A range of exactly the cap must be ACCEPTED -- the cap is on what is over
// the limit, not on the limit itself. Off-by-one here would refuse the
// largest batch the command is supposed to allow.
func TestExpandTicketKeysAcceptsARangeOfExactlyTheCap(t *testing.T) {
	got, err := expandTicketKeys("release add", "<version> <KEY>...",
		[]string{fmt.Sprintf("OR-1..OR-%d", maxExpandedKeys)})
	if err != nil {
		t.Fatalf("a range of exactly %d keys was refused: %v", maxExpandedKeys, err)
	}
	if len(got) != maxExpandedKeys {
		t.Fatalf("got %d keys, want %d", len(got), maxExpandedKeys)
	}
	if got[0] != "OR-1" || got[len(got)-1] != fmt.Sprintf("OR-%d", maxExpandedKeys) {
		t.Errorf("range endpoints are %s..%s, want OR-1..OR-%d",
			got[0], got[len(got)-1], maxExpandedKeys)
	}
}

// A comma with a space only on the LEFT side (space before, none after) is
// the mirror of the "commas with spaces" case already covered, and must be
// trimmed the same way.
func TestExpandTicketKeysTrimsASpaceBeforeTheComma(t *testing.T) {
	got, err := expandTicketKeys("release add", "<version> <KEY>...",
		[]string{"OR-100 ,OR-133"})
	if err != nil {
		t.Fatalf("errored: %v", err)
	}
	if strings.Join(got, ",") != "OR-100,OR-133" {
		t.Errorf("got %v, want [OR-100 OR-133]", got)
	}
}

// A leading comma -- ",OR-100" -- is the same shape as a trailing one
// (already covered) and must be skipped rather than rejected as an empty
// token.
func TestExpandTicketKeysSkipsALeadingComma(t *testing.T) {
	got, err := expandTicketKeys("release add", "<version> <KEY>...",
		[]string{",OR-100"})
	if err != nil {
		t.Fatalf("errored: %v", err)
	}
	if strings.Join(got, ",") != "OR-100" {
		t.Errorf("got %v, want [OR-100]", got)
	}
}
