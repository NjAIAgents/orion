package lessons

import (
	"os"
	"strings"
	"testing"
	"time"
)

func appendRaw(path, line string) error {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.WriteString(line)
	return err
}

func sighting(text, project string) Proposal {
	return Proposal{Text: text, Project: project, Evidence: project + "-1 on 2026-08-27: merged u"}
}

// Once is circumstance. The two-strike rule has to govern whether something is
// RECORDED at all, not only whether it is promoted across projects -- otherwise
// every one-off reaches a human, and a queue of one-offs gets dismissed
// unread, taking the real lessons with it.
func TestOneSightingIsNotOfferedForApproval(t *testing.T) {
	s := newStore(t)

	c, err := s.Observe(sighting("CI failed with a flaky timeout", "p1"))
	if err != nil {
		t.Fatal(err)
	}
	if c.Strikes != 1 || c.Ready() {
		t.Fatalf("one sighting was ready for approval: %+v", c)
	}
	pending, err := s.Pending()
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 0 {
		t.Fatalf("got %d pending, want 0", len(pending))
	}
}

func TestTwoSightingsBecomeAProposal(t *testing.T) {
	s := newStore(t)
	s.Observe(sighting("The migration ran twice", "p1"))
	c, err := s.Observe(sighting("the migration ran twice.", "p2"))
	if err != nil {
		t.Fatal(err)
	}
	// Normalized to one signature: the same mistake, phrased the same way.
	if c.Strikes != 2 || !c.Ready() {
		t.Fatalf("two sightings did not make a proposal: %+v", c)
	}
	if len(c.Projects) != 2 {
		t.Errorf("both projects must be carried: %v", c.Projects)
	}
}

// The store is append-only and injected into every session's CLAUDE.md, so a
// wrong lesson is durable and re-read forever. Nothing may write one on its
// own authority.
func TestObservingNeverWritesALesson(t *testing.T) {
	s := newStore(t)
	s.Observe(sighting("Something happened", "p1"))
	s.Observe(sighting("Something happened", "p1"))

	rs, err := s.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(rs) != 0 {
		t.Fatalf("a lesson was recorded without approval: %+v", rs)
	}
}

func TestApprovingRecordsTheLessonWithItsProvenance(t *testing.T) {
	s := newStore(t)
	s.Observe(sighting("Rebase merges do not preserve ancestry", "p1"))
	c, _ := s.Observe(sighting("Rebase merges do not preserve ancestry", "p2"))

	if _, err := s.Decide(c.Signature, DecisionApproved, "nav"); err != nil {
		t.Fatal(err)
	}
	rs, err := s.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(rs) != 1 {
		t.Fatalf("got %d records, want 1", len(rs))
	}
	r := rs[0]
	if r.Project == "" {
		t.Error("a recorded lesson must name the project it came from")
	}
	if r.At.IsZero() || time.Since(r.At) > time.Hour {
		t.Errorf("a recorded lesson must name the date: %v", r.At)
	}
	if !strings.Contains(r.Evidence, "p1-1") || !strings.Contains(r.Evidence, "p2-1") {
		t.Errorf("the evidence of every sighting must survive: %q", r.Evidence)
	}
	if len(r.Projects) != 2 {
		t.Errorf("recurrence across projects must be preserved for promotion: %v", r.Projects)
	}
}

func TestRejectingRecordsNothingAndClosesTheProposal(t *testing.T) {
	s := newStore(t)
	s.Observe(sighting("Environment noise", "p1"))
	c, _ := s.Observe(sighting("Environment noise", "p1"))

	if _, err := s.Decide(c.Signature, DecisionRejected, "nav"); err != nil {
		t.Fatal(err)
	}
	rs, _ := s.Load()
	if len(rs) != 0 {
		t.Fatalf("a rejected proposal was recorded: %+v", rs)
	}
	pending, _ := s.Pending()
	if len(pending) != 0 {
		t.Fatal("a rejected proposal is still being asked about")
	}
	// And a third sighting must not reopen a question already answered.
	s.Observe(sighting("Environment noise", "p1"))
	if pending, _ = s.Pending(); len(pending) != 0 {
		t.Fatal("a rejected proposal came back")
	}
}

