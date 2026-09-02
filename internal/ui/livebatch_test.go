package ui

import (
	"bytes"
	"io"
	"strings"
	"testing"
	"time"
)

// plain strips escape codes so a test asserts on TEXT, not on formatting.
func plain(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == 0x1b {
			for i < len(s) && s[i] != 'm' {
				i++
			}
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

func batchLines(t *testing.T, now time.Time) []string {
	t.Helper()
	var buf bytes.Buffer
	st := liveSnapshot()
	out := renderBatch(&buf, st.batch, now, 200)
	for i := range out {
		out[i] = plain(out[i])
	}
	return out
}

func joined(t *testing.T, now time.Time) string {
	t.Helper()
	return strings.Join(batchLines(t, now), "\n")
}

// The rule the whole mode exists for. During CI there is one run, so there
// must be exactly one bar -- four bars filled from one shared deadline would
// all read the same value and imply four independent things are progressing.
func TestTestingDrawsOneBarForTheWholeBatchAndNotOnePerMember(t *testing.T) {
	LiveReset()
	defer LiveReset()
	start := time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)

	LiveBatchStart("orion/batch", "develop", []string{"OR-223", "OR-224", "OR-242"})
	LiveBatchMedian(11 * time.Minute)
	liveBatchPhase(BatchTesting, start)

	got := joined(t, start.Add(4*time.Minute))
	if n := strings.Count(got, barFullGlyph); n == 0 {
		t.Fatalf("no bar was drawn at all:\n%s", got)
	}
	// One line carrying a bar, not three.
	bars := 0
	for _, line := range batchLines(t, start.Add(4*time.Minute)) {
		if strings.Contains(line, barFullGlyph) || strings.Contains(line, barEmptyGlyph) {
			bars++
		}
	}
	if bars != 1 {
		t.Errorf("%d lines carry a bar; a shared CI run must have exactly one:\n%s", bars, got)
	}
	// Named by NUMBER, in the segmented membership bar. The project prefix is
	// dropped there because it is identical on every member of one batch, so
	// it would spend a third of each segment saying nothing (OR-264).
	for _, k := range []string{"223", "224", "242"} {
		if !strings.Contains(got, k) {
			t.Errorf("%s is not named in the batch:\n%s", k, got)
		}
	}
}

// Ejected must not read as failed. An ejected branch is sound and comes back;
// a culprit broke CI. Rendering them alike sends someone to debug a branch
// with nothing wrong with it.
func TestEjectedIsVisuallyAndVerballyDistinctFromCulprit(t *testing.T) {
	LiveReset()
	defer LiveReset()
	var buf bytes.Buffer

	ejGlyph, ejNote := memberGlyph(&buf, MemberEjected)
	cuGlyph, cuNote := memberGlyph(&buf, MemberCulprit)

	if plain(ejGlyph) == plain(cuGlyph) {
		t.Errorf("ejected and culprit share the glyph %q", plain(ejGlyph))
	}
	if ejNote == "" {
		t.Error("an ejection must say what happens next, or it reads as a failure")
	}
	if !strings.Contains(plain(ejNote), "returns to the queue") {
		t.Errorf("the ejection note does not say the branch comes back: %q", plain(ejNote))
	}
	if plain(ejNote) == plain(cuNote) {
		t.Error("ejected and culprit say the same thing")
	}
}

// The cost figure is the argument for the feature, so it must appear in the
// summary and must be right. It printed last in the first cut, which read as
// bookkeeping rather than as the result.
func TestTheCostLineStatesRunsPerBranchAndComesFirst(t *testing.T) {
	LiveReset()
	defer LiveReset()
	now := time.Now()

	LiveBatchStart("orion/batch", "develop", []string{"OR-1", "OR-2", "OR-3", "OR-4"})
	liveBatchPhase(BatchTesting, now)
	for _, k := range []string{"OR-1", "OR-2", "OR-3", "OR-4"} {
		LiveBatchMember(k, MemberLanded)
	}
	LiveBatchPhase(BatchDone)

	lines := batchLines(t, now)
	if len(lines) == 0 {
		t.Fatal("the done phase rendered nothing")
	}
	if !strings.Contains(lines[0], "1 CI run for 4 branches") {
		t.Errorf("the first line is not the cost figure: %q", lines[0])
	}
}

// Singular and plural both have to read correctly, because this line is the
// headline and "1 CI runs for 1 branches" undercuts it.
func TestTheCostLineAgreesInNumber(t *testing.T) {
	LiveReset()
	defer LiveReset()
	now := time.Now()

	LiveBatchStart("orion/batch", "develop", []string{"OR-1"})
	liveBatchPhase(BatchTesting, now)
	LiveBatchMember("OR-1", MemberLanded)

	var buf bytes.Buffer
	st := liveSnapshot()
	if got := plain(st.batch.costLine(&buf)); got != "1 CI run for 1 branch" {
		t.Errorf("costLine = %q, want %q", got, "1 CI run for 1 branch")
	}
}

// The tree is what explains the cost. A row per member could name the culprit;
// only the tree says why finding it took four runs rather than one.
func TestIsolationDrawsTheTreeAndMarksTheCulprit(t *testing.T) {
	LiveReset()
	defer LiveReset()
	now := time.Now()

	LiveBatchStart("orion/batch", "develop", []string{"OR-223", "OR-224", "OR-242"})
	LiveBatchPhase(BatchIsolating)
	LiveBatchSplit([]string{"OR-223", "OR-224", "OR-242"}, false, 0, 1, false)
	LiveBatchSplit([]string{"OR-223", "OR-224"}, true, 1, 2, false)
	LiveBatchSplit([]string{"OR-242"}, false, 1, 3, true)

	got := joined(t, now)
	if !strings.Contains(got, "culprit") {
		t.Errorf("the culprit is not marked:\n%s", got)
	}
	if !strings.Contains(got, "run 3") {
		t.Errorf("the run count is missing, which is the number the search is judged by:\n%s", got)
	}
	// The losing side must not be drawn as a pass.
	for _, line := range batchLines(t, now) {
		if strings.Contains(line, "[OR-242]") && strings.Contains(line, "✓") {
			t.Errorf("the culprit leaf is marked green: %q", line)
		}
	}
}

// Off a TTY the contract is a log, not an animation: one line per tick, no
// bar, no spinner, no cursor control.
func TestOffTerminalTheBatchIsOnePlainLineWithNoBar(t *testing.T) {
	LiveReset()
	defer LiveReset()
	now := time.Now()

	LiveBatchStart("orion/batch", "develop", []string{"OR-1", "OR-2"})
	liveBatchPhase(BatchTesting, now)
	LiveBatchMember("OR-2", MemberEjected)

	line := renderBatchPlain(liveSnapshot().batch, now)
	if line == "" {
		t.Fatal("the batch produced no plain line")
	}
	for _, g := range []string{barFullGlyph, barHeadGlyph, spinnerGlyphs[:3], "\x1b"} {
		if strings.Contains(line, g) {
			t.Errorf("the plain line carries terminal-only output %q: %s", g, line)
		}
	}
	if !strings.Contains(line, "1 ejected") {
		t.Errorf("the plain line does not report the ejection: %s", line)
	}
}

// A batch has no rows of its own -- the agents have finished by the time it
// assembles -- so the region must stay alive on the batch alone. Otherwise the
// thirty-minute CI wait renders nothing, which is the silence OR-240 removed
// reappearing in the new path.
func TestTheRegionSurvivesOnABatchWithNoRunningRows(t *testing.T) {
	LiveReset()
	defer LiveReset()
	var buf bytes.Buffer
	now := time.Now()

	if got := renderRegion(&buf, liveSnapshot(), now, 200); got != nil {
		t.Fatalf("an empty registry must render nothing, got %v", got)
	}
	LiveBatchStart("orion/batch", "develop", []string{"OR-1"})
	liveBatchPhase(BatchTesting, now)

	if got := renderRegion(&buf, liveSnapshot(), now, 200); len(got) == 0 {
		t.Error("a batch with no running rows rendered nothing; the CI wait would be silent")
	}
}

// The batch is drawn BELOW the ticket rows, under the status line.
//
// It sat above them originally, on the reasoning that a set should be read
// before its members. In practice the rows are what changes second to second
// and the batch is the summary they roll up into, so it belongs next to the
// other summary at the bottom of the region rather than pushing the moving
// part down the screen (OR-264).
func TestTheBatchIsDrawnBelowTheTicketRows(t *testing.T) {
	LiveReset()
	defer LiveReset()
	var buf bytes.Buffer
	now := time.Now()

	liveStart("OR-9", now)
	LiveBatchStart("orion/batch", "develop", []string{"OR-9"})
	liveBatchPhase(BatchTesting, now)

	lines := renderRegion(&buf, liveSnapshot(), now, 200)
	batchAt, rowAt := -1, -1
	for i, l := range lines {
		p := plain(l)
		if batchAt < 0 && strings.Contains(p, "batch") && strings.Contains(p, "CI") {
			batchAt = i
		}
		// The ticket row is the one carrying the stage; the membership bar
		// names the key too.
		if rowAt < 0 && strings.Contains(p, "OR-9") && strings.Contains(p, "starting") {
			rowAt = i
		}
	}
	if batchAt < 0 || rowAt < 0 {
		t.Fatalf("batch=%d row=%d in:\n%s", batchAt, rowAt, strings.Join(lines, "\n"))
	}
	if batchAt < rowAt {
		t.Errorf("the batch is drawn above its members (batch=%d row=%d)", batchAt, rowAt)
	}
}

// A member resolved that the batch was never opened with still gets shown.
// Dropping it silently is how the display would come to disagree with what
// actually merged.
func TestAMemberResolvedLateIsShownRatherThanDropped(t *testing.T) {
	LiveReset()
	defer LiveReset()

	LiveBatchStart("orion/batch", "develop", []string{"OR-1"})
	LiveBatchMember("OR-7", MemberMerged)

	if got := liveSnapshot().batch.batchKeys(); len(got) != 2 {
		t.Errorf("keys = %v, want both OR-1 and the late OR-7", got)
	}
}

// Entering the testing phase starts a run. Two calls that could get out of
// step would let the bar measure one run while the counter reported another.
func TestEnteringTestingAdvancesTheRunCounterAndTheClockTogether(t *testing.T) {
	LiveReset()
	defer LiveReset()
	at := time.Date(2026, 8, 31, 9, 0, 0, 0, time.UTC)

	LiveBatchStart("orion/batch", "develop", []string{"OR-1"})
	liveBatchPhase(BatchTesting, at)
	b := liveSnapshot().batch
	if b.runs != 1 || !b.ciStarted.Equal(at) {
		t.Fatalf("runs=%d ciStarted=%v, want 1 and %v", b.runs, b.ciStarted, at)
	}
	liveBatchPhase(BatchTesting, at.Add(time.Minute))
	b = liveSnapshot().batch
	if b.runs != 2 || !b.ciStarted.Equal(at.Add(time.Minute)) {
		t.Errorf("a second run did not reset both: runs=%d ciStarted=%v", b.runs, b.ciStarted)
	}
}

// Every call must be a no-op with no batch open, because collect calls these
// on a path that runs whether or not batching is enabled.
func TestEveryBatchCallIsSafeWithNoBatchOpen(t *testing.T) {
	LiveReset()
	defer LiveReset()

	LiveBatchPhase(BatchTesting)
	LiveBatchMember("OR-1", MemberLanded)
	LiveBatchSplit([]string{"OR-1"}, false, 0, 1, true)
	LiveBatchMedian(time.Minute)
	LiveBatchEnd()

	if b := liveSnapshot().batch; b != nil {
		t.Errorf("a batch appeared without one being started: %+v", b)
	}
}

// A bar with no median draws nothing, which is honest -- inventing a baseline
// is what OR-250 forbids. Drawing nothing SILENTLY is not: fourteen blank
// columns read as a display that was never built, and were read exactly that
// way. The absence has to say what it is (OR-264).
func TestAMissingBaselineIsStatedRatherThanDrawnAsBlankSpace(t *testing.T) {
	now := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	r := liveRun{key: "OR-223", started: now.Add(-18 * time.Minute)}

	notes := r.notes(now)
	if !hasNote(notes, "no baseline yet") {
		t.Errorf("a run with no median must say so rather than rendering blank: %+v", notes)
	}
	// And the bar itself still draws nothing: the fix is the words beside it,
	// not a bar against a number nobody has.
	if got := r.bar(io.Discard, 18*time.Minute); strings.TrimSpace(got) != "" {
		t.Errorf("a bar with no median must stay blank, got %q", got)
	}
}

// The batch bar carries the same rule, and labels the number as a MEDIAN.
// Without that word "/ ~11m" reads as an estimate of when this run finishes,
// which is the prediction the bar deliberately refuses to make.
func TestTheBatchBarNamesItsMedianAndSaysWhenThereIsNone(t *testing.T) {
	now := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)

	withMedian := &liveBatch{
		ref: "orion/batch", base: "develop", phase: BatchTesting, runs: 1,
		members: []batchMember{{key: "OR-223"}}, median: 11 * time.Minute,
		ciStarted: now.Add(-4 * time.Minute),
	}
	got := strings.Join(renderTesting(io.Discard, withMedian, now, 200), "\n")
	if !strings.Contains(got, "median") {
		t.Errorf("the batch bar does not name what its number is:\n%s", got)
	}

	none := &liveBatch{
		ref: "orion/batch", base: "develop", phase: BatchTesting, runs: 1,
		members: []batchMember{{key: "OR-223"}}, ciStarted: now.Add(-4 * time.Minute),
	}
	if got := strings.Join(renderTesting(io.Discard, none, now, 200), "\n"); !strings.Contains(got, "no baseline yet") {
		t.Errorf("a batch with no baseline must say so:\n%s", got)
	}
}

