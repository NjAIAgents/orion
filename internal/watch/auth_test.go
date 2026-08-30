package watch

import (
	"strings"
	"testing"
	"time"

	"github.com/orion-sdlc/orion/internal/work"
)

// A watcher that keeps claiming while the CLI is logged out converts one
// fixable problem into a queue of released tickets. Every subsequent ticket
// fails identically until a human signs in, so there is nothing to wait for
// and nothing to gain by trying the next one (OR-212).
func TestALoggedOutCLIStopsTheWatcherRatherThanDrainingTheQueue(t *testing.T) {
	s := &spy{
		queued:    issues("OR-1", "OR-2", "OR-3", "OR-4"),
		outcome:   work.OutcomeNoAuth,
		maxSleeps: 6,
	}
	d := s.deps()
	inner := d.Work
	d.Work = func(o work.Options) []work.Result {
		res := inner(o)
		res[0].Note = "claude is not authenticated: Anthropic profile login expired. " +
			"Run: claude, sign in, then restart the watcher."
		return res
	}

	t.Setenv("COLUMNS", "")
	stopping.Store(false)
	var buf strings.Builder
	if err := Run(Options{
		Out: &buf, Home: t.TempDir(), Interval: time.Millisecond, MaxConcurrent: 1,
	}, d); err != nil {
		t.Fatal(err)
	}

	if got := s.workedKeys(); len(got) != 1 {
		t.Fatalf("claimed %v; the queue must not be drained against a logged-out CLI", got)
	}
	out := buf.String()
	if !strings.Contains(out, "claude is not authenticated") ||
		!strings.Contains(out, "sign in") {
		t.Errorf("the watcher stopped without naming the cause or the fix:\n%s", out)
	}
}
