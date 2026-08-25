package lessons

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newStore(t *testing.T) *Store {
	t.Helper()
	return New(t.TempDir())
}

func TestAppendAndLoad(t *testing.T) {
	s := newStore(t)
	if err := s.Append(Lesson{Text: "Money is BigDecimal, never double", Project: "p1", Kind: KindCorrection}); err != nil {
		t.Fatal(err)
	}
	rs, err := s.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(rs) != 1 {
		t.Fatalf("got %d records, want 1", len(rs))
	}
	if rs[0].Scope != ScopeProject {
		t.Errorf("a new lesson must start project-scoped, got %s", rs[0].Scope)
	}
}

func TestDedupBySignature(t *testing.T) {
	s := newStore(t)
	s.Append(Lesson{Text: "Do not bump dependency versions", Project: "p1"})
	s.Append(Lesson{Text: "do not bump dependency versions.", Project: "p1"})
	s.Append(Lesson{Text: "Do  not   bump dependency versions", Project: "p1"})

	rs, _ := s.Load()
	if len(rs) != 1 {
		t.Fatalf("case, punctuation and whitespace variants must collapse; got %d records", len(rs))
	}
	if rs[0].Hits != 3 {
		t.Errorf("Hits = %d, want 3", rs[0].Hits)
	}
}

// The central safety property: a lesson learned in one project must not
// leak into an unrelated one until it has actually recurred elsewhere.
func TestNoPromotionWithoutCrossProjectEvidence(t *testing.T) {
	s := newStore(t)
	for i := 0; i < 5; i++ {
		s.Append(Lesson{Text: "Use the v2 package, v1 is frozen", Project: "p1"})
	}
	rs, _ := s.Load()
	if got := s.AutoPromote(rs); len(got) != 0 {
		t.Fatal("recurring five times inside ONE project proves nothing about other projects")
	}

	applicable := Applicable(rs, "p2", "go")
	if len(applicable) != 0 {
		t.Error("a project-scoped lesson must not reach a different project")
	}
	if len(Applicable(rs, "p1", "go")) != 1 {
		t.Error("it must still apply in its own project")
	}
}

func TestPromotionOnCrossProjectRecurrence(t *testing.T) {
	s := newStore(t)
	s.Append(Lesson{Text: "Never retry a failed migration blindly", Project: "p1", Stack: "go"})
	s.Append(Lesson{Text: "Never retry a failed migration blindly", Project: "p2", Stack: "go"})

	rs, _ := s.Load()
	promoted := s.AutoPromote(rs)
	if len(promoted) != 1 {
		t.Fatalf("recurrence in a second project should promote; got %d", len(promoted))
	}
	if promoted[0].Scope == ScopeProject {
		t.Error("scope should have risen above project")
	}
	if !strings.Contains(promoted[0].Evidence, "p1") || !strings.Contains(promoted[0].Evidence, "p2") {
		t.Errorf("promotion must record its evidence, got %q", promoted[0].Evidence)
	}
}

func TestStackScopeOnlyAppliesToMatchingStack(t *testing.T) {
	rs := []Record{{
		Lesson:   Lesson{Text: "Pin the toolchain", Scope: ScopeStack, Stack: "go"},
		Hits:     2,
		LastSeen: time.Now(),
	}}
	if len(Applicable(rs, "any", "go")) != 1 {
		t.Error("should apply to a matching stack")
	}
	if len(Applicable(rs, "any", "node")) != 0 {
		t.Error("must not leak into a different stack")
	}
	if len(Applicable(rs, "any", "")) != 0 {
		t.Error("unknown stack must not receive stack-scoped advice")
	}
}

func TestExpiryAndRetirement(t *testing.T) {
	old := time.Now().Add(-Expiry - time.Hour)
	rs := []Record{
		{Lesson: Lesson{Text: "stale", Scope: ScopeGlobal}, Hits: 9, LastSeen: old},
		{Lesson: Lesson{Text: "retired", Scope: ScopeGlobal, Retired: true}, Hits: 9, LastSeen: time.Now()},
		{Lesson: Lesson{Text: "live", Scope: ScopeGlobal}, Hits: 1, LastSeen: time.Now()},
	}
	got := Applicable(rs, "p", "go")
	if len(got) != 1 || got[0].Text != "live" {
		t.Errorf("only the live lesson should apply, got %+v", got)
	}
}

// Unbounded growth quietly degrades every session, because the agent
// reads the whole file each time.
func TestRenderedBlockIsCapped(t *testing.T) {
	var rs []Record
	for i := 0; i < MaxRendered*3; i++ {
		rs = append(rs, Record{
			Lesson:   Lesson{Text: "lesson " + string(rune('a'+i%26)) + string(rune('0'+i/26)), Scope: ScopeGlobal},
			Hits:     1,
			LastSeen: time.Now(),
		})
	}
	if got := len(Applicable(rs, "p", "go")); got > MaxRendered {
		t.Errorf("applied %d lessons, cap is %d", got, MaxRendered)
	}
}

func TestRankingPrefersRecentAndHumanCorrections(t *testing.T) {
	rs := []Record{
		{Lesson: Lesson{Text: "ancient", Scope: ScopeGlobal, Kind: KindBreaker}, Hits: 20, LastSeen: time.Now().Add(-80 * 24 * time.Hour)},
		{Lesson: Lesson{Text: "recent", Scope: ScopeGlobal, Kind: KindCorrection}, Hits: 3, LastSeen: time.Now()},
	}
	got := Applicable(rs, "p", "go")
	if got[0].Text != "recent" {
		t.Error("a fresh human correction should outrank a stale heuristic; the codebase it described may be gone")
	}
}

// Destroying a team's own CLAUDE.md notes is the fastest way to get a
// tool turned off.
func TestInjectPreservesHandWrittenContent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "CLAUDE.md")
	original := "# My project\n\n## Commands\n- make test\n"
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}

	block := Render([]Record{{Lesson: Lesson{Text: "first"}, Hits: 1, LastSeen: time.Now()}})
	if err := Inject(path, block); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(path)
	if !strings.Contains(string(got), "## Commands") {
		t.Fatal("hand-written content was destroyed")
	}
	if !strings.Contains(string(got), "first") {
		t.Fatal("lesson block was not injected")
	}

	// A second injection must replace the block, not stack another copy.
	block2 := Render([]Record{{Lesson: Lesson{Text: "second"}, Hits: 1, LastSeen: time.Now()}})
	if err := Inject(path, block2); err != nil {
		t.Fatal(err)
	}
	got2 := string(mustRead(t, path))
	if strings.Count(got2, beginMarker) != 1 {
		t.Errorf("expected exactly one managed block, got %d", strings.Count(got2, beginMarker))
	}
	if strings.Contains(got2, "first") {
		t.Error("the previous block should have been replaced")
	}
	if !strings.Contains(got2, "## Commands") {
		t.Error("hand-written content must survive repeated injections")
	}
}

func TestInjectCreatesFileWhenAbsent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "CLAUDE.md")
	block := Render([]Record{{Lesson: Lesson{Text: "x"}, Hits: 1, LastSeen: time.Now()}})
	if err := Inject(path, block); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(mustRead(t, path)), "x") {
		t.Error("should have created the file")
	}
}

func TestEmptyTextRejected(t *testing.T) {
	if err := newStore(t).Append(Lesson{Text: "   ", Project: "p"}); err == nil {
		t.Error("an empty lesson should be rejected rather than stored")
	}
}

func mustRead(t *testing.T, p string) []byte {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
