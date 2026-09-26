package web

// OR-273: the board's columns are the queue's states, and the web package is
// not allowed a second opinion about what those are.

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/orion-sdlc/orion/internal/tracker"
)

// The column set IS the state machine -- same states, same order, same
// labels. Written as a comparison against tracker rather than against a list
// spelled out here, so the day a sixth state is added this test expects six
// columns without being edited: a web package that kept its own five would
// fail here, which is the regression this ticket exists to prevent.
func TestColumnsAreTheQueueStates(t *testing.T) {
	const queueLabel = "TEST-QUEUE-LABEL"

	want := tracker.QueueStates(queueLabel)
	if len(want) < 2 {
		t.Fatalf("the state machine has %d state(s); this test would pass on an empty board", len(want))
	}

	got := Columns(queueLabel)
	if len(got) != len(want) {
		t.Fatalf("the board draws %d column(s) for a machine with %d state(s):\ngot  %+v\nwant %+v",
			len(got), len(want), got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("column %d is %+v, want %+v -- the board's order is the pipeline order tracker states",
				i, got[i], want[i])
		}
	}
}

// The queue label is per project, so it cannot be frozen into a column: two
// projects on one board would otherwise both draw the first one's label.
func TestTheQueuedColumnCarriesTheProjectsOwnLabel(t *testing.T) {
	const queueLabel = "SOME-OTHER-LABEL"
	for _, c := range Columns(queueLabel) {
		if c.Label == queueLabel {
			return
		}
	}
	t.Errorf("no column carries the project's queue label %q: %+v", queueLabel, Columns(queueLabel))
}

// A caller that never resolved config gets the shared default label rather
// than a column with no label at all -- the same fallback the watcher and the
// scheduler make.
func TestAnUnresolvedQueueLabelFallsBackToTheDefault(t *testing.T) {
	for _, c := range Columns("") {
		if c.Label == "" {
			t.Fatalf("column %q has no label: %+v", c.Name, Columns(""))
		}
	}
	if got, want := Columns(""), Columns(tracker.QueueLabelDefault); len(got) != len(want) || got[0] != want[0] {
		t.Errorf("an empty queue label draws %+v, want the default label's %+v", got, want)
	}
}

// Done is not a column. The state machine records completion by the ABSENCE
// of every managed label rather than by adding a fifth one, so a finished
// ticket -- once Orion has cleared its own labels off it -- carries none of
// the labels any column matches, and the board has nowhere to draw it. A
// sixth column named "done" would be a state the tracker never declares.
func TestDoneIsNotABoardColumn(t *testing.T) {
	for _, c := range Columns(tracker.QueueLabelDefault) {
		if strings.EqualFold(c.Name, "done") || strings.EqualFold(c.Label, "done") {
			t.Errorf("the board has a done column %+v; done is the absence of every label, not one of them", c)
		}
	}

	done := tracker.Issue{
		Key:            "OR-1",
		StatusCategory: tracker.StatusCategoryDone,
		// No labels: exactly what a ticket looks like once Orion's own
		// closing path (work.release / alreadyResolved) has cleared its
		// managed labels off a finished ticket.
	}
	if !done.Resolved() {
		t.Fatal("test setup: the fixture issue must read as resolved")
	}
	for _, s := range tracker.QueueStates(tracker.QueueLabelDefault) {
		for _, l := range done.Labels {
			if strings.EqualFold(l, s.Label) {
				t.Errorf("a done ticket carries %q, the label for column %q", l, s.Name)
			}
		}
	}
}

// Nothing in the web package names a label of the state machine.
//
// The same enforcement TestNoRoleModelIsDeclaredInTheWebPackage makes for the
// roster: the comparison test above proves the two agree TODAY, and this one
// proves the agreement is derivation rather than coincidence, because a copy
// good enough to pass that test would be visible here.
//
// The LABELS, not the state names. "working" and "failed" are also two of
// internal/ui's five outcome verbs, which a card legitimately carries and
// model.go legitimately writes down; a scan for those words would report that
// vocabulary as a copy of this one. A label is Orion's own and has no second
// meaning -- with one exception, carved out below rather than weakening the
// \b-word-boundary match every other label relies on.
//
// "queued"'s label is tracker.QueueLabelDefault, and its value is "ORION":
// the project's own name, in a config field that happens to default to it.
// Every other label here (orion-working, orion-failed, ...) is Orion's own
// vocabulary and nothing else; "ORION" bare is also just the product's name,
// legitimately spelled in a doc comment, a page title, a banner string, or a
// User-Agent -- none of which is the copy this test exists to catch. Skipped
// entirely rather than scanned for, so a future all-caps mention of the
// product in this package's prose does not fail a test about queue labels.
func TestNoQueueStateIsDeclaredInTheWebPackage(t *testing.T) {
	var pats []*regexp.Regexp
	var names []string
	for _, s := range tracker.QueueStates(tracker.QueueLabelDefault) {
		if s.Label == tracker.QueueLabelDefault {
			continue
		}
		pats = append(pats, regexp.MustCompile(`\b`+regexp.QuoteMeta(s.Label)+`\b`))
		names = append(names, s.Label)
	}
	if len(pats) == 0 {
		t.Fatal("no state to look for; this test would pass on an empty state machine")
	}

	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		for i, p := range pats {
			if p.Match(b) {
				t.Errorf("%s names %q. A queue state belongs to internal/tracker alone; "+
					"declaring one here declares it twice, and the copy is what the board "+
					"will keep drawing after the state machine changes.", f, names[i])
			}
		}
	}
}
