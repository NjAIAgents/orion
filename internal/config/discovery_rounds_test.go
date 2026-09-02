package config

import "testing"

// The ceiling has to have a value even when nobody set one: an absent
// discovery block must not read as "no bound", which is the state OR-152
// exists to make impossible.
func TestDiscoveryRoundsDefaults(t *testing.T) {
	for _, tc := range []struct {
		name string
		d    Discovery
		want int
	}{
		{"absent", Discovery{}, DiscoveryRounds},
		{"zero restores the default", Discovery{MaxRounds: 0}, DiscoveryRounds},
		{"set", Discovery{MaxRounds: 4}, 4},
	} {
		if got := tc.d.Rounds(); got != tc.want {
			t.Errorf("%s: Rounds() = %d, want %d", tc.name, got, tc.want)
		}
	}
}

// Two, and stated here rather than only in a comment: this is the number the
// ticket asked for, and a silent drift to FixRounds' three would triple-book
// the most expensive round in the system without anyone deciding to.
func TestDiscoveryCeilingIsTwo(t *testing.T) {
	if DiscoveryRounds != 2 {
		t.Errorf("DiscoveryRounds = %d, want 2", DiscoveryRounds)
	}
}

// It is read off the config a repository actually ships, not just off the
// struct: a field nothing parses is a knob that silently does nothing.
func TestDiscoveryRoundsAreReadFromJSON(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, `{"version":1,"discovery":{"max_rounds":5}}`)
	if got := Load(dir).Discovery.Rounds(); got != 5 {
		t.Errorf("Discovery.Rounds() = %d after loading max_rounds 5", got)
	}

	dir = t.TempDir()
	writeConfig(t, dir, `{"version":1}`)
	if got := Load(dir).Discovery.Rounds(); got != DiscoveryRounds {
		t.Errorf("a config with no discovery block read %d, want the default %d",
			got, DiscoveryRounds)
	}
}
