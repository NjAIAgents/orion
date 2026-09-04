package collect

import (
	"bytes"
	"strings"
	"testing"
)

// Observed 2026-09-03: "went red; the next pass will isolate the cause",
// then "assembling 3 branch(es)", then "CI is running" -- rebuilt, not
// isolated, on every pass. A known-red set skips the test it already has the
// answer to and begins the search (OR-324).
func TestAKnownRedBatchIsolatesWithoutTestingTheWholeSetAgain(t *testing.T) {
	g := newFakeGit()
	tr := &fakeTester{g: g, bad: map[string]bool{"orion/or-3": true}}

	b, err := Land(g, tr, "batch", "develop", members("OR-1", "OR-2", "OR-3", "OR-4"), nil,
		WithKnownRed())
	if err != nil {
		t.Fatal(err)
	}
	if tr.tested["batch"] {
		t.Error("the whole set was tested again; its verdict was already known")
	}
	if got := b.Members(Culprit); len(got) != 1 || got[0] != "OR-3" {
		t.Errorf("culprit = %v, want [OR-3]: the search must run", got)
	}
	if b.Runs < 2 {
		t.Errorf("Runs = %d: the known-red run counts, and the search spent more", b.Runs)
	}
}

// The red verdict is recorded rather than forgotten, and read back by the
// next pass as a reason to isolate.
func TestARedResumeKeepsARecordThatTheNextPassReadsAsKnownRed(t *testing.T) {
	ms := members("OR-297", "OR-300")
	st, g, ws, cfg := landResumedFixture(t, ms)
	st.Status = batchTesting
	if err := saveBatchState(ws.Dir, st); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	deps := Deps{Jira: newTracker(),
		Status: func(string, string) (PR, error) {
			return PR{Verdict: VerdictFailing, URL: "u", Detail: "go (macos-latest): FAIL"}, nil
		}}

	resumeTesting(st, ms, cfg, Options{}, deps, g, ws, nil, &buf)

	if !strings.Contains(buf.String(), "went red") {
		t.Errorf("the red verdict was not reported:\n%s", buf.String())
	}
	got, ok := loadBatchState(ws.Dir)
	if !ok || got.Status != batchRed {
		t.Fatalf("record after a red run = %+v (present=%v), want status %q", got, ok, batchRed)
	}
	if !knownRed(ws.Dir, "develop", g, ms) {
		t.Error("the next pass must read the record as known red and isolate")
	}
	// A different set over the same base is a new batch, tested from scratch.
	if knownRed(ws.Dir, "develop", g, members("OR-297")) {
		t.Error("a different member set must not inherit the red verdict")
	}
}