// An ejected branch is out of THIS run, not out of the picture. Dropping it
// from the testing view left a batch of four naming three members with
// nothing anywhere accounting for the fourth.
func TestAnEjectedMemberKeepsItsRowWhileTheBatchIsTesting(t *testing.T) {
	now := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	b := &liveBatch{
		ref: "orion/batch", base: "develop", phase: BatchTesting, runs: 1,
		members: []batchMember{
			{key: "OR-223"}, {key: "OR-229", state: MemberEjected}, {key: "OR-242"},
		},
		median: 11 * time.Minute, ciStarted: now.Add(-4 * time.Minute),
	}
	got := strings.Join(renderTesting(io.Discard, b, now, 200), "\n")

	if !strings.Contains(got, "OR-229") {
		t.Errorf("the ejected member vanished from the testing view:\n%s", got)
	}
	if !strings.Contains(got, "returns to the queue") {
		t.Errorf("the ejected row must say it is coming back, not read as a failure:\n%s", got)
	}
	// It is NOT in the shared-run key list: that line is "who is in this CI
	// run", and it is not.
	keyLine := strings.Split(got, "\n")[1]
	if strings.Contains(keyLine, "OR-229") {
		t.Errorf("an ejected branch must not be listed as part of the CI run:\n%s", keyLine)
	}
}

