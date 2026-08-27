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
