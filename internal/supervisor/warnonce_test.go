package supervisor

import (
	"bytes"
	"strings"
	"testing"
)

// OR-240: a configuration warning is a property of the MACHINE, not of the
// ticket, so it is said once per process rather than once per run.
//
// The one this was filed for is OR-239's: "this run inherited YOUR Claude
// Code configuration ... because a curated config directory cannot
// authenticate on darwin". Every run gets an identically provisioned config
// directory, so it was as true on run forty as on run one -- and repeating it
// forty times taught the operator that the line is boilerplate, which is the
// fastest way to make a real warning invisible.
func TestAConfigurationWarningIsSaidOncePerProcess(t *testing.T) {
	msg := "this run inherited YOUR configuration; " + t.Name()

	var first, second, third bytes.Buffer
	warnOnce(&first, msg)
	warnOnce(&second, msg)
	warnOnce(&third, msg)

	if !strings.Contains(first.String(), msg) {
		t.Errorf("the first run must say it: %q", first.String())
	}
	if second.Len() != 0 || third.Len() != 0 {
		t.Errorf("runs two and three repeated it: %q %q", second.String(), third.String())
	}
}

// Once per WARNING, not once ever. A second, different problem still has to
// reach the operator -- suppressing it would trade one kind of blindness for
// a worse one.
func TestADifferentWarningIsStillSaid(t *testing.T) {
	var buf bytes.Buffer
	warnOnce(&buf, "nj-agents was not found; "+t.Name())
	warnOnce(&buf, "could not link a skill; "+t.Name())

	if n := strings.Count(buf.String(), "orion: "); n != 2 {
		t.Errorf("expected both warnings, got %d:\n%s", n, buf.String())
	}
}