// isTicketRow distinguishes a rendered run from the batch's own member list,
// which also names several keys on one line. A row leads with a spinner.
func isTicketRow(p string) bool {
	t := strings.TrimSpace(p)
	if t == "" {
		return false
	}
	return strings.ContainsAny(string([]rune(t)[0]), spinnerGlyphs+spinnerASCII)
}

// While the batch is TESTING the rows pair two to a line. That phase is the
// one where a row each says least -- one CI run covers every member, so the
// stage, the bar and the sparkline are the same story repeated -- and the
// Every ticket keeps its OWN row while the batch tests, including the stage,
// the actor and the sparkline.
//
// They were paired two to a line for a while, following the mockup's compact
// testing view. Seen on a real terminal that traded away the columns that say
// what each agent is DOING for vertical space the region did not need once
// the batch block moved below the status line, so the pairing came out again
// (OR-264).
func TestEveryTicketKeepsItsOwnRowWhileTesting(t *testing.T) {
	LiveReset()
	defer LiveReset()
	now := time.Now()
	for _, k := range []string{"OR-223", "OR-224", "OR-242"} {
		liveStart(k, now.Add(-10*time.Minute))
		liveActivity(k, "implementer", now)
	}
	LiveBatchStart("orion/batch", "develop", []string{"OR-223", "OR-224", "OR-242"})
	liveBatchPhase(BatchTesting, now.Add(-time.Minute))

	var buf bytes.Buffer
	lines := renderRegion(&buf, liveSnapshot(), now, 200)

	for _, k := range []string{"OR-223", "OR-224", "OR-242"} {
		var found string
		for _, l := range lines {
			p := plain(l)
			// The membership bar names every member on one line; a ticket ROW
			// is the one that also carries its stage.
			if strings.Contains(p, k) && strings.Contains(p, "starting") {
				found = p
			}
		}
		if found == "" {
			t.Errorf("%s has no row of its own:\n%s", k, strings.Join(lines, "\n"))
			continue
		}
		// One ticket per row: a row naming two keys is the paired layout back.
		for _, other := range []string{"OR-223", "OR-224", "OR-242"} {
			if other != k && strings.Contains(found, other) {
				t.Errorf("%s and %s share a row: %q", k, other, found)
			}
		}
	}
}

