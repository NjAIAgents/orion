package watch

import (
	"math"
	"testing"
)

// CodeQL go/allocation-size-overflow, high severity, found on the v0.9.0
// promotion. newPool sized its result channel n*4, where n is
// limits.max_concurrent_tickets -- a number a person types into orion.json.
// A large enough n overflows int and make() is asked for a negative length,
// which panics at startup.
//
// The cap itself is preserved: only the BUFFER is bounded, because the
// buffer is an optimisation and the cap is a contract.
func TestThePoolBufferSurvivesAnAbsurdConcurrencyCap(t *testing.T) {
	for _, n := range []int{math.MaxInt, math.MaxInt / 2, math.MaxInt/4 + 1, 1 << 40} {
		p := newPool(n) // must not panic
		if got := cap(p.done); got < 0 {
			t.Errorf("newPool(%d) built a channel with capacity %d", n, got)
		}
		if got := cap(p.done); got > maxPoolBuffer {
			t.Errorf("newPool(%d) buffered %d, above the %d bound", n, got, maxPoolBuffer)
		}
		if p.cap != n {
			t.Errorf("newPool(%d) changed the CAP to %d; only the buffer may be bounded",
				n, p.cap)
		}
	}
}

// The ordinary case is untouched: a realistic cap still gets four slots per
// ticket, which is what the buffer is for.
func TestAnOrdinaryCapStillGetsItsFullBuffer(t *testing.T) {
	for _, n := range []int{1, 2, 4, 8, 64} {
		if got, want := cap(newPool(n).done), n*poolBufferPerSlot; got != want {
			t.Errorf("newPool(%d) buffered %d, want %d", n, got, want)
		}
	}
}

// A nonsensical cap is corrected rather than passed to make().
func TestAZeroOrNegativeCapBecomesOne(t *testing.T) {
	for _, n := range []int{0, -1, math.MinInt} {
		if got := newPool(n).cap; got != 1 {
			t.Errorf("newPool(%d).cap = %d, want 1", n, got)
		}
	}
}
