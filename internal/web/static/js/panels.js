// The panel/route registry (OR-70): adding a page needs one file, not an
// edit to a central router.
//
// THE SAME SEAM server.go ALREADY OWNS, one layer up. Handle(pattern,
// handler) in internal/web/server.go lets a Go file register its own route
// from its own init(), so OR-53 and OR-54 can each add a page without
// touching a shared switch -- the exact collision that produced the
// src/fcia/cli.py failure this epic keeps citing. registerPanel is that
// same idea client-side: a panel module calls it once, at module scope, and
// nothing in this file or in app.js ever names a specific panel.
//
// ES MODULES HAVE NO FILESYSTEM DISCOVERY, unlike //go:embed. A panel's
// module only runs once something imports it, so "one file" here means one
// file PLUS one import line in panels-registered.js -- the smallest true
// equivalent of Go's init() seam that a bundler-free, build-step-free
// front end (OR-66) can offer. Nothing about a panel's own code changes
// when it is added; the one line lives in a file whose only job is holding
// that list, so two panels added at once collide on one line rather than
// on logic.

// panels is registration order -- the same order the nav bar draws in,
// which is what "run" being first and on by default depends on.
const panels = [];

// registerPanel adds one page. name is the URL hash fragment ("run",
// "gates"); title is the nav label; Component is a class component (no
// hooks -- VENDOR.md rules them out) that receives no props and renders the
// whole page body for that route.
export function registerPanel(name, title, Component) {
  if (panels.some((p) => p.name === name)) {
    // A DUPLICATE NAME IS A BUG WORTH FAILING LOUDLY ON, the same reasoning
    // Handle's own doc gives for why registering the same server pattern
    // twice panics when Listen builds the mux: at the point the mistake was
    // made, rather than as two nav items that silently shadow each other
    // and leave a reader wondering why clicking one does nothing.
    throw new Error(`panel "${name}" is already registered`);
  }
  panels.push({ name, title, Component });
}

// listPanels returns the registered set, in registration order. A copy, so
// a caller iterating it cannot mutate the registry through the reference.
export function listPanels() {
  return panels.slice();
}