// Every other phase keeps one row per ticket, because then the tickets really
// are doing different things and the stage and sparkline are worth the space.
func TestOnlyTestingPairsTheRows(t *testing.T) {
	LiveReset()
	defer LiveReset()
	now := time.Now()
	for _, k := range []string{"OR-223", "OR-224"} {
		liveStart(k, now.Add(-10*time.Minute))
	}
	LiveBatchStart("orion/batch", "develop", []string{"OR-223", "OR-224"})

	for _, phase := range []BatchPhase{BatchAssembling, BatchIsolating, BatchDone} {
		liveBatchPhase(phase, now)
		var buf bytes.Buffer
		for _, l := range renderRegion(&buf, liveSnapshot(), now, 200) {
			p := plain(l)
			if !isTicketRow(p) {
				continue
			}
			if strings.Contains(p, "OR-223") && strings.Contains(p, "OR-224") {
				t.Errorf("%s must keep one row per ticket, got a paired line: %q", phase, p)
			}
		}
	}
}

// A terminal too narrow for two cells keeps one row per ticket: a wrapped row
// is two rows, and two wrapped rows are four -- more than the layout it was
// meant to compress.
func TestANarrowTerminalDoesNotPair(t *testing.T) {
	LiveReset()
	defer LiveReset()
	now := time.Now()
	for _, k := range []string{"OR-223", "OR-224"} {
		liveStart(k, now.Add(-10*time.Minute))
	}
	LiveBatchStart("orion/batch", "develop", []string{"OR-223", "OR-224"})
	liveBatchPhase(BatchTesting, now)

	var buf bytes.Buffer
	for _, l := range renderRegion(&buf, liveSnapshot(), now, 60) {
		p := plain(l)
		if isTicketRow(p) && strings.Contains(p, "OR-223") && strings.Contains(p, "OR-224") {
			t.Errorf("60 columns is too narrow to pair, got: %q", p)
		}
	}
}

