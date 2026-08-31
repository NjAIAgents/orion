package watch

import (
	"os"
	"testing"

	"github.com/orion-sdlc/orion/internal/cost"
)

// medianFor feeds the live region's progress bar (OR-240). An unreadable
// usage history is not a watcher fault: the row must simply show no bar,
// never crash the tick that asked for one.
func TestMedianForUnreadableHistoryYieldsNoMedian(t *testing.T) {
	home := t.TempDir()
	// A directory where the history file is expected makes os.ReadFile fail
	// deterministically, unlike chmod, which root ignores.
	if err := os.Mkdir(cost.HistoryPath(home), 0o755); err != nil {
		t.Fatalf("setting up an unreadable history: %v", err)
	}

	f := medianFor(home, nil)
	if got := f("implementer"); got != 0 {
		t.Errorf("an unreadable history must yield no median, got %v", got)
	}
}

// A history with too few completed runs is the ordinary case for a fresh
// project, not an error -- and must degrade the same way, to no bar.
func TestMedianForTooFewRunsYieldsNoMedian(t *testing.T) {
	home := t.TempDir()
	if err := os.WriteFile(cost.HistoryPath(home), []byte(""), 0o644); err != nil {
		t.Fatalf("writing an empty history: %v", err)
	}
	f := medianFor(home, nil)
	if got := f("implementer"); got != 0 {
		t.Errorf("an empty history must yield no median, got %v", got)
	}
}
