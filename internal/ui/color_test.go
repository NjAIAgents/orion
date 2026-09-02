package ui

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// clean removes every escape sequence, so a test can compare what a person
// reads against what lands in a log file.
var escapes = regexp.MustCompile(`\x1b\[[0-9;]*m`)

func plainOf(s string) string { return escapes.ReplaceAllString(s, "") }

// noColour puts the package in its default state for a test: no forcing, no
// suppression, and a writer that is not a terminal.
func noColour(t *testing.T) {
	t.Helper()
	t.Setenv("NO_COLOR", "")
	os.Unsetenv("NO_COLOR")
	t.Setenv("CLICOLOR_FORCE", "")
	os.Unsetenv("CLICOLOR_FORCE")
	t.Setenv("TERM", "xterm")
}

func forceColour(t *testing.T) {
	t.Helper()
	noColour(t)
	t.Setenv("CLICOLOR_FORCE", "1")
}

// A strings.Builder is not a terminal, so nothing should be painted. This is
// the case that matters most: it is what happens when output is piped into a
// file, a pager, or another program.
func TestNoColourWhenTheDestinationIsNotATerminal(t *testing.T) {
	noColour(t)
	var b strings.Builder
	Ok(&b, "created", "branch %s", "develop")
	if strings.Contains(b.String(), "\x1b[") {
		t.Errorf("escape codes reached a non-terminal: %q", b.String())
	}
}

// NO_COLOR is honoured by its PRESENCE, whatever the value. Requiring a
// particular value would break for everyone who exports it empty.
func TestNoColorIsHonouredByPresenceAtAnyValue(t *testing.T) {
	for _, v := range []string{"", "0", "false", "1"} {
		func() {
			forceColour(t)
			t.Setenv("NO_COLOR", v)
			var b strings.Builder
			Ok(&b, "created", "x")
			if strings.Contains(b.String(), "\x1b[") {
				t.Errorf("NO_COLOR=%q did not suppress colour: %q", v, b.String())
			}
		}()
	}
}

// The counterpart convention, and the reason the coloured path is testable
// at all: keep colour when the destination is a pager or a CI log.
func TestClicolorForceEnablesColourOffATerminal(t *testing.T) {
	forceColour(t)
	var b strings.Builder
	Ok(&b, "created", "x")
	if !strings.Contains(b.String(), "\x1b[") {
		t.Error("CLICOLOR_FORCE did not enable colour")
	}

	// "0" means off, matching how other tools read it.
	t.Setenv("CLICOLOR_FORCE", "0")
	var b2 strings.Builder
	Ok(&b2, "created", "x")
	if strings.Contains(b2.String(), "\x1b[") {
		t.Errorf("CLICOLOR_FORCE=0 still painted: %q", b2.String())
	}
}

// A terminal that says it cannot render escape codes is telling the truth.
func TestTermDumbDisablesColour(t *testing.T) {
	noColour(t)
	t.Setenv("TERM", "dumb")
	var b strings.Builder
	Ok(&b, "created", "x")
	if strings.Contains(b.String(), "\x1b[") {
		t.Errorf("TERM=dumb still painted: %q", b.String())
	}
}

// The claim this package rests on, asserted rather than assumed: colour marks
// STATUS only, so stripping it changes nothing a reader depends on. Every
// line still leads with the word.
func TestPipedOutputIsIdenticalToColouredOutputWithoutEscapes(t *testing.T) {
	cases := []struct{ verb, detail string }{
		{"created", "branch develop"},
		{"failed", "Jira project FCIA: 400"},
		{"warning", "no sandbox yet"},
		{"ci-wait", "PR #4"},
		{"working", "FCIA-6"},
		{"queued", "FCIA-7"},
		{"mystery", "unknown verb falls through"},
	}
	for _, c := range cases {
		noColour(t)
		var plain strings.Builder
		Ok(&plain, c.verb, "%s", c.detail)

		forceColour(t)
		var painted strings.Builder
		Ok(&painted, c.verb, "%s", c.detail)

		if got := plainOf(painted.String()); got != plain.String() {
			t.Errorf("verb %q: stripping colour gave %q, want %q", c.verb, got, plain.String())
		}
		if !strings.Contains(plain.String(), c.verb) {
			t.Errorf("verb %q is not present in the plain text; colour is carrying meaning alone", c.verb)
		}
	}
}

// Each status must be visually distinct, or the colour is decoration. The
// pairs here are the ones that would mislead if they matched.
func TestStatusesThatMustNotShareAColour(t *testing.T) {
	forceColour(t)
	code := func(verb string) string {
		s := Label(os.Stdout, verb, "")
		m := escapes.FindString(s)
		return m
	}
	for _, pair := range [][2]string{
		// working is money being spent; ci-wait is a machine deciding.
		{"working", "ci-wait"},
		// created is done; working is not.
		{"created", "working"},
		{"failed", "warning"},
		{"created", "failed"},
	} {
		if a, b := code(pair[0]), code(pair[1]); a == b {
			t.Errorf("%q and %q share colour %q; the distinction is invisible", pair[0], pair[1], a)
		}
	}
}

