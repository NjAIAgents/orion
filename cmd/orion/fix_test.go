package main

import (
	"strings"
	"testing"
)

// oneLine is the piece of OR-129 that reduces a whole closing message to
// something that fits a console line -- keeping the first non-blank line
// rather than truncating mid-sentence, since an agent's summary is usually
// front-loaded.
func TestOneLineKeepsTheFirstNonBlankLine(t *testing.T) {
	cases := map[string]string{
		"":                                  "",
		"\n\n  \n":                          "",
		"fixed the off-by-one":              "fixed the off-by-one",
		"\n\nfixed the off-by-one\nbecause": "fixed the off-by-one",
		"  leading space trimmed  \nrest":   "leading space trimmed",
	}
	for in, want := range cases {
		if got := oneLine(in); got != want {
			t.Errorf("oneLine(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestOneLineTruncatesAnOverlongLine(t *testing.T) {
	long := strings.Repeat("x", 300)
	got := oneLine(long)
	if want := strings.Repeat("x", 160); !strings.HasPrefix(got, want) {
		t.Errorf("oneLine(long) did not keep the first 160 chars: %q", got)
	}
	if !strings.HasSuffix(got, "\u2026") {
		t.Errorf("oneLine(long) = %q, want a trailing ellipsis", got)
	}
	if n := len([]rune(got)); n != 161 { // 160 chars + the ellipsis rune
		t.Errorf("len([]rune(oneLine(long))) = %d, want 161", n)
	}
}

// isEditTool must name the exact set Shield is wired to in PreToolUse
// ("Edit|Write|MultiEdit|NotebookEdit" in writeSettings) -- missing one here
// means a write that Shield blocks goes undetected as a policy denial.
func TestIsEditToolMatchesWhatShieldGuards(t *testing.T) {
	for _, tool := range []string{"Edit", "Write", "MultiEdit", "NotebookEdit"} {
		if !isEditTool(tool) {
			t.Errorf("isEditTool(%q) = false, want true", tool)
		}
	}
	for _, tool := range []string{"Read", "Bash", "Grep", "Task"} {
		if isEditTool(tool) {
			t.Errorf("isEditTool(%q) = true, want false", tool)
		}
	}
}

// This is the exact OR-172 scenario: an Edit targeting the CI workflow,
// which orion.json's default paths.protected list denies unconditionally.
func TestMatchedRuleFindsTheProtectedCIWorkflowPath(t *testing.T) {
	protected := []string{".github/workflows/**", "orion.json", "managed-settings.json"}

	if got := matchedRule(protected, ".github/workflows/ci.yml"); got != ".github/workflows/**" {
		t.Errorf("matchedRule(ci.yml) = %q, want the workflows rule", got)
	}
	if got := matchedRule(protected, "orion.json"); got != "orion.json" {
		t.Errorf("matchedRule(orion.json) = %q, want an exact match", got)
	}
	if got := matchedRule(protected, "internal/collect/fixloop.go"); got != "" {
		t.Errorf("matchedRule(fixloop.go) = %q, want no match for an ordinary source file", got)
	}
}
