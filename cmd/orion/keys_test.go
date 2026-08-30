package main

import (
	"strings"
	"testing"
)

// The bug this pins:
//
//	orion collect or or-39
//	WARNING   OR: no pull request found for orion/or.
//
// "or" is the PROJECT key, not an issue key, so no ticket, branch or pull
// request could ever be named that. Orion warned about a missing PR two
// screens after the real fault -- a typo on the command line -- and then
// carried on and exited 0, so a bad key in a cron line warned once per tick
// forever with nothing downstream noticing.
func TestAMalformedTicketKeyIsRejected(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want []string // nil means: reject the whole invocation
	}{
		{"a valid key", []string{"OR-39"}, []string{"OR-39"}},
		{"lowercase is upcased, not rejected", []string{"or-39"}, []string{"OR-39"}},
		{"digits in the project key", []string{"AB2C-7"}, []string{"AB2C-7"}},
		{"several valid keys", []string{"or-39", "FCIA-6"}, []string{"OR-39", "FCIA-6"}},
		{"no keys at all is not an error", nil, []string{}},
		{"the project key alone", []string{"or"}, nil},
		{"an empty argument", []string{""}, nil},
		{"a flag that slipped through", []string{"--dry-run"}, nil},
		{"no issue number", []string{"OR-"}, nil},
		{"a number where the project key goes", []string{"1-2"}, nil},
		// The whole invocation fails: doing the valid half and warning about
		// the rest is what made the original bug survive a cron line.
		{"a valid key mixed with an invalid one", []string{"OR-39", "or"}, nil},
		{"the invalid one first", []string{"or", "OR-39"}, nil},
	} {
		got, err := ticketKeys("collect", "[KEY...]", tc.args)
		switch {
		case tc.want == nil && err == nil:
			t.Errorf("%s: ticketKeys(%v) accepted it, want a usage error", tc.name, tc.args)
		case tc.want == nil:
			continue
		case err != nil:
			t.Errorf("%s: ticketKeys(%v) = %v, want %v", tc.name, tc.args, err, tc.want)
		case strings.Join(got, ",") != strings.Join(tc.want, ","):
			t.Errorf("%s: ticketKeys(%v) = %v, want %v", tc.name, tc.args, got, tc.want)
		}
	}
}

