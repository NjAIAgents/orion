package config

import "testing"

// Case assigned under OR-152: discovery.max_rounds defaults to 2 when
// nothing is configured -- stated here as its own assertion so the number is
// not only load-bearing inside DiscoveryRounds' own doc comment.
func TestDiscoveryMaxRoundsDefaultsToTwo(t *testing.T) {
	got := Discovery{}.Rounds()
	if got != 2 {
		t.Errorf("Discovery{}.Rounds() = %d, want 2", got)
	}
	if DiscoveryRounds != 2 {
		t.Errorf("DiscoveryRounds = %d, want 2", DiscoveryRounds)
	}
}
