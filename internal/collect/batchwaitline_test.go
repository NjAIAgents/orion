package collect

import (
	"os"
	"strings"
	"testing"
)

// The CI-wait line repeats once a minute for as long as the batch is testing.
// It used to read "orion/batch: 3 branch(es), 6m0s elapsed" -- a count, which
// is the one fact already visible from `orion queue`, and not the names, which
// are the fact a reader needs: what is at risk while this runs, and which
// tickets to open when it goes red.
//
// Asserted on keysOf rather than by capturing the printed line, because the
// keys are what the format string interpolates and a test that re-implemented
// the formatting would pass while the line said something else.
func TestTheCIWaitLineNamesTheTicketsRatherThanCountingThem(t *testing.T) {
	got := strings.Join(keysOf(members("OR-57", "OR-60", "OR-64")), " ")

	for _, want := range []string{"OR-57", "OR-60", "OR-64"} {
		if !strings.Contains(got, want) {
			t.Errorf("the wait line must name %s so a reader knows what is at risk; got %q", want, got)
		}
	}
	if strings.Contains(got, "branch(es)") {
		t.Errorf("a count is what the queue already shows; the line should carry names, got %q", got)
	}
}

// One member is still named, not summarised. A batch of one is the common case
// when tickets finish apart, and "1 branch(es)" was the least useful line of
// the set.
func TestASingleMemberBatchIsNamedToo(t *testing.T) {
	if got := strings.Join(keysOf(members("OR-55")), " "); got != "OR-55" {
		t.Errorf("a one-member batch should read as its key; got %q", got)
	}
}

// The test above proves keysOf returns the names; it does not prove the wait
// line USES them, and a revert of the format string alone would leave it
// green. So this reads the source.
//
// Asserting on source text is a blunt instrument and not a habit worth
// spreading -- it is here because the alternative is threading a writer and a
// git double through resumeBatch's whole pending path to observe one line,
// which is a large fixture for a small guarantee, and because the regression
// being guarded is precisely "someone put the count back".
func TestTheWaitLineDoesNotCountBranchesInSource(t *testing.T) {
	src, err := os.ReadFile("batchrun.go")
	if err != nil {
		t.Fatal(err)
	}
	// The line under test: ui.Ok(w, "ci", ...) inside the pending branch.
	for _, line := range strings.Split(string(src), "\n") {
		if !strings.Contains(line, `ui.Ok(w, "ci"`) {
			continue
		}
		if strings.Contains(line, "branch(es)") {
			t.Errorf("the CI wait line counts branches instead of naming them:\n  %s",
				strings.TrimSpace(line))
		}
		if !strings.Contains(line, "%s: %s") {
			t.Errorf("the CI wait line should interpolate the ref and the joined keys:\n  %s",
				strings.TrimSpace(line))
		}
		return
	}
	t.Error(`no ui.Ok(w, "ci", ...) line found in batchrun.go; this test has drifted from the code`)
}
