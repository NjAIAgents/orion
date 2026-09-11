package web

// The panel/route registry (OR-70): adding a page requires adding one
// file, not editing a central router by hand.
//
// Tests over the served artifacts, the pattern runview_test.go and
// logpanel_test.go both established: no browser, no Node, per OR-71's own
// no-Node criterion.

import (
	"strings"
	"testing"
)

// DONE-WHEN, THE WHOLE OF IT: app.js -- the shell -- must not name a
// specific panel anywhere. If it did, "adding a page" would still mean
// editing this file, which is exactly the router-by-hand OR-70 exists to
// remove.
func TestAppJSNamesNoSpecificPanel(t *testing.T) {
	src := servedFile(t, "/js/app.js")
	// Comment lines excluded, the same reason
	// TestAppJSNeverUsesDangerouslySetInnerHTML excludes them: a doc comment
	// naming an example (this file's own currentName comment shows "#run" as
	// a worked example of hash parsing) is not the shell depending on that
	// panel, and a bare substring match cannot tell the two apart.
	for _, line := range strings.Split(src, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "//") {
			continue
		}
		for _, forbidden := range []string{"RunPanel", "CardGrid", "LogPanel", `"run"`} {
			if strings.Contains(line, forbidden) {
				t.Errorf("app.js references %q in code; the shell must learn what panels "+
					"exist only from the registry, never by naming one. Line: %s",
					forbidden, trimmed)
			}
		}
	}
}

// The shell draws its nav from listPanels(), not a hardcoded array -- the
// mechanism, checked directly, rather than only its absence of names above.
func TestAppJSBuildsNavFromTheRegistry(t *testing.T) {
	src := servedFile(t, "/js/app.js")
	if !strings.Contains(src, "listPanels()") {
		t.Fatal("app.js does not call listPanels(); the nav has nothing to draw from " +
			"but a hardcoded list")
	}
	// The sidebar (OR-439) splits the registry into two rendered groups --
	// pagePanels (inNav !== false) and viewPanels (inNav === false) -- rather
	// than one navPanels list, so both must exist and both must be mapped
	// over, or one half of the registry has nothing rendering it.
	if !strings.Contains(src, "pagePanels.map(") {
		t.Error("app.js does not map over pagePanels to build the sidebar's page section")
	}
	if !strings.Contains(src, "viewPanels.map(") {
		t.Error("app.js does not map over viewPanels to build the sidebar's destination-view section")
	}
	if !strings.Contains(src, "panels.filter(") {
		t.Error("app.js does not filter the registry (OR-53's detail panel and OR-437's ask " +
			"broker mark themselves inNav: false); a sidebar built straight from listPanels() " +
			"with no filter could not tell a page from a destination-only view")
	}
}

// registerPanel is what a panel module calls, at its own top level, to add
// itself -- the client-side half of the seam server.go's Handle/init()
// pattern already establishes server-side. panel-run.js is the one caller
// that exists today; this proves it actually uses the seam rather than
// bypassing it.
func TestPanelRunRegistersThroughTheSeam(t *testing.T) {
	src := servedFile(t, "/js/panel-run.js")
	if !strings.Contains(src, `registerPanel("run"`) {
		t.Fatal(`panel-run.js does not call registerPanel("run", ...); it is not ` +
			"actually using the registry OR-70 added")
	}
	if !strings.Contains(src, `import { registerPanel } from "./panels.js"`) {
		t.Error("panel-run.js does not import registerPanel from panels.js")
	}
}

// panels-registered.js IS THE ONE FILE THAT NAMES PANELS -- deliberately,
// since ES modules have no filesystem discovery and something has to import
// each panel module for its top-level registerPanel call to run at all.
// This test is the flip side of TestAppJSNamesNoSpecificPanel: the naming
// has to happen SOMEWHERE, and it must be here, not in the shell.
func TestPanelsRegisteredImportsEveryPanelModule(t *testing.T) {
	src := servedFile(t, "/js/panels-registered.js")
	if !strings.Contains(src, `"./panel-run.js"`) {
		t.Fatal("panels-registered.js does not import panel-run.js; the run panel " +
			"would never register itself, and the page would render nothing")
	}
}

// app.js must actually load panels-registered.js -- otherwise the file
// above exists and does nothing, because nothing ever imports it and its
// side-effecting imports never run.
func TestAppJSLoadsThePanelsRegisteredFile(t *testing.T) {
	src := servedFile(t, "/js/app.js")
	if !strings.Contains(src, `"./panels-registered.js"`) {
		t.Fatal("app.js does not import panels-registered.js; no panel would ever " +
			"reach the registry")
	}
}

// A duplicate name must fail loudly, the way Handle's own doc says a
// duplicate server route panics at Listen rather than silently shadowing --
// this is the same design decision made twice, in two languages, and this
// test is that the JS half actually made it.
func TestDuplicatePanelNameThrows(t *testing.T) {
	src := servedFile(t, "/js/panels.js")
	if !strings.Contains(src, "already registered") {
		t.Fatal("panels.js has no guard against registering the same panel name twice")
	}
	if !strings.Contains(src, "throw new Error") {
		t.Error("the duplicate-name guard does not actually throw; a silent no-op would " +
			"leave two panels claiming one nav slot with no error anywhere")
	}
}

// panels.js is pure registration bookkeeping -- no vdom, and no dependency
// on the vendored runtime to have one. Checked by the two things rendering
// would actually require, not by the bare word "Component": that word is
// also registerPanel's own third PARAMETER name (the class component a
// panel hands in), and a naive substring match on it flagged that
// legitimate signature as if it were a render call.
func TestPanelsJSRendersNothing(t *testing.T) {
	src := servedFile(t, "/js/panels.js")
	if strings.Contains(src, "html`") {
		t.Error("panels.js contains a tagged html`...` template; it is meant to be pure " +
			"registration bookkeeping with no rendering concerns of its own")
	}
	if strings.Contains(src, "/vendor/") {
		t.Error("panels.js imports from the vendored runtime; a pure bookkeeping module " +
			"needing Preact or htm would suggest it has started rendering something")
	}
}
