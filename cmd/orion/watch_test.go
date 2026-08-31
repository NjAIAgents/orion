package main

import (
	"bytes"
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"
	"time"

	"github.com/orion-sdlc/orion/internal/tracker"
	"github.com/orion-sdlc/orion/internal/watch"
)

// OR-128: `orion watch` printed nothing at all for minutes -- no banner, no
// poll, nothing in the event log -- and a person had to reach for `sample`
// to tell "still starting up" from "hung, kill it". The first half of the
// fix is that SOMETHING reaches the console before anything that can block.
func TestWatchBannerNamesTheTermsBeforeAnythingElse(t *testing.T) {
	var buf bytes.Buffer
	watchBanner(&buf, []string{"OR", "FCIA"}, 90*time.Second, 2, 2,
		"OR's limits.max_concurrent_tickets", false)

	// Which project, which label, what interval: the three things the issue
	// asks for, because a banner that says only "watching" confirms the
	// process is alive without confirming it is watching what you meant.
	// The concurrency cap is on the list too: it decides how much money is in
	// flight at once and it comes from a file the operator may not have opened,
	// so discovering it from a bill is the wrong way to learn it (OR-184).
	for _, want := range []string{
		"OR, FCIA", tracker.QueueLabelDefault, "1m30s", "2 job(s)",
		"2 ticket(s)", "max_concurrent_tickets",
	} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("the startup banner does not mention %q:\n%s", want, buf.String())
		}
	}
}

// The banner states BOTH halves of the claim criterion (OR-221): the queue
// label, and -- where a project uses releases -- an open one. Half of the
// criterion is new, and an operator who only sees "labelled ORION" would not
// know why a labelled ticket sat unclaimed.
func TestWatchBannerNamesTheReleaseRequirement(t *testing.T) {
	var buf bytes.Buffer
	watchBanner(&buf, []string{"OR"}, time.Minute, 0, 2, "default", false)

	for _, want := range []string{
		"labelled " + tracker.QueueLabelDefault,
		"open",
	} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("the banner does not say %q:\n%s", want, buf.String())
		}
	}
	if !strings.Contains(buf.String(), "where the project uses releases") {
		t.Errorf("the banner does not say the release requirement is conditional on the project:\n%s", buf.String())
	}
}

// The banner is worth nothing if it prints after the thing that hangs. This
// pins the ORDER inside runWatch: the banner call must come before the
// tracker client is built and before the loop starts, so a stall in either
// still leaves the terms on screen.
//
// A source check rather than a behavioural one because runWatch needs a
// configured tracker and exits the process on failure -- there is no seam to
// drive it through. The ordering is the whole guarantee, so it is pinned
// where it can actually be pinned.
func TestTheBannerIsPrintedBeforeAnyNetworkCall(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "watch.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}

	var fn *ast.FuncDecl
	for _, d := range f.Decls {
		if g, ok := d.(*ast.FuncDecl); ok && g.Name.Name == "runWatch" {
			fn = g
		}
	}
	if fn == nil {
		t.Fatal("runWatch is gone; this test needs rewriting rather than deleting")
	}

	// Position of the first call to each, in source order. runWatch is
	// straight-line code, so source order is execution order.
	pos := map[string]int{}
	ast.Inspect(fn, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		name := ""
		switch f := call.Fun.(type) {
		case *ast.Ident:
			name = f.Name
		case *ast.SelectorExpr:
			if pkg, ok := f.X.(*ast.Ident); ok {
				name = pkg.Name + "." + f.Sel.Name
			}
		}
		if _, seen := pos[name]; !seen && name != "" {
			pos[name] = int(call.Pos())
		}
		return true
	})

	banner, ok := pos["watchBanner"]
	if !ok {
		t.Fatal("runWatch no longer prints a startup banner: an orion watch that " +
			"prints nothing before its first network call is indistinguishable from a hang (OR-128)")
	}
	for _, later := range []string{"tracker.NewJiraFromEnv", "watch.Run"} {
		at, ok := pos[later]
		if !ok {
			t.Fatalf("runWatch no longer calls %s; this test needs rewriting", later)
		}
		if at < banner {
			t.Errorf("%s runs BEFORE the startup banner.\n"+
				"  Anything that can block must come after the banner, or a stall in it "+
				"prints nothing at all and looks exactly like a healthy idle watcher (OR-128).",
				later)
		}
	}
}

// `orion watch` with no --interval ticks every minute (OR-218), and the
// banner has to print that same number: the interval the banner names is the
// operator's only statement of how long a green CI run or a merged PR waits
// before anything acts on it.
//
// Zero and below mean "unset" here exactly as they do in the loop, so the
// two can never disagree about what the watcher is doing.
func TestTheWatchIntervalDefaultsToAMinuteAndTheBannerSaysSo(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want time.Duration
	}{
		{"unset", []string{"fcia"}, time.Minute},
		{"explicit", []string{"--interval", "300", "fcia"}, 5 * time.Minute},
		// A small explicit value below the default must still be used as
		// given -- the fallback is for non-positive input, not for "smaller
		// than what we'd have picked ourselves".
		{"explicit below the default", []string{"--interval", "30", "fcia"}, 30 * time.Second},
		{"zero", []string{"--interval", "0"}, time.Minute},
		{"negative", []string{"--interval", "-30"}, time.Minute},
	} {
		got := watchInterval(tc.args)
		if got != tc.want {
			t.Errorf("watchInterval(%v) = %v, want %v", tc.args, got, tc.want)
		}
		if got != watch.DefaultInterval && tc.want == time.Minute {
			t.Errorf("watchInterval(%v) = %v, which is not the loop's own default %v",
				tc.args, got, watch.DefaultInterval)
		}

		var buf bytes.Buffer
		watchBanner(&buf, nil, got, 0, 1, "default", false)
		if want := "interval  " + got.String(); !strings.Contains(buf.String(), want) {
			t.Errorf("%s: the banner does not print the interval in use (%q):\n%s",
				tc.name, want, buf.String())
		}
	}
}