// The verb column is padded so detail lines up. Without it a long report is
// a ragged wall and the eye has nothing to follow.
func TestLabelPadsTheVerbSoColumnsAlign(t *testing.T) {
	noColour(t)
	short := Label(nil, "ok", "detail")
	long := Label(nil, "installed", "detail")
	si := strings.Index(short, "detail")
	li := strings.Index(long, "detail")
	if si != li {
		t.Errorf("detail starts at %d for a short verb and %d for a long one", si, li)
	}
}

func TestWarnAndFailUseTheirOwnVerbs(t *testing.T) {
	noColour(t)
	var b strings.Builder
	Warn(&b, "something %s", "odd")
	if !strings.Contains(b.String(), "WARNING") || !strings.Contains(b.String(), "something odd") {
		t.Errorf("Warn = %q", b.String())
	}
	var c strings.Builder
	Fail(&c, "it %s", "broke")
	if !strings.Contains(c.String(), "failed") || !strings.Contains(c.String(), "it broke") {
		t.Errorf("Fail = %q", c.String())
	}
}

func TestEveryPrinterEndsItsLine(t *testing.T) {
	noColour(t)
	for name, fn := range map[string]func(*strings.Builder){
		"Ok":   func(b *strings.Builder) { Ok(b, "created", "x") },
		"Warn": func(b *strings.Builder) { Warn(b, "x") },
		"Fail": func(b *strings.Builder) { Fail(b, "x") },
	} {
		var b strings.Builder
		fn(&b)
		if !strings.HasSuffix(b.String(), "\n") {
			t.Errorf("%s did not terminate its line: %q", name, b.String())
		}
	}
}

func TestHeadingAndDimAreTransparentWhenColourIsOff(t *testing.T) {
	noColour(t)
	if got := Heading(nil, "queue"); got != "queue" {
		t.Errorf("Heading = %q, want the bare text", got)
	}
	if got := Dim(nil, "path/to/thing"); got != "path/to/thing" {
		t.Errorf("Dim = %q, want the bare text", got)
	}

	forceColour(t)
	if got := Heading(os.Stdout, "queue"); plainOf(got) != "queue" {
		t.Errorf("Heading altered the text: %q -> %q", got, plainOf(got))
	}
	if got := Dim(os.Stdout, "p"); !strings.Contains(got, "\x1b[") {
		t.Errorf("Dim did not paint when forced: %q", got)
	}
}

// The update notice (OR-92) is yellow, as asked for, but it is not a
// warning: nothing is broken and no action is required. The distinct VERB is
// what keeps it apart from "your branch is stale" for someone scanning, so
// it must never be rendered as WARNING.
func TestUpdateIsItsOwnVerbAtTheWarningColour(t *testing.T) {
	forceColour(t)
	code := func(verb string) string { return escapes.FindString(Label(os.Stdout, verb, "")) }
	if code("update") != code("warning") {
		t.Errorf("update is %q, warning is %q; the ticket asks for yellow", code("update"), code("warning"))
	}
	noColour(t)
	if !strings.HasPrefix(Label(nil, "update", "x"), "update") {
		t.Errorf("the line must lead with its own verb: %q", Label(nil, "update", "x"))
	}
}

// A continuation line sits under the detail column, so the command to run
// reads as part of the status above it rather than as a new one.
func TestDetailAlignsUnderTheDetailColumn(t *testing.T) {
	noColour(t)
	status := Label(nil, "update", "orion v0.5.1 is available")
	if got, want := len(Detail(nil, "")), strings.Index(status, "orion"); got != want {
		t.Errorf("continuation indents %d columns, detail starts at %d", got, want)
	}
}

// A nil writer must not panic: callers pass one when they only want the
// string, and a formatting helper that crashes takes the run with it.
func TestNilWriterIsSafe(t *testing.T) {
	noColour(t)
	if got := Label(nil, "created", "x"); !strings.Contains(got, "created") {
		t.Errorf("Label(nil) = %q", got)
	}
}

// Escape codes must always be closed, or every line after a coloured one
// inherits the colour and the terminal is left tinted after the command.
func TestEveryPaintedStringResets(t *testing.T) {
	forceColour(t)
	for _, s := range []string{
		Label(os.Stdout, "created", "x"),
		Heading(os.Stdout, "h"),
		Dim(os.Stdout, "d"),
	} {
		if !strings.Contains(s, "\x1b[") {
			continue
		}
		if !strings.Contains(s, "\x1b[0m") {
			t.Errorf("%q opens a colour without closing it", s)
		}
	}
}
