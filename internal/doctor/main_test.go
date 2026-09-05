package doctor

import (
	"os"
	"testing"

	"github.com/orion-sdlc/orion/internal/fakebin"
)

// TestMain lets a fakebin copy of this binary act as the fake it was
// installed to be -- see internal/fakebin.
func TestMain(m *testing.M) {
	fakebin.Main()
	os.Exit(m.Run())
}
