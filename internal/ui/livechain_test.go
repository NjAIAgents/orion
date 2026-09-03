package ui

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

// The reported defect: at 76 columns the batch line clipped the elapsed, the
// median and the run number with "…", and the check line clipped checks four
// to six. No line of the block may clip, in any phase (OR-319).
func TestNoLineOfTheBatchBlockClipsAt76Columns(t *testing.T) {
	now := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)
	checks := []Check{
		{"go (ubuntu-latest)", CheckRunning}, {"go (macos-latest)", CheckRunning},
		{"go (windows-latest)", CheckRunning}, {"codeql", CheckPassed},
		{"analyze go", CheckPassed}, {"analyze actions", CheckRunning},
	}
	for _, phase := range []BatchPhase{BatchAssembling, BatchTesting, BatchIsolating, BatchDone} {
		LiveReset()
		LiveBatchStart("orion/batch", "develop", []string{"OR-223", "OR-224", "OR-229", "OR-242"})
		LiveBatchMedian(11 * time.Minute)
		liveBatchPhase(BatchTesting, now.Add(-4*time.Minute))
		liveBatchPhase(phase, now.Add(-4*time.Minute))
		for _, k := range []string{"OR-223", "OR-224", "OR-242"} {
			LiveBatchMember(k, MemberMerged)
		}
		LiveBatchMemberDetail("OR-229", MemberEjected, "internal/work/activity_test.go")
		LiveChecks(checks)
		var buf bytes.Buffer
		for _, line := range renderBatch(&buf, liveSnapshot().batch, now, 76) {
			p := plain(line)
			if strings.Contains(p, "…") {
				t.Errorf("%s: a batch line is clipped at 76 columns: %q", phase, p)
			}
			if n := displayCells(p); n > 76 {
				t.Errorf("%s: a batch line is %d cells wide: %q", phase, n, p)
			}
		}
	}
	LiveReset()
}

// The rule carries the verdict and the measure; the chain names every member
// riding on the build and none that is not; the jobs sit three to a row with
// a tally beneath.
func TestTheCIBlockIsRuleChainJobsAndTally(t *testing.T) {
	now := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)
	b := &liveBatch{
		ref: "orion/batch", base: "develop", phase: BatchTesting, runs: 1,
		median: 11 * time.Minute, ciStarted: now.Add(-4 * time.Minute),
		members: []batchMember{
			{key: "OR-223", state: MemberMerged}, {key: "OR-224", state: MemberMerged},
			{key: "OR-229", state: MemberEjected}, {key: "OR-242", state: MemberMerged},
		},
		checks: []Check{
			{"go (ubuntu-latest)", CheckPassed}, {"go (macos-latest)", CheckRunning},
			{"go (windows-latest)", CheckRunning}, {"codeql", CheckPassed},
		},
	}
	var buf bytes.Buffer
	lines := ciBlock(&buf, b, now, 76)
	for i := range lines {
		lines[i] = plain(lines[i])
	}
	got := strings.Join(lines, "\n")

	if !strings.Contains(lines[0], "CI") || !strings.Contains(lines[0], "running") ||
		!strings.Contains(lines[0], "4m00s / ~11m median") {
		t.Errorf("the rule must carry the verdict and the measure: %q", lines[0])
	}
	chain := lines[1]
	for _, k := range []string{"OR-223", "OR-224", "OR-242"} {
		if !strings.Contains(chain, k) {
			t.Errorf("the chain does not name %s: %q", k, chain)
		}
	}
	if strings.Contains(chain, "229") {
		t.Errorf("an ejected member is not riding on this build and must be absent from the chain: %q", chain)
	}
	if !strings.Contains(chain, "━") || !strings.Contains(chain, "─") {
		t.Errorf("the chain must show a partial fill at 4 of 11 minutes: %q", chain)
	}
	// Jobs three to a row, short names, then the tally.
	if !strings.Contains(lines[2], "go ubuntu") || !strings.Contains(lines[2], "go windows") {
		t.Errorf("the first job row must hold three jobs: %q", lines[2])
	}
	if strings.Contains(got, "-latest") || strings.Contains(got, "(") {
		t.Errorf("job names must be the short form: %s", got)
	}
	if !strings.Contains(got, "2 of 4 green · waiting on 2") {
		t.Errorf("the tally is missing or wrong:\n%s", got)
	}
}

// A red run names the failing job in the tally and the failing test beneath.
func TestARedRunNamesTheJobAndTheTest(t *testing.T) {
	now := time.Now()
	b := &liveBatch{
		ref: "orion/batch", base: "develop", phase: BatchIsolating, runs: 2,
		members: []batchMember{
			{key: "OR-223", state: MemberMerged},
			{key: "OR-242", state: MemberCulprit, detail: "go ubuntu: TestPathLength"},
		},
		checks: []Check{{"go (ubuntu-latest)", CheckFailed}, {"go (macos-latest)", CheckPassed}},
	}
	var buf bytes.Buffer
	got := plain(strings.Join(ciBlock(&buf, b, now, 76), "\n"))
	if !strings.Contains(got, "1 of 2 red · go ubuntu") {
		t.Errorf("the failing job is not named in the tally:\n%s", got)
	}
	if !strings.Contains(got, "go ubuntu: TestPathLength") {
		t.Errorf("the failing test is not named beneath the jobs:\n%s", got)
	}
}

// The status line has to fit inside the rule above it. At two spaces a side
// it ran to 96 cells and wrapped at 76 columns (OR-319).
func TestTheStatusLineFitsInsideTheRule(t *testing.T) {
	LiveReset()
	t.Cleanup(LiveReset)
	now := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)
	for _, k := range []string{"OR-223", "OR-224", "OR-242"} {
		liveStart(k, now.Add(-18*time.Minute))
	}
	LiveSpend(12.40)
	LiveCI(3)
	LiveBatchStart("orion/batch", "develop", []string{"OR-223"})

	var buf bytes.Buffer
	got := plain(renderHeaderAt(&buf, liveSnapshot(), now, false))
	if n := displayCells(got); n > liveRuleWidth {
		t.Errorf("the status line is %d cells, wider than the %d-cell rule: %q", n, liveRuleWidth, got)
	}
	if !strings.HasSuffix(got, "ctrl-o collapses") {
		t.Errorf("the hint sits at the right edge: %q", got)
	}
	if strings.Contains(got, "in CI") {
		t.Errorf("during a batch the CI block owns the build; the header must not count it: %q", got)
	}
}
