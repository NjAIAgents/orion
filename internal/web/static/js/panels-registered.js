// The whole reason OR-70 exists: adding a page is one line here, appended,
// never an edit to app.js or to any other panel's file.
//
// AN IMPORT FOR ITS SIDE EFFECT, not its exports. Each panel module calls
// registerPanel at its own top level (see panels.js and panel-run.js); this
// file's only job is making sure that top-level code has run before app.js
// asks the registry what exists. ES modules have no filesystem discovery
// the way //go:embed does, so this file is the one true concession to that
// -- OR-53 and OR-54 each add themselves by appending one import here, and
// nothing about panel-run.js or any other panel changes when they do,
// which is the collision this ticket exists to prevent.
import "./panel-run.js";
