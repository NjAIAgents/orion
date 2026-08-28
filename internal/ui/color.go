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
	"sync"
)

const (
	reset  = "\x1b[0m"
	green  = "\x1b[32m"
	red    = "\x1b[31m"
	yellow = "\x1b[33m"
	cyan   = "\x1b[36m"
	blue   = "\x1b[34m"
	dim    = "\x1b[2m"
	bold   = "\x1b[1m"
	// The non-semantic set, for colouring things that are IDENTITIES rather
	// than outcomes -- a ticket, an actor. Green, red and yellow are spoken
	// for: a ticket rendered in red reads as broken on every line it emits.
	magenta       = "\x1b[35m"
	brightCyan    = "\x1b[96m"
	brightMagenta = "\x1b[95m"
	brightBlue    = "\x1b[94m"
)

// colorMu guards the ticket-colour assignment in event.go. A watcher renders
// from more than one goroutine, and a data race in the renderer would
// corrupt the output at exactly the moment somebody is reading it to find
// out what went wrong.
var colorMu sync.Mutex

// enabled reports whether to emit escape codes.
//
// Three ways to say no, and all of them are respected: NO_COLOR is the
// cross-tool convention (its presence counts, whatever the value), TERM=dumb
// is how a terminal announces it cannot render them, and a non-terminal
// writer means the output is being piped into a file or another program that
// would have to strip them back out.
func enabled(w io.Writer) bool {
	// NO_COLOR wins over everything, including an explicit force. The
	// convention exists for people who cannot read the output otherwise, and
	// a tool that lets a different variable override it has misunderstood
	// what the variable is for.
	if _, ok := os.LookupEnv("NO_COLOR"); ok {
		return false
	}
	// CLICOLOR_FORCE is the counterpart convention: keep colour when the
	// destination is not a terminal, for a pager or a CI log that renders it.
	// "0" means off, matching how other tools read it.
	if v, ok := os.LookupEnv("CLICOLOR_FORCE"); ok && v != "0" {
		return true
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

// glyphs reports whether to emit the non-ASCII status icons.
//
// The same opt-outs as colour, for the same reason -- NO_COLOR is set by
// people who need the plain form, and TERM=dumb is a terminal saying it
// cannot render anything clever -- plus the locale, because a glyph on a
// non-UTF-8 terminal is mojibake rather than a status.
//
// Not gated on whether the writer is a terminal, unlike colour: a glyph in a
// piped log or a nohup capture reads correctly, and an escape code does not.
func glyphs() bool {
	if _, ok := os.LookupEnv("NO_COLOR"); ok {
		return false
	}
	if os.Getenv("TERM") == "dumb" {
		return false
	}
	return utf8Locale()
}

// utf8Locale reads the locale the way every other terminal tool does.
//
// An UNSET locale counts as UTF-8. On a modern desktop these variables are
// frequently empty, and treating empty as "not UTF-8" would hand almost
// everybody the degraded output for the sake of the rare C-locale terminal.
// An explicitly set locale is the signal worth acting on.
func utf8Locale() bool {
	for _, v := range []string{"LC_ALL", "LC_CTYPE", "LANG"} {
		s := strings.ToUpper(os.Getenv(v))
		if s == "" {
			continue
		}
		return strings.Contains(s, "UTF-8") || strings.Contains(s, "UTF8")
	}
	return true
}

func paint(w io.Writer, code, s string) string {
	if !enabled(w) {
		return s
	}
	return code + s + reset
}

// verbWidth is the width the status verb is padded to.
//
// Wide enough for the longest verb in use ("installed", 9). It was 8, which
// silently broke the alignment it exists to provide: every report containing
// "installed" pushed its detail one column right of every other line, which
// is precisely the ragged wall the padding was added to prevent.
//
// Derived rather than typed as a literal so adding a longer verb widens the
// column instead of quietly overflowing it again.
var verbWidth = longestVerb()

func longestVerb() int {
	n := 0
	for _, v := range []string{
		"created", "installed", "updated", "skipped", "bound", "backup",
		"WARNING", "failed", "error", "ci-wait", "working", "running",
		"queued", "pushed", "invited", "ok", "resolved", "fetched",
	} {
		if len(v) > n {
			n = len(v)
		}
	}
	return n
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
	case "ci-wait":
		// Distinct from working: nothing is being spent here, a machine is
		// deciding. Sharing a colour would hide which runs cost money.
		code = blue
	case "working", "running":
		// Distinct from success: a claimed ticket is in flight, not finished.
		// Colouring it green would say "done" for work that may still fail.
		code = cyan
	case "warning", "skipped", "queued":
		code = yellow
	default:
		code = dim
	}
	return fmt.Sprintf("%s %s", paint(w, code, fmt.Sprintf("%-*s", verbWidth, verb)), detail)
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
