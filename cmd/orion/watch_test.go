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
)

// OR-128: `orion watch` printed nothing at all for minutes -- no banner, no
// poll, nothing in the event log -- and a person had to reach for `sample`
// to tell "still starting up" from "hung, kill it". The first half of the
// fix is that SOMETHING reaches the console before anything that can block.
func TestWatchBannerNamesTheTermsBeforeAnythingElse(t *testing.T) {
	var buf bytes.Buffer
	watchBanner(&buf, []string{"OR", "FCIA"}, 90*time.Second, 2, false)

	// Which project, which label, what interval: the three things the issue
	// asks for, because a banner that says only "watching" confirms the
	// process is alive without confirming it is watching what you meant.
	for _, want := range []string{"OR, FCIA", tracker.QueueLabelDefault, "1m30s", "2 job(s)"} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("the startup banner does not mention %q:\n%s", want, buf.String())
		}
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
