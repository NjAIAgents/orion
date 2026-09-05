package conform

// Acceptance criterion for OR-158: "up to three divergences are extracted
// from the reply; more than three is silently capped." ConformPrompt tells
// the model AT MOST THREE, but ParseReply itself enforces no such cap --
// every DIVERGES: line found is appended, however many there are. This test
// pins the acceptance criterion as written and is expected to fail against
// the current implementation; see reply_decoration_test.go for the same
// finding noted at the point ParseReply's decoration handling was tested.

import "testing"

func TestMoreThanThreeDivergencesAreSilentlyCapped(t *testing.T) {
	reply := ReplyDiverges + " one\n" +
		ReplyDiverges + " two\n" +
		ReplyDiverges + " three\n" +
		ReplyDiverges + " four\n" +
		ReplyDiverges + " five"

	found, ok := ParseReply(reply)
	if !ok {
		t.Fatalf("reply with five DIVERGES: lines did not parse as a divergence: ok=%v", ok)
	}
	if len(found) > 3 {
		t.Errorf("ParseReply returned %d divergences, want at most 3 (the reply told the "+
			"model AT MOST THREE, but nothing in ParseReply enforces it): %+v", len(found), found)
	}
}
