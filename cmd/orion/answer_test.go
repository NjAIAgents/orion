package main

import (
	"bufio"
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/orion-sdlc/orion/internal/discovery"
)

// Three questions: one answered, one skipped, one marked unknown. The file
// carries the two answers, and the listing afterwards shows one open.
func TestAnswerInteractivelyWritesAnswersAndSkips(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "intent.md")
	if err := os.WriteFile(p, []byte("# Intent\n\n## Open questions\n\n- Which region?\n- How many users?\n- Who signs off?\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	in := bufio.NewReader(strings.NewReader("eu-west-1\n-\n?\n"))
	var out bytes.Buffer

	written := answerInteractively(&out, in, []discovery.Assessment{discovery.Assess(p)})

	if len(written) != 1 || written[0] != p {
		t.Errorf("written = %v", written)
	}
	a := discovery.Assess(p)
	if a.Open != 1 || a.Questions[1].Answered {
		t.Errorf("want exactly the skipped question open: %+v", a.Questions)
	}
	b, _ := os.ReadFile(p)
	for _, want := range []string{"- [x] Which region?\n  Answer: eu-west-1", "- How many users?\n", "- [x] Who signs off?\n  Answer: Unknown at this stage"} {
		if !strings.Contains(string(b), want) {
			t.Errorf("file lacks %q:\n%s", want, b)
		}
	}
	// The counter and the question text are on separate lines now (OR-444:
	// bold question, distinct from the plain counter beside it), so the
	// assertion checks both appear in order rather than on one line.
	if !strings.Contains(out.String(), "[1/3]") || !strings.Contains(out.String(), "Which region?") ||
		!strings.Contains(out.String(), "[3/3]") || !strings.Contains(out.String(), "Who signs off?") {
		t.Errorf("questions were not numbered in order:\n%s", out.String())
	}
	if strings.Index(out.String(), "[1/3]") > strings.Index(out.String(), "[3/3]") {
		t.Errorf("questions were not asked in order:\n%s", out.String())
	}
}

// Input that ends early stops the loop without inventing answers.
func TestAnswerInteractivelyStopsWhenInputEnds(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "intent.md")
	if err := os.WriteFile(p, []byte("## Open questions\n- A?\n- B?\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	answerInteractively(&out, bufio.NewReader(strings.NewReader("yes")), []discovery.Assessment{discovery.Assess(p)})
	if got := discovery.Assess(p).Open; got != 2 {
		t.Errorf("Open = %d; a line without a newline is exhausted input, not an answer", got)
	}
}

// '=' takes the question's own stand-in as the answer; without one it
// records unknown rather than an empty answer.
func TestAnswerInteractivelyAcceptsTheStandIn(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "intent.md")
	if err := os.WriteFile(p, []byte("## Open questions\n- [ ] OQ-01 — End date? *Stand-in: licence ends 2027-03-31; live by 2027-02-28.* (FR-005)\n- [ ] OQ-06 — Cost basis? *No stand-in.*\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	answerInteractively(&out, bufio.NewReader(strings.NewReader("=\n=\n")), []discovery.Assessment{discovery.Assess(p)})
	b, _ := os.ReadFile(p)
	if !strings.Contains(string(b), "Answer: licence ends 2027-03-31; live by 2027-02-28\n") {
		t.Errorf("stand-in not taken:\n%s", b)
	}
	if !strings.Contains(string(b), "OQ-06 — Cost basis? *No stand-in.*\n  Answer: Unknown at this stage") {
		t.Errorf("a question with no stand-in should record unknown:\n%s", b)
	}
	if got := discovery.Assess(p).Open; got != 0 {
		t.Errorf("Open = %d, want 0", got)
	}
}

// OR-444: pressing Enter with nothing typed must not silently skip the way
// "-" does -- it re-prompts once, and only a SECOND blank (or an explicit
// "-") actually skips. A real answer typed at the confirm re-prompt is
// accepted and written, same as if it had been typed the first time.
func TestAnswerInteractivelyConfirmsBeforeSkippingOnBlankInput(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "intent.md")
	if err := os.WriteFile(p, []byte("## Open questions\n- Which region?\n- How many users?\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Q1: blank, then a real answer at the confirm re-prompt.
	// Q2: blank, then blank again -- now it skips.
	in := bufio.NewReader(strings.NewReader("\neu-west-1\n\n\n"))
	var out bytes.Buffer

	answerInteractively(&out, in, []discovery.Assessment{discovery.Assess(p)})

	b, _ := os.ReadFile(p)
	if !strings.Contains(string(b), "- [x] Which region?\n  Answer: eu-west-1") {
		t.Errorf("a real answer given at the confirm re-prompt was not written:\n%s", b)
	}
	if strings.Contains(string(b), "[x] How many users?") {
		t.Errorf("a question left blank twice should stay unanswered, not get ticked:\n%s", b)
	}
	if !strings.Contains(out.String(), "nothing typed") {
		t.Errorf("blank input did not trigger the confirm re-prompt:\n%s", out.String())
	}
	a := discovery.Assess(p)
	if a.Open != 1 {
		t.Errorf("Open = %d, want 1 (the twice-blank question stays open)", a.Open)
	}
}

// "-" alone, with no prior blank, still skips immediately -- an explicit
// skip must not require confirming twice.
func TestAnswerInteractivelyDashStillSkipsImmediately(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "intent.md")
	if err := os.WriteFile(p, []byte("## Open questions\n- Which region?\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	in := bufio.NewReader(strings.NewReader("-\n"))
	var out bytes.Buffer

	answerInteractively(&out, in, []discovery.Assessment{discovery.Assess(p)})

	if strings.Contains(out.String(), "nothing typed") {
		t.Errorf("an explicit - should not trigger the blank-input confirm:\n%s", out.String())
	}
	if got := discovery.Assess(p).Open; got != 1 {
		t.Errorf("Open = %d, want 1 (explicitly skipped)", got)
	}
}
