package main

import (
	"os"
	"testing"

	"github.com/orion-sdlc/orion/internal/fakebin"
)

// TestMain lets a fakebin copy of this binary act as the fake it was
// installed to be -- see internal/fakebin. On unix, and for the ordinary
// test-binary invocation, Main returns immediately.
func TestMain(m *testing.M) {
	fakebin.Main()
	os.Exit(m.Run())
}
