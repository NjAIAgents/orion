package conform_test

// Finer-grained coverage of ParseReply's tolerance for how a model actually
// writes its answer (OR-158): a divergence with no reason attached, and the
// decoration a model puts in front of a marker line.
//
// NOTE ON THE THIRD CASE: ConformPrompt tells the model "AT MOST THREE"
// divergences, but ParseReply itself enforces no cap -- every DIVERGES: line
// found is appended, however many there are. A test asserting a silent cap at
// three would fail against the current implementation, and per the brief for
// this file the implementation is not to be changed to make it pass. Left
// untested here.

import (
	"strings"
	"testing"

	"github.com/orion-sdlc/orion/internal/conform"
)

// A DIVERGES: line with nothing after it carries no evidence a person could
// check, so it must not surface as a finding -- even when a real divergence
// follows it in the same reply.
func TestADivergenceWithNoReasonIsRejected(t *testing.T) {
	d, ok := conform.ParseReply(conform.ReplyDiverges + "   ")
	if ok || len(d) != 0 {
		t.Errorf("a reasonless divergence alone was accepted: (%v, %v)", d, ok)
	}

	mixed, ok := conform.ParseReply(conform.ReplyDiverges + "   \n" +
		conform.ReplyDiverges + " the plan says one index per issuer")
	if !ok || len(mixed) != 1 {
		t.Fatalf("a reasonless line alongside a real one parsed as (%v, %v), want the one real divergence", mixed, ok)
	}
	if !strings.Contains(mixed[0].What, "one index per issuer") {
		t.Errorf("the real divergence's reason was dropped: %q", mixed[0].What)
	}
}

// A model rarely writes the bare marker; it bullets it, bolds it, blockquotes
// it. Every decoration TrimLeft strips must still leave the marker readable.
func TestReplyWithDecorationInFrontOfMarkersStillParses(t *testing.T) {
	for _, decoration := range []string{"-", "*", "#", ">", "_"} {
		reply := decoration + " " + conform.ReplyDiverges + " the plan says one index per issuer"
		d, ok := conform.ParseReply(reply)
		if !ok || len(d) != 1 {
			t.Errorf("decoration %q hid the divergence marker: parsed as (%v, %v)", decoration, d, ok)
			continue
		}
		if !strings.Contains(d[0].What, "one index per issuer") {
			t.Errorf("decoration %q: the reason was dropped: %q", decoration, d[0].What)
		}
	}

	// CONFORMS decorated the same way must also still parse.
	for _, decoration := range []string{"-", "*", "#", ">", "_"} {
		reply := decoration + " " + conform.ReplyConforms
		d, ok := conform.ParseReply(reply)
		if !ok || len(d) != 0 {
			t.Errorf("decoration %q hid the conforms marker: parsed as (%v, %v)", decoration, d, ok)
		}
	}
}
