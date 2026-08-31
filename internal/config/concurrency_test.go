package config

import (
	"os"
	"path/filepath"
	"testing"
)

// A repo with no orion.json still gets a real cap. Zero must never read as
// unlimited here: the failure that would cause is an unattended watcher
// starting as many agents as the queue is long.
func TestAnAbsentConcurrencyCapDefaultsRatherThanUnbounding(t *testing.T) {
	if got := (Limits{}).ConcurrentTickets(); got != Defaults().Limits.MaxConcurrentTickets {
		t.Fatalf("ConcurrentTickets() = %d with nothing set, want the shipped default", got)
	}
	// Against Defaults() rather than a literal: this assertion is about a
	// negative value falling back to whatever the shipped default IS, not
	// about what that number happens to be. Hardcoding it means raising the
	// default fails a test that was never testing the default's value.
	if got := (Limits{MaxConcurrentTickets: -3}).ConcurrentTickets(); got != Defaults().Limits.MaxConcurrentTickets {
		t.Fatalf("ConcurrentTickets() = %d for a negative value, want the default", got)
	}
}

// A configured value is HONOURED, however large.
//
// This used to assert the opposite: forty was clamped to a ceiling of five.
// That produced a file saying forty while the watcher ran five, with nothing
// in either place explaining the gap -- so the config could not be trusted to
// describe behaviour. The argument about whether a number is wise now happens
// where it is set (`orion config limits` confirms above
// ConcurrencyWarnAbove), which is the only place with a person to ask.
func TestAConfiguredConcurrencyCapIsHonouredHoweverLarge(t *testing.T) {
	if got := (Limits{MaxConcurrentTickets: 40}).ConcurrentTickets(); got != 40 {
		t.Fatalf("ConcurrentTickets() = %d, want the configured 40", got)
	}

	dir := t.TempDir()
	body := `{"version":1,"limits":{"max_concurrent_tickets":40}}`
	if err := os.WriteFile(filepath.Join(dir, "orion.json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	// Honoured on LOAD too, not only through the accessor: a caller reading
	// the field directly must see the same number the file states.
	if got := Load(dir).Limits.MaxConcurrentTickets; got != 40 {
		t.Fatalf("the loaded config carries %d; it must agree with the file", got)
	}
}

// Zero still means the shipped default and never "unlimited". An absent value
// must not widen a control -- the rule every field in this package follows.
func TestZeroStillMeansTheDefaultRatherThanUnlimited(t *testing.T) {
	if got := (Limits{MaxConcurrentTickets: 0}).ConcurrentTickets(); got != Defaults().Limits.MaxConcurrentTickets {
		t.Fatalf("ConcurrentTickets() = %d for zero, want the shipped default", got)
	}
}

// The warning threshold has to sit above the shipped default, or every
// operator is asked to confirm a number Orion itself chose.
func TestTheWarningThresholdIsAboveTheShippedDefault(t *testing.T) {
	if ConcurrencyWarnAbove <= Defaults().Limits.MaxConcurrentTickets {
		t.Fatalf("ConcurrencyWarnAbove (%d) must exceed the default (%d), or the default prompts",
			ConcurrencyWarnAbove, Defaults().Limits.MaxConcurrentTickets)
	}
}
