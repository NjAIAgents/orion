// Package ui renders Orion's terminal output.
//
// Colour is an accessibility feature here, not decoration: `orion init` now
// reports a dozen actions across git, Jira, Slack and dun, and a wall of
// uniform text makes the one line that failed as quiet as the eleven that
// worked. Colour is what makes a failure findable at a glance.
//
// It is therefore applied to STATUS only -- did this work, did it not --
// never to carry meaning colour is the sole conveyor of. Every line still
// begins with a word (created, failed, WARNING) that says the same thing, so
// the output reads identically when piped, logged, or seen by someone who
// cannot distinguish the colours.
package ui

import (
	"fmt"
	"io"
	"os"
	"strings"
)

const (
	reset  = "\x1b[0m"
	green  = "\x1b[32m"
	red    = "\x1b[31m"
	yellow = "\x1b[33m"
	dim    = "\x1b[2m"
	bold   = "\x1b[1m"
)

// enabled reports whether to emit escape codes.
//
// Three ways to say no, and all of them are respected: NO_COLOR is the
// cross-tool convention (its presence counts, whatever the value), TERM=dumb
// is how a terminal announces it cannot render them, and a non-terminal
// writer means the output is being piped into a file or another program that
// would have to strip them back out.
func enabled(w io.Writer) bool {
	if _, ok := os.LookupEnv("NO_COLOR"); ok {
		return false
	}
	if os.Getenv("TERM") == "dumb" {
		return false
	}
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	fi, err := f.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

func paint(w io.Writer, code, s string) string {
	if !enabled(w) {
		return s
	}
	return code + s + reset
}

// Label renders one status line: a coloured verb, then plain detail.
//
// The verb is padded so detail columns line up, which is what makes a long
// init report scannable at all.
func Label(w io.Writer, verb, detail string) string {
	var code string
	switch strings.ToLower(verb) {
	case "created", "installed", "updated", "ok", "bound", "pushed":
		code = green
	case "failed", "error":
		code = red
	case "warning", "skipped":
		code = yellow
	default:
		code = dim
	}
	return fmt.Sprintf("%s %s", paint(w, code, fmt.Sprintf("%-8s", verb)), detail)
}

// Ok, Warn and Fail print one status line each.
func Ok(w io.Writer, verb, format string, a ...any) {
	fmt.Fprintln(w, Label(w, verb, fmt.Sprintf(format, a...)))
}

func Warn(w io.Writer, format string, a ...any) {
	fmt.Fprintln(w, Label(w, "WARNING", fmt.Sprintf(format, a...)))
}

func Fail(w io.Writer, format string, a ...any) {
	fmt.Fprintln(w, Label(w, "failed", fmt.Sprintf(format, a...)))
}

// Heading renders a section title.
func Heading(w io.Writer, s string) string { return paint(w, bold, s) }

// Dim renders secondary detail, for continuation lines under a status.
func Dim(w io.Writer, s string) string { return paint(w, dim, s) }
