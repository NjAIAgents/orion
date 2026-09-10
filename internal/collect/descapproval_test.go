package collect

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/orion-sdlc/orion/internal/tracker"
)

// *tracker.Jira must satisfy DescTrackerAPI, or this whole gate is unusable
// outside a test.
var _ DescTrackerAPI = (*tracker.Jira)(nil)

// fakeDescAPI is the smallest double this gate needs -- no HTTP, no ADF, just
// the four calls DescTrackerAPI names.
type fakeDescAPI struct {
	issue        *tracker.Issue
	comments     []string
	setLabels    [][2][]string // {add, remove} per call
	description  string
	applyErr     error
	getErr       error
	setLabelsErr error
}

func (f *fakeDescAPI) GetIssue(key string) (*tracker.Issue, error) {
	if f.getErr != nil {
		return nil, f.getErr
	}
	cp := *f.issue
	return &cp, nil
}
func (f *fakeDescAPI) SetLabels(key string, add, remove []string) error {
	f.setLabels = append(f.setLabels, [2][]string{add, remove})
	if f.setLabelsErr != nil {
		return f.setLabelsErr
	}
	labels := map[string]bool{}
	for _, l := range f.issue.Labels {
		labels[l] = true
	}
	for _, l := range remove {
		delete(labels, l)
	}
	for _, l := range add {
		labels[l] = true
	}
	f.issue.Labels = nil
	for l := range labels {
		f.issue.Labels = append(f.issue.Labels, l)
	}
	return nil
}
func (f *fakeDescAPI) Comment(key, text string) error {
	f.comments = append(f.comments, text)
	return nil
}
func (f *fakeDescAPI) SetDescription(key, text string) (string, error) {
	if f.applyErr != nil {
		return "", f.applyErr
	}
	was := f.description
	f.description = text
	return was, nil
}

func newFakeDescAPI(desc string, labels ...string) *fakeDescAPI {
	return &fakeDescAPI{
		issue:       &tracker.Issue{Key: "OR-77", Description: desc, Labels: labels},
		description: desc,
	}
}

// The request NEVER writes the description. This is the property the whole
// gate rests on: composing and applying must stay two functions, or a future
// refactor collapses them into "ask and do it anyway".
func TestRequestNeverAppliesTheChange(t *testing.T) {
	f := newFakeDescAPI("old text")
	var buf bytes.Buffer
	if err := RequestDescriptionChange(f, "OR-77", "Mahesh", "new text", &buf); err != nil {
		t.Fatal(err)
	}
	if f.description != "old text" {
		t.Fatalf("the request must not write the description; got %q", f.description)
	}
}

// The comment must carry both texts in full, not a diff -- the reviewer is
// deciding whether to trust the NEW text, not how it was derived.
func TestRequestPostsBothTextsInFull(t *testing.T) {
	f := newFakeDescAPI("old text")
	var buf bytes.Buffer
	if err := RequestDescriptionChange(f, "OR-77", "Mahesh", "new text", &buf); err != nil {
		t.Fatal(err)
	}
	if len(f.comments) != 1 {
		t.Fatalf("expected one comment, got %d", len(f.comments))
	}
	if !strings.Contains(f.comments[0], "old text") || !strings.Contains(f.comments[0], "new text") {
		t.Errorf("the comment must carry both texts: %q", f.comments[0])
	}
}

// The pending label is what makes this resumable across ticks: a watcher
// that restarts mid-approval must still find the request.
func TestRequestMarksTheTicketPending(t *testing.T) {
	f := newFakeDescAPI("old text")
	var buf bytes.Buffer
	if err := RequestDescriptionChange(f, "OR-77", "Mahesh", "new text", &buf); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, l := range f.issue.Labels {
		if l == tracker.LabelDescPending {
			found = true
		}
	}
	if !found {
		t.Error("the ticket must wear the pending label after a request")
	}
}

// THE HEART OF THE GATE: approved means applied.
func TestApprovedMeansApplied(t *testing.T) {
	f := newFakeDescAPI("old text", tracker.LabelDescPending, tracker.LabelDescApproved)
	var buf bytes.Buffer
	p := PendingDescriptionChange{Key: "OR-77", Proposed: "new text"}
	if err := ResolveDescriptionChange(f, p, &buf); err != nil {
		t.Fatal(err)
	}
	if f.description != "new text" {
		t.Errorf("an approved change must be applied, got %q", f.description)
	}
	for _, l := range f.issue.Labels {
		if l == tracker.LabelDescPending || l == tracker.LabelDescApproved {
			t.Errorf("the decision labels must be cleared after resolving, still has %q", l)
		}
	}
}

// Rejected means nothing is written, ever.
func TestRejectedMeansNothingIsWritten(t *testing.T) {
	f := newFakeDescAPI("old text", tracker.LabelDescPending, tracker.LabelDescRejected)
	var buf bytes.Buffer
	p := PendingDescriptionChange{Key: "OR-77", Proposed: "new text"}
	if err := ResolveDescriptionChange(f, p, &buf); err != nil {
		t.Fatal(err)
	}
	if f.description != "old text" {
		t.Errorf("a rejected change must not be applied, got %q", f.description)
	}
	for _, l := range f.issue.Labels {
		if l == tracker.LabelDescPending {
			t.Error("the pending label must be cleared even on rejection, or the " +
				"ticket can never be proposed again")
		}
	}
}

// Neither label yet: still waiting. Not an error, and nothing is written --
// this is the ordinary state of a request nobody has looked at.
func TestNeitherLabelMeansStillWaiting(t *testing.T) {
	f := newFakeDescAPI("old text", tracker.LabelDescPending)
	var buf bytes.Buffer
	p := PendingDescriptionChange{Key: "OR-77", Proposed: "new text"}
	if err := ResolveDescriptionChange(f, p, &buf); err != nil {
		t.Fatal(err)
	}
	if f.description != "old text" {
		t.Error("with no decision yet, nothing may be written")
	}
	if len(f.setLabels) != 0 {
		t.Error("with no decision yet, no label write should happen at all")
	}
}

// A read failure at resolve time must not be silently treated as "not yet
// decided" -- that would make a transient tracker outage indistinguishable
// from an unreviewed request.
func TestAReadFailureAtResolveIsAnError(t *testing.T) {
	f := newFakeDescAPI("old text", tracker.LabelDescPending)
	f.getErr = errors.New("tracker unavailable")
	var buf bytes.Buffer
	p := PendingDescriptionChange{Key: "OR-77", Proposed: "new text"}
	if err := ResolveDescriptionChange(f, p, &buf); err == nil {
		t.Fatal("expected an error when the ticket could not be read")
	}
}

// Both labels present is a state a human should not be able to reach through
// the normal Jira UI (they are radio-shaped in intent), but the code must
// not double-apply if it happens. Approval wins, since it is checked first --
// documented here so the precedence is a decision, not an accident.
func TestBothLabelsPresentApprovalWins(t *testing.T) {
	f := newFakeDescAPI("old text",
		tracker.LabelDescPending, tracker.LabelDescApproved, tracker.LabelDescRejected)
	var buf bytes.Buffer
	p := PendingDescriptionChange{Key: "OR-77", Proposed: "new text"}
	if err := ResolveDescriptionChange(f, p, &buf); err != nil {
		t.Fatal(err)
	}
	if f.description != "new text" {
		t.Error("with both labels present, approval must win by the stated precedence")
	}
}
