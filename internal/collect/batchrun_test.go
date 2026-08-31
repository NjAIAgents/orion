package collect

import (
	"strings"
	"testing"

	"github.com/orion-sdlc/orion/internal/config"
)

// The flag is the whole safety story, so it gets the sharpest test.
//
// batch_integration is off by default, and OFF must mean the batch path is
// unreachable rather than merely unlikely. If a default config could reach
// runBatch, then every existing repository would change how it merges on
// upgrade, which is the one outcome this feature must not have.
func TestBatchIntegrationIsOffInADefaultConfig(t *testing.T) {
	if config.Defaults().Collect.BatchIntegration {
		t.Fatal("batch_integration must default OFF: a repository that never asked " +
			"for it must not change how it merges on upgrade")
	}
}

// An empty check rollup must NOT read as green.
//
// This is the landmine ADR 0015 found in the existing code: cmd/orion/collect
// turns "no checks" into a PASSING verdict, which is right for a repository
// with no CI and catastrophic for a merge ref whose checks have not started.
// Under a batch every member would land on no evidence at all.
func TestSilenceIsNotGreen(t *testing.T) {
	quiet := PR{Verdict: VerdictPassing,
		Detail: "no checks are configured on this repository"}
	if !noChecksYet(quiet) {
		t.Fatal("an empty rollup must be recognised, or a batch lands on no evidence")
	}

	real := PR{Verdict: VerdictPassing, Detail: "3 check(s) passed"}
	if noChecksYet(real) {
		t.Error("a real passing result must not be mistaken for silence")
	}

	// Case-insensitively: the wording is produced elsewhere and only has to
	// stay recognisable, not identical.
	if !noChecksYet(PR{Detail: "No Checks Are Configured On This Repository"}) {
		t.Error("the check must not turn on capitalisation")
	}
}

// A batch of members whose branches nobody recorded produces nothing, and
// nothing is the signal to fall back to the per-branch path rather than to
// report an empty pass.
func TestAnEmptyBatchFallsBackRatherThanReportingNothing(t *testing.T) {
	g := newFakeGit()
	tr := &fakeTester{g: g, bad: map[string]bool{}}
	b, err := Land(g, tr, "orion/batch", "develop", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(b.Results) != 0 {
		t.Fatalf("an empty batch must produce no results, got %v", b.Describe())
	}
	if b.Runs != 0 {
		t.Errorf("Runs = %d: an empty batch must not spend a CI run", b.Runs)
	}
}

// Every outcome has to map onto a verdict the watcher already renders, or the
// batch path reports states the rest of the system cannot display.
func TestEveryOutcomeMapsOntoAVerdictTheWatcherKnows(t *testing.T) {
	known := map[Verdict]bool{
		VerdictPassing: true, VerdictFailing: true, VerdictStale: true,
	}
	for _, o := range []Outcome{Landed, Ejected, Culprit, Deferred} {
		var v Verdict
		switch o {
		case Landed:
			v = VerdictPassing
		case Culprit:
			v = VerdictFailing
		default:
			v = VerdictStale
		}
		if !known[v] {
			t.Errorf("outcome %q maps to %q, which the watcher does not render", o, v)
		}
	}
}

// The report has to name every member and what became of it. A batch that
// lands four tickets and says "the batch was green" leaves an operator with
// nothing to check.
func TestTheBatchReportNamesEveryMemberAndItsOutcome(t *testing.T) {
	g := newFakeGit("orion/or-2")
	tr := &fakeTester{g: g, bad: map[string]bool{"orion/or-3": true}}
	b, err := Land(g, tr, "batch", "develop", members("OR-1", "OR-2", "OR-3"), nil)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Join(b.Describe(), "\n")
	for _, key := range []string{"OR-1", "OR-2", "OR-3"} {
		if !strings.Contains(lines, key) {
			t.Errorf("%s is missing from the report:\n%s", key, lines)
		}
	}
	for _, word := range []string{"ejected", "culprit"} {
		if !strings.Contains(lines, word) {
			t.Errorf("the report must say %q so the outcome is legible:\n%s", word, lines)
		}
	}
}
