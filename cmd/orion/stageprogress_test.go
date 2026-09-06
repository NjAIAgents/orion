package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/orion-sdlc/orion/internal/supervisor"
)

// The reported symptom: `orion run --stage spec` printed one warning and then
// nothing for five and a half minutes, on a run that was working the whole
// time. Silence reads as a hang, and a working run gets killed halfway.
func TestAStageSaysWhatItIsDoing(t *testing.T) {
	var out bytes.Buffer
	p := newStageProgress(&out)

	p.On(supervisor.Activity{Kind: "start", Model: "opus", Tools: 42})
	p.On(supervisor.Activity{Kind: "tool", Tool: "WebFetch", Detail: "https://example.com/product"})
	p.On(supervisor.Activity{Kind: "text", Detail: "Reading the vendor's page first.\nThen writing."})
	p.On(supervisor.Activity{Kind: "tool", Tool: "Write", Detail: "docs/intent/cloudlens.md"})

	got := out.String()
	for _, want := range []string{
		"started", "opus", "42 tools",
		"WebFetch", "https://example.com/product",
		"Reading the vendor's page first.",
		"Write", "docs/intent/cloudlens.md",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the transcript never mentions %q:\n%s", want, got)
		}
	}
	// Only the first line of the agent's prose: the rest is in the log, and a
	// paragraph per thought buries the tool calls around it.
	if strings.Contains(got, "Then writing.") {
		t.Errorf("a whole paragraph was printed:\n%s", got)
	}
}

// A long stage can make thousands of tool calls. Past the cap it must still
// answer the only question left -- is it alive -- without burying the summary
// that follows.
func TestALongStageFallsBackToAHeartbeat(t *testing.T) {
	var out bytes.Buffer
	p := newStageProgress(&out)

	for i := 0; i < maxProgressLines+50; i++ {
		p.On(supervisor.Activity{Kind: "tool", Tool: "Read", Detail: "file.go"})
	}

	lines := strings.Count(out.String(), "\n")
	if lines > maxProgressLines+5 {
		t.Errorf("printed %d lines; the cap is %d", lines, maxProgressLines)
	}
	if lines < maxProgressLines {
		t.Errorf("printed only %d lines, so the cap cut it short", lines)
	}
}

// Long arguments are clipped rather than wrapped: a wrapped line is two
// lines, and two lines per tool call is not a transcript.
func TestALongArgumentIsClipped(t *testing.T) {
	got := progressLine(supervisor.Activity{
		Kind: "tool", Tool: "Bash", Detail: strings.Repeat("x", 400),
	})
	if len(got) > 100 {
		t.Errorf("line is %d characters:\n%s", len(got), got)
	}
	if !strings.HasSuffix(got, "…") {
		t.Error("a clipped line does not show that it was cut")
	}
}

// A newline inside a tool argument would break one line into several and
// desynchronise the transcript from what actually happened.
func TestANewlineInAnArgumentStaysOnOneLine(t *testing.T) {
	got := progressLine(supervisor.Activity{
		Kind: "tool", Tool: "Bash", Detail: "go test ./...\nrm -rf /tmp/x",
	})
	if strings.Contains(got, "\n") {
		t.Errorf("the line contains a newline: %q", got)
	}
}

// An activity with nothing to say produces no line at all, rather than a
// blank one.
func TestAnEmptyActivityPrintsNothing(t *testing.T) {
	var out bytes.Buffer
	p := newStageProgress(&out)
	p.On(supervisor.Activity{Kind: "text", Detail: "   \n  "})
	p.On(supervisor.Activity{Kind: "unknown-kind"})
	if out.String() != "" {
		t.Errorf("printed something for nothing:\n%q", out.String())
	}
}
