package watch

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/orion-sdlc/orion/internal/queue"
	"github.com/orion-sdlc/orion/internal/registry"
	"github.com/orion-sdlc/orion/internal/tracker"
)

// seedFixRounds writes a ci-fixes.json directly, the same file
// collect.FixRounds reads, so a test can assert on a real reader without
// needing an unexported writer from another package.
func seedFixRounds(t *testing.T, wsDir, key string, attempts int) {
	t.Helper()
	type attempt struct {
		At time.Time `json:"at"`
	}
	type fixState struct {
		Key      string    `json:"key"`
		Attempts []attempt `json:"attempts"`
	}
	type fixFile struct {
		Version int                 `json:"version"`
		States  map[string]fixState `json:"states"`
	}
	f := fixFile{Version: 1, States: map[string]fixState{}}
	var atts []attempt
	for i := 0; i < attempts; i++ {
		atts = append(atts, attempt{At: time.Now()})
	}
	f.States[key] = fixState{Key: key, Attempts: atts}
	b, err := json.Marshal(f)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(wsDir, ".orion")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "ci-fixes.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
}

// OR-456: fixRoundsLookup used to not exist at all -- queue.Facts.FixRounds
// was always nil in the live watch loop, so a ticket could spend an
// unbounded number of CI fix rounds and never be evicted for it.
func TestFixRoundsLookupBridgesTheRegistryToTheWorkspace(t *testing.T) {
	home := t.TempDir()
	ws := t.TempDir()
	if err := registry.Save(home, &registry.File{Repos: map[string]registry.Entry{
		"OR": {Key: "OR", Source: t.TempDir(), Workspace: ws},
	}}); err != nil {
		t.Fatal(err)
	}
	seedFixRounds(t, ws, "OR-1", 3)

	lookup := fixRoundsLookup(home)
	n, known := lookup("OR-1")
	if !known || n != 3 {
		t.Errorf("fixRoundsLookup(OR-1) = (%d, %v), want (3, true)", n, known)
	}

	// A key the registry has never heard of is unknown, not zero.
	if _, known := lookup("NOPE-1"); known {
		t.Error("an unregistered ticket must be unknown, not a reading of zero")
	}
}

// OR-456: maxFixRounds used to not exist -- the ceiling queue.Plan checks
// FixRounds against was always zero, which disables the rule entirely
// (queue.Facts's own doc: "Zero disables that rule").
func TestMaxFixRoundsReadsTheSmallestConfiguredCeiling(t *testing.T) {
	home := t.TempDir()
	repoA := t.TempDir()
	repoB := t.TempDir()
	writeOrionJSON(t, repoA, `{"version":1,"ci":{"max_fix_attempts":5}}`)
	writeOrionJSON(t, repoB, `{"version":1,"ci":{"max_fix_attempts":2}}`)
	if err := registry.Save(home, &registry.File{Repos: map[string]registry.Entry{
		"A": {Key: "A", Source: repoA},
		"B": {Key: "B", Source: repoB},
	}}); err != nil {
		t.Fatal(err)
	}

	if n := maxFixRounds(home, []string{"A", "B"}); n != 2 {
		t.Errorf("maxFixRounds = %d, want 2 (the smaller of the two projects)", n)
	}
}

func writeOrionJSON(t *testing.T, dir, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "orion.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// OR-456 end to end: a ticket evicted once must be evicted again on a plain
// re-run of Plan, and escalated -- not evicted a third time -- once the
// ledger actually records two priors. Before this fix the ledger passed to
// Plan was always the zero value, so Escalate could never fire no matter how
// many times a ticket had really been evicted.
func TestQueuedEscalatesATicketEvictedTwiceRatherThanEvictingAgain(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/project/OR/versions"):
			_, _ = w.Write([]byte(`[]`))
		case strings.Contains(r.URL.Path, "/search/jql"):
			_, _ = w.Write([]byte(`{"issues":[{"key":"OR-1","fields":{"labels":["ORION"]}}]}`))
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()

	home := t.TempDir()
	ws := t.TempDir()
	// Past the ceiling, so this candidate evicts on "rounds" every pass.
	repo := t.TempDir()
	writeOrionJSON(t, repo, `{"version":1,"ci":{"max_fix_attempts":1}}`)
	if err := registry.Save(home, &registry.File{Repos: map[string]registry.Entry{
		"OR": {Key: "OR", Source: repo, Workspace: ws},
	}}); err != nil {
		t.Fatal(err)
	}
	seedFixRounds(t, ws, "OR-1", 1)

	if _, err := Queued(&tracker.Jira{BaseURL: srv.URL}, home, []string{"OR"}, "ORION"); err != nil {
		t.Fatal(err)
	}
	if _, err := Queued(&tracker.Jira{BaseURL: srv.URL}, home, []string{"OR"}, "ORION"); err != nil {
		t.Fatal(err)
	}

	l := queue.LoadLedger(home)
	if got := l.Count("OR-1"); got != 2 {
		t.Fatalf("ledger recorded %d evictions after two passes, want 2 (was the ledger ever saved?)", got)
	}

	q, err := Queued(&tracker.Jira{BaseURL: srv.URL}, home, []string{"OR"}, "ORION")
	if err != nil {
		t.Fatal(err)
	}
	if len(q.Held) != 1 {
		t.Fatalf("want exactly one held ticket on the third pass, got %+v", q.Held)
	}
	if !strings.Contains(q.Held[0].Reason, "evicted 2 times already") {
		t.Errorf("third pass reason = %q, want it to escalate rather than evict a third time", q.Held[0].Reason)
	}
}
