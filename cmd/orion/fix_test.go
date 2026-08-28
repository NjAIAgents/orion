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