// The error has to name the token and the shape, because the shape IS the
// misunderstanding: the user was reaching for a project argument that these
// commands do not take.
func TestTheKeyErrorNamesTheTokenAndTheExpectedShape(t *testing.T) {
	_, err := ticketKeys("collect", "[KEY...]", []string{"or"})
	if err == nil {
		t.Fatal("no error")
	}
	for _, want := range []string{`"or"`, "PROJ-123", "usage: orion collect [KEY...]"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}

// `orion watch` scopes a queue to PROJECTS, so its positionals are the exact
// inverse: FCIA is right and FCIA-6 is the mistake.
func TestWatchTakesProjectKeysNotTicketKeys(t *testing.T) {
	got, err := projectKeys([]string{"fcia", "ORION"})
	if err != nil || strings.Join(got, ",") != "FCIA,ORION" {
		t.Fatalf("projectKeys = %v, %v; want [FCIA ORION]", got, err)
	}
	for _, bad := range []string{"or-39", "", "--dry-run", "1"} {
		if _, err := projectKeys([]string{bad}); err == nil {
			t.Errorf("projectKeys(%q) accepted it, want a usage error", bad)
		}
	}
}

// Bare keys, comma- or space-separated, and INCLUSIVE ranges. The range is
// the reason this command exists: one work block was thirty-six consecutive
// tickets and attaching them meant a scripted REST loop (OR-222).
func TestExpandTicketKeysAcceptsKeysListsAndRanges(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want []string
	}{
		{"a bare key", []string{"OR-100"}, []string{"OR-100"}},
		{"lowercase is upcased", []string{"or-100"}, []string{"OR-100"}},
		{"space separated", []string{"OR-100", "OR-133"}, []string{"OR-100", "OR-133"}},
		{"comma separated in one argument", []string{"OR-100,OR-133"}, []string{"OR-100", "OR-133"}},
		{"commas with spaces", []string{"OR-100, OR-133"}, []string{"OR-100", "OR-133"}},
		// Inclusive at BOTH ends. A half-open range silently drops the last
		// ticket, which is the one nobody would think to check.
		{"an inclusive range", []string{"OR-140..OR-145"},
			[]string{"OR-140", "OR-141", "OR-142", "OR-143", "OR-144", "OR-145"}},
		{"a range of one", []string{"OR-140..OR-140"}, []string{"OR-140"}},
		// Numeric, not lexical: OR-9..OR-11 is three tickets, and a string
		// comparison would call it an empty range.
		{"a range across a digit boundary", []string{"OR-9..OR-11"},
			[]string{"OR-9", "OR-10", "OR-11"}},
		{"keys and a range together", []string{"OR-100", "OR-140..OR-142"},
			[]string{"OR-100", "OR-140", "OR-141", "OR-142"}},
		// The ticket's own example line.
		{"the documented invocation", []string{"OR-100", "OR-133", "OR-140..OR-145"},
			[]string{"OR-100", "OR-133", "OR-140", "OR-141", "OR-142", "OR-143", "OR-144", "OR-145"}},
		// A key named twice, or covered by a range and named again, must be
		// written once -- not queued for two writes of the same field.
		{"an overlapping range and key", []string{"OR-140..OR-142", "OR-141"},
			[]string{"OR-140", "OR-141", "OR-142"}},
		{"a trailing comma", []string{"OR-100,"}, []string{"OR-100"}},
		{"no arguments at all", nil, []string{}},
	} {
		got, err := expandTicketKeys("release add", "<version> <KEY>...", tc.args)
		if err != nil {
			t.Errorf("%s: expandTicketKeys(%v) errored: %v", tc.name, tc.args, err)
			continue
		}
		if strings.Join(got, ",") != strings.Join(tc.want, ",") {
			t.Errorf("%s: expandTicketKeys(%v) = %v, want %v", tc.name, tc.args, got, tc.want)
		}
	}
}

// Every refusal has to NAME the reason. A range expanded to nothing looks
// exactly like success, so "0 tickets" is the worst possible answer to a
// reversed range.
func TestExpandTicketKeysRefusesBadRangesWithTheReason(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want []string // fragments the message must contain
	}{
		{"reversed", []string{"OR-145..OR-140"}, []string{"OR-145..OR-140", "before it starts"}},
		{"cross project", []string{"OR-140..AB-145"}, []string{"two projects", "OR", "AB"}},
		{"a project key as an end", []string{"OR..OR-145"}, []string{"not a ticket key"}},
		{"three ends", []string{"OR-1..OR-2..OR-3"}, []string{"not a range"}},
		{"no left end", []string{"..OR-3"}, []string{"not a range"}},
		{"no right end", []string{"OR-3.."}, []string{"not a range"}},
		{"a plain bad key", []string{"or"}, []string{"not a ticket key or range"}},
		{"a flag that slipped through", []string{"--project"}, []string{"not a ticket key or range"}},
		// A typo is four keystrokes from writing to every ticket in the
		// project, so an absurd range is refused rather than attempted.
		{"a runaway range", []string{"OR-1..OR-99999"}, []string{"99999", "100"}},
	} {
		got, err := expandTicketKeys("release add", "<version> <KEY>...", tc.args)
		if err == nil {
			t.Errorf("%s: expandTicketKeys(%v) = %v, want a refusal", tc.name, tc.args, got)
			continue
		}
		for _, want := range tc.want {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("%s: refusal %q does not say %q", tc.name, err, want)
			}
		}
	}
}
