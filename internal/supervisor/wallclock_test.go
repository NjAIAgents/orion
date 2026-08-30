package supervisor

import (
	"testing"
	"time"
)

// TestWallClockDefaultsAreProductionValues guards the OR-202 injectability
// change: wallClockUnit and graceTimeout are package variables so a test can
// shrink them, but every test that shrinks them restores via t.Cleanup, so
// whichever test runs next -- and production, which never calls
// shrinkWallClock -- must see the real minute and the real eight-second
// grace. If a cleanup were missing or ordered wrong, this would catch a
// polluted value leaking across tests.
func TestWallClockDefaultsAreProductionValues(t *testing.T) {
	if wallClockUnit != time.Minute {
		t.Fatalf("wallClockUnit = %s, want %s (production MaxMinutes unit)", wallClockUnit, time.Minute)
	}
	if graceTimeout != 8*time.Second {
		t.Fatalf("graceTimeout = %s, want %s (production SIGINT grace)", graceTimeout, 8*time.Second)
	}
}

// TestShrinkWallClockRestoresOnCleanup proves shrinkWallClock's restore
// actually fires when its subtest ends, rather than only being trusted by
// convention. A cleanup that failed to run would leave a later test racing
// a millisecond deadline it never asked for.
func TestShrinkWallClockRestoresOnCleanup(t *testing.T) {
	before := wallClockUnit
	beforeGrace := graceTimeout

	t.Run("shrunk", func(t *testing.T) {
		shrinkWallClock(t, 250*time.Millisecond, 100*time.Millisecond)
		if wallClockUnit != 250*time.Millisecond {
			t.Fatalf("wallClockUnit = %s, want 250ms while shrunk", wallClockUnit)
		}
		if graceTimeout != 100*time.Millisecond {
			t.Fatalf("graceTimeout = %s, want 100ms while shrunk", graceTimeout)
		}
	})

	if wallClockUnit != before {
		t.Fatalf("wallClockUnit leaked past subtest cleanup: got %s, want %s", wallClockUnit, before)
	}
	if graceTimeout != beforeGrace {
		t.Fatalf("graceTimeout leaked past subtest cleanup: got %s, want %s", graceTimeout, beforeGrace)
	}
}