// If a reviewer could wave a single sighting through, the two-strike rule
// would be advisory rather than a rule.
func TestASingleSightingCannotBeApproved(t *testing.T) {
	s := newStore(t)
	c, _ := s.Observe(sighting("Seen exactly once", "p1"))

	if _, err := s.Decide(c.Signature, DecisionApproved, "nav"); err == nil {
		t.Fatal("a one-off was recorded as a lesson")
	}
}

func TestDecidingTwiceIsRefused(t *testing.T) {
	s := newStore(t)
	s.Observe(sighting("Twice decided", "p1"))
	c, _ := s.Observe(sighting("Twice decided", "p1"))
	if _, err := s.Decide(c.Signature, DecisionApproved, "nav"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Decide(c.Signature, DecisionRejected, "nav"); err == nil {
		t.Fatal("a decided proposal was decided again")
	}
}

// Grounding. A proposal that cannot say what happened, where, and when is an
// agent's opinion, and an opinion injected into every future session is worse
// than no memory at all.
func TestAnUngroundedProposalIsRefused(t *testing.T) {
	s := newStore(t)
	for _, p := range []Proposal{
		{Text: "  ", Project: "p1", Evidence: "e"},
		{Text: "something", Project: "", Evidence: "e"},
		{Text: "something", Project: "p1", Evidence: ""},
	} {
		if _, err := s.Observe(p); err == nil {
			t.Errorf("accepted an ungrounded proposal: %+v", p)
		}
	}
}

func TestALessonWithoutAProjectIsRefused(t *testing.T) {
	if err := newStore(t).Append(Lesson{Text: "no provenance"}); err == nil {
		t.Fatal("recorded a lesson that cannot say where it came from")
	}
}

// The failure this whole ticket is about: an empty store looked exactly like a
// working one. Health is what tells the two apart.
func TestHealthDistinguishesAnEmptyStoreFromAnUnwrittenOne(t *testing.T) {
	s := newStore(t)

	h, err := s.Health()
	if err != nil {
		t.Fatal(err)
	}
	if h.Observed() {
		t.Fatal("a store nothing has written to reported observations")
	}

	s.Observe(sighting("Something real", "p1"))
	s.Observe(sighting("Something real", "p1"))

	h, err = s.Health()
	if err != nil {
		t.Fatal(err)
	}
	if !h.Observed() || h.Sightings != 2 {
		t.Fatalf("sightings = %d, want 2", h.Sightings)
	}
	if h.Pending != 1 {
		t.Fatalf("pending = %d, want 1", h.Pending)
	}
	if h.LastObserved.IsZero() {
		t.Error("the date of the last observation must be reportable")
	}
}

// Stack decides whether a stack-scoped lesson reaches a project, so the
// session hook and the automatic proposal path have to agree on the answer.
func TestDetectStackReadsTheManifest(t *testing.T) {
	dir := t.TempDir()
	if DetectStack(dir) != "" {
		t.Error("a directory with no manifest claimed a stack")
	}
	if err := os.WriteFile(dir+"/go.mod", []byte("module x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := DetectStack(dir); got != "go" {
		t.Errorf("stack = %q, want go", got)
	}
	if DetectStack("") != "" {
		t.Error("an empty root claimed a stack")
	}
}

func TestACorruptProposalLineDoesNotLoseTheRest(t *testing.T) {
	s := newStore(t)
	s.Observe(sighting("Survivor", "p1"))
	if err := appendRaw(s.proposalsPath(), "{not json\n"); err != nil {
		t.Fatal(err)
	}
	s.Observe(sighting("Survivor", "p1"))

	pending, err := s.Pending()
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 {
		t.Fatalf("got %d pending, want 1", len(pending))
	}
}
