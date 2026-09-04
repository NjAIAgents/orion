package supervisor

import (
	"strings"
	"testing"
)

// A fan-out landing line's four remaining OR-335 requirements: it counts
// against the total, it repeats what the child was given so a landing can be
// matched back to its dispatch, and its outcome lives in the verb column
// alone -- "exit 0" restated what "ok" already said, and a non-zero exit
// still names its code because there the number is the finding.

func landingJobs() []Options {
	return []Options{
		{Stage: "qa", Prompt: "PASS", MaxMinutes: 1, MaxTurns: 1, Actor: "qa",
			Model: "sonnet", Key: "OR-335", About: "7 case(s)"},
		{Stage: "qa", Prompt: "FAIL", MaxMinutes: 1, MaxTurns: 1, Actor: "qa",
			Model: "sonnet", Key: "OR-335", About: "9 case(s)"},
	}
}

// landingFakeClaude exits 1 on the job whose prompt is "FAIL", so a landing
// line for that child carries a real non-zero exit rather than a fabricated
// one.
const landingFakeClaude = `for a in "$@"; do
  if [ "$a" = "FAIL" ]; then
    echo "boom" >&2
    exit 3
  fi
done
echo '` + fanResultJSON + `'
exit 0
`

func TestFanLandingLinesShowProgressAsCountOverTotal(t *testing.T) {
	fakeClaudeTree(t, landingFakeClaude)
	out := captureFanOut(t)

	Fan(ws(t, ""), landingJobs())

	got := out.String()
	if !strings.Contains(got, "1/2") || !strings.Contains(got, "2/2") {
		t.Errorf("landings do not count against the total, so nothing says what is still "+
			"outstanding: %q", got)
	}
}

func TestFanLandingLinesRepeatWhatTheChildWasGiven(t *testing.T) {
	fakeClaudeTree(t, landingFakeClaude)
	out := captureFanOut(t)

	Fan(ws(t, ""), landingJobs())

	got := out.String()
	for _, about := range []string{"7 case(s)", "9 case(s)"} {
		if strings.Count(got, about) < 2 {
			t.Errorf("%q appears on fewer than two lines -- a landing that does not repeat "+
				"what its dispatch line said cannot be matched back to it: %q", about, got)
		}
	}
}

func TestFanLandingReportsOutcomeThroughTheVerbColumnNotExitZero(t *testing.T) {
	fakeClaudeTree(t, landingFakeClaude)
	out := captureFanOut(t)

	Fan(ws(t, ""), landingJobs())

	got := out.String()
	if strings.Contains(got, "exit 0") {
		t.Errorf("a landing still spells out \"exit 0\" -- the verb column already says the "+
			"child worked, and a line has no room to say the same fact twice: %q", got)
	}
	if !strings.Contains(got, "ok") {
		t.Errorf("the passing child's landing never says it worked: %q", got)
	}
}

func TestFanLandingWithNonZeroExitNamesTheCodeInTheMessage(t *testing.T) {
	fakeClaudeTree(t, landingFakeClaude)
	out := captureFanOut(t)

	Fan(ws(t, ""), landingJobs())

	got := out.String()
	if !strings.Contains(got, "exit 3") {
		t.Errorf("the failing child's landing never names its exit code, so the one case "+
			"where the number IS the finding says nothing: %q", got)
	}
	if !strings.Contains(got, "failed") {
		t.Errorf("the failing child's landing never says it failed: %q", got)
	}
}