// The region is erased on the way out and everything in it goes too. For the
// ticket rows that is right -- a finished run has nothing left to say. For the
// BATCH it was wrong: the summary is the cost line and what became of each
// member, drawn four times a second and then wiped, leaving only the scrolling
// log, which is the one thing that cannot answer "what did that batch do".
// The mockup calls it "one durable line" (OR-264).
func TestTheBatchSummarySurvivesTheRegionBeingErased(t *testing.T) {
	t.Setenv("COLUMNS", "100")
	t.Setenv("LINES", "40")
	LiveReset()
	defer LiveReset()
	now := time.Now()

	var b bytes.Buffer
	l := &Live{w: &b, cursor: true}
	LiveBatchStart("orion/batch", "develop", []string{"OR-223", "OR-242"})
	liveBatchPhase(BatchTesting, now)
	LiveBatchMember("OR-223", MemberLanded)
	LiveBatchMember("OR-242", MemberCulprit)
	liveBatchPhase(BatchDone, now)

	b.Reset()
	l.Close()
	got := plain(b.String())

	for _, want := range []string{"OR-223", "landed", "OR-242", "culprit"} {
		if !strings.Contains(got, want) {
			t.Errorf("the summary lost %q on the way out:\n%s", want, got)
		}
	}
}

// A batch still in flight has no outcome worth keeping, and committing one
// mid-run would print a summary the next redraw contradicts.
func TestAnUnfinishedBatchLeavesNoSummary(t *testing.T) {
	t.Setenv("COLUMNS", "100")
	LiveReset()
	defer LiveReset()
	now := time.Now()

	var b bytes.Buffer
	l := &Live{w: &b, cursor: true}
	LiveBatchStart("orion/batch", "develop", []string{"OR-223"})
	liveBatchPhase(BatchTesting, now)

	b.Reset()
	l.Close()
	if strings.Contains(plain(b.String()), "OR-223") {
		t.Errorf("a batch mid-CI left a summary behind:\n%s", plain(b.String()))
	}
}
