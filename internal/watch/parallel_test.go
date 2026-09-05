package watch

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/orion-sdlc/orion/internal/config"
	"github.com/orion-sdlc/orion/internal/registry"
	"github.com/orion-sdlc/orion/internal/tracker"
)

// Concurrency does not cause merge conflicts; picking n tickets that all edit
// the same files does. On 2026-08-29 five queued tickets all touched the fix
// loop, the activity logger and the notify path, and taking the top two by
// priority produced a hand-resolved three-way conflict.
func TestPickSpreadsAcrossAreasRatherThanTakingTheTopN(t *testing.T) {
	queued := []tracker.Issue{
		{Key: "OR-1", Components: []string{"watch"}},
		{Key: "OR-2", Components: []string{"watch"}}, // same area as the head
		{Key: "OR-3", Components: []string{"notify"}},
	}
	got, _ := pick(queued, 2)

	if len(got) != 2 {
		t.Fatalf("picked %v, want 2", got)
	}
	if got[0] != "OR-1" {
		t.Errorf("picked %v; the head of the queue must still go first", got)
	}
	if got[1] != "OR-3" {
		t.Errorf("picked %v; OR-2 shares an area with OR-1 and OR-3 does not", got)
	}
}

// A reordering, not a filter. Once every area is represented the rest are
// taken in the tracker's own priority order -- refusing to fill a slot would
// idle an agent to avoid a conflict that may not exist.
func TestPickFallsBackToPriorityOrderWhenEveryTicketSharesAnArea(t *testing.T) {
	queued := []tracker.Issue{
		{Key: "OR-1", Components: []string{"watch"}},
		{Key: "OR-2", Components: []string{"watch"}},
		{Key: "OR-3", Components: []string{"watch"}},
	}
	got, _ := pick(queued, 2)

	if len(got) != 2 || got[0] != "OR-1" || got[1] != "OR-2" {
		t.Fatalf("picked %v, want [OR-1 OR-2]: with nothing to spread across, rank decides", got)
	}
}

// With one slot the spread must change nothing at all: the ordering a person
// expressed by ranking their backlog is the whole point of a queue, and there
// is no conflict to avoid when only one ticket runs.
func TestPickAtOneIsExactlyTheHeadOfTheQueue(t *testing.T) {
	queued := []tracker.Issue{
		{Key: "OR-1", Components: []string{"watch"}},
		{Key: "OR-2", Components: []string{"notify"}},
	}
	if got, _ := pick(queued, 1); len(got) != 1 || got[0] != "OR-1" {
		t.Fatalf("picked %v, want [OR-1]", got)
	}
}

// With no components at all the project is the area, so a watcher spanning two
// projects still spreads rather than draining one backlog first.
func TestPickTreatsTheProjectAsTheAreaWhenThereAreNoComponents(t *testing.T) {
	queued := []tracker.Issue{{Key: "OR-1"}, {Key: "OR-2"}, {Key: "FCIA-9"}}
	got, _ := pick(queued, 2)

	if len(got) != 2 || got[1] != "FCIA-9" {
		t.Fatalf("picked %v; a second project is a different area", got)
	}
}

// A ticket this watcher itself dispatched still holds the claim label, so the
// tracker's answer and this process's goroutines overlap. Counting them
// separately would double-count every running job and halve the cap.
func TestOwnClaimsAreNotCountedTwice(t *testing.T) {
	got := claimedElsewhere([]string{"OR-1", "or-2", "OR-3"}, []string{"OR-1", "OR-2"})
	if len(got) != 1 || got[0] != "OR-3" {
		t.Fatalf("claimedElsewhere = %v, want [OR-3] -- case included", got)
	}
}

// withProjectCap plants an orion.json carrying a concurrency cap in a fake
// source checkout and registers it.
func withProjectCap(t *testing.T, home, key string, n int) {
	t.Helper()
	dir := t.TempDir()
	body := `{"version":1,"limits":{"max_concurrent_tickets":` + strconv.Itoa(n) + `}}`
	if err := os.WriteFile(filepath.Join(dir, "orion.json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := registry.Bind(home, registry.Entry{
		Key: key, Source: dir, Workspace: "ws-" + key,
	}); err != nil {
		t.Fatal(err)
	}
}

// The cap comes from the project's own orion.json, and its source is
// nameable: the setting that decides how much money is in flight at once is
// read from a file the operator may never have opened.
func TestConcurrencyIsReadFromTheProjectConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("ORION_HOME", home)
	withProjectCap(t, home, "OR", 3)

	n, from := Concurrency(home, []string{"OR"})
	if n != 3 {
		t.Fatalf("Concurrency = %d, want 3 from limits.max_concurrent_tickets", n)
	}
	if !strings.Contains(from, "max_concurrent_tickets") {
		t.Errorf("the source must be nameable in the banner, got %q", from)
	}
}

// A watcher spans several projects and the cap is one number, so it has to be
// reconciled somehow. The only safe direction is down: taking the largest
// would let one repository raise the concurrency of another's.
func TestConcurrencyTakesTheSmallestCapAcrossWatchedProjects(t *testing.T) {
	home := t.TempDir()
	t.Setenv("ORION_HOME", home)
	withProjectCap(t, home, "OR", 4)
	withProjectCap(t, home, "FCIA", 1)

	if n, _ := Concurrency(home, nil); n != 1 {
		t.Fatalf("Concurrency = %d, want 1: the strictest project decides", n)
	}
}

// A config asking for forty gets forty. There is no ceiling: the watcher
// honours what the operator set, and `orion config limits` is where a large
// number is questioned.
func TestConcurrencyHonoursALargeConfiguredValue(t *testing.T) {
	home := t.TempDir()
	t.Setenv("ORION_HOME", home)
	withProjectCap(t, home, "OR", 40)

	if n, _ := Concurrency(home, []string{"OR"}); n != 40 {
		t.Fatalf("Concurrency = %d, want the configured 40", n)
	}
}

// Nothing registered yet is a normal state, not a reason to refuse or to run
// unbounded.
func TestConcurrencyFallsBackToTheDefaultWithNoProjects(t *testing.T) {
	home := t.TempDir()
	t.Setenv("ORION_HOME", home)

	n, from := Concurrency(home, nil)
	if n != config.Defaults().Limits.MaxConcurrentTickets {
		t.Fatalf("Concurrency = %d, want the shipped default", n)
	}
	if from == "" {
		t.Error("the banner still has to say where the number came from")
	}
}
