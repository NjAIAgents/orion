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
	if got := (Limits{MaxConcurrentTickets: -3}).ConcurrentTickets(); got != 2 {
		t.Fatalf("ConcurrentTickets() = %d for a negative value, want the default", got)
	}
}

// The ceiling is a ceiling, and it has to survive a hand-edited file rather
// than only the menu that writes one.
func TestAConfiguredConcurrencyCapIsClampedToTheCeiling(t *testing.T) {
	if got := (Limits{MaxConcurrentTickets: 40}).ConcurrentTickets(); got != MaxConcurrentTicketsCeiling {
		t.Fatalf("ConcurrentTickets() = %d, want the ceiling %d", got, MaxConcurrentTicketsCeiling)
	}

	dir := t.TempDir()
	body := `{"version":1,"limits":{"max_concurrent_tickets":40}}`
	if err := os.WriteFile(filepath.Join(dir, "orion.json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	// Clamped on LOAD too, not only through the accessor: anything reading the
	// field directly must not see 40 either.
	if got := Load(dir).Limits.MaxConcurrentTickets; got != MaxConcurrentTicketsCeiling {
		t.Fatalf("the loaded config carries %d; a hand edit must not widen the control", got)
	}
}

// A value inside the range is honoured exactly -- the point of the setting is
// that an operator who has proved it at two can raise it.
func TestAConfiguredConcurrencyCapInRangeIsHonoured(t *testing.T) {
	dir := t.TempDir()
	body := `{"version":1,"limits":{"max_concurrent_tickets":4}}`
	if err := os.WriteFile(filepath.Join(dir, "orion.json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := Load(dir).Limits.ConcurrentTickets(); got != 4 {
		t.Fatalf("ConcurrentTickets() = %d, want 4", got)
	}
}
