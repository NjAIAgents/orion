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
	if !strings.Contains(out.String(), "[1/3] Which region?") || !strings.Contains(out.String(), "[3/3] Who signs off?") {
		t.Errorf("questions were not numbered in order:\n%s", out.String())
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
