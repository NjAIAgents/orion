package main

import (
	"testing"

	"github.com/orion-sdlc/orion/internal/fakebin"
)

// Thin wrappers over internal/fakebin, which is where the portability
// argument lives: on Windows a fake is a copy of this very test binary
// (dispatched via TestMain), because a .bat shim truncates any argument
// containing a newline -- which every multi-line prompt does (OR-342).

func writeFakeBin(t *testing.T, name, script string) string {
	t.Helper()
	return fakebin.Install(t, t.TempDir(), name, script)
}

func writeFakeBinIn(t *testing.T, dir, name, script string) string {
	t.Helper()
	return fakebin.Install(t, dir, name, script)
}

func shPath(p string) string { return fakebin.ShPath(p) }
