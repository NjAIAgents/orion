package main

// The confirmation gate itself (OR-154): where a record is filed, who is
// allowed to move it, and that it stays unreadable to later stages until it
// physically exists in decide.ConfirmedDir. plandba_test.go pins the ORDER
// -- recommend, confirm, design -- this file pins the mechanics underneath
// that order that a later change could quietly break without breaking the
// order itself.

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/orion-sdlc/orion/internal/decide"
	"github.com/orion-sdlc/orion/internal/slack"
	"github.com/orion-sdlc/orion/internal/supervisor"
	"github.com/orion-sdlc/orion/internal/tracker"
)

// Until the database choice is written to decide.ConfirmedDir, it does not
// exist there at all -- not under a different name, not with different
// content. A later stage that reads the confirmed path directly (as the
// schema prompt does) must get exactly "not found", never a stale or
// partial file.
func TestConfirmedDatabaseRecordUnreadableUntilWrittenToConfirmedDir(t *testing.T) {
	h := newPlanHarness(t, choiceReport, schemaReport)
	if err := h.run(t); err != nil {
		t.Fatalf("planDBA: %v", err)
	}

	if _, err := os.ReadFile(h.confirmed(planDBATopicDatabase)); !os.IsNotExist(err) {
		t.Fatalf("the database record is readable from %s before anyone confirmed it: %v",
			decide.ConfirmedDir, err)
	}

	h.slack.reactions = []slack.Reaction{
		{Name: "white_check_mark", Users: []string{"U-APPROVER"}},
	}
	if err := h.run(t); err != nil {
		t.Fatalf("planDBA, after confirmation: %v", err)
	}
	body, err := os.ReadFile(h.confirmed(planDBATopicDatabase))
	if err != nil {
		t.Fatalf("the database record is still not readable from %s once confirmed: %v",
			decide.ConfirmedDir, err)
	}
	if !strings.Contains(string(body), "PostgreSQL 16") {
		t.Errorf("the confirmed record does not carry what was recommended:\n%s", body)
	}
}

// The schema record makes the same trip through the same gate, one topic
// later. Nothing here should promote it a step early just because the
// database ahead of it already went through.
func TestConfirmedSchemaRecordUnreadableUntilWrittenToConfirmedDir(t *testing.T) {
	h := newPlanHarness(t, choiceReport, schemaReport)
	if err := h.run(t); err != nil {
		t.Fatalf("planDBA: %v", err)
	}
	h.slack.reactions = []slack.Reaction{
		{Name: "white_check_mark", Users: []string{"U-APPROVER"}},
	}
	if err := h.run(t); err != nil {
		t.Fatalf("planDBA, confirming the database: %v", err)
	}

	if _, err := os.ReadFile(h.confirmed(planDBATopicSchema)); !os.IsNotExist(err) {
		t.Fatalf("the schema record is readable from %s before anyone confirmed it: %v",
			decide.ConfirmedDir, err)
	}

	if err := h.run(t); err != nil {
		t.Fatalf("planDBA, confirming the schema: %v", err)
	}
	body, err := os.ReadFile(h.confirmed(planDBATopicSchema))
	if err != nil {
		t.Fatalf("the schema record is still not readable from %s once confirmed: %v",
			decide.ConfirmedDir, err)
	}
	if !strings.Contains(string(body), "ledger_entry") {
		t.Errorf("the confirmed schema record does not carry what was recommended:\n%s", body)
	}
}

// A workspace bound to a tracker project files its records under that
// project's KEY, not the workspace id -- the id is an internal handle and
// the key is what a person confirming in Slack, or reading the directory
// later, recognizes.
func TestRecordsAreFiledUnderTheTrackerKeyWhenBound(t *testing.T) {
	h := newPlanHarness(t, choiceReport)
	raw, err := json.Marshal(tracker.Binding{Provider: "jira", Key: "ORPAY", ProjectID: "10001"})
	if err != nil {
		t.Fatalf("marshalling the tracker binding: %v", err)
	}
	h.ws.Task.Tracker = raw

	if err := h.run(t); err != nil {
		t.Fatalf("planDBA: %v", err)
	}
	if _, err := os.Stat(pendingRecord(h.ws.RepoDir(), "ORPAY-"+planDBATopicDatabase)); err != nil {
		t.Errorf("the record was not filed under the tracker key ORPAY: %v", err)
	}
	if _, err := os.Stat(h.pending(planDBATopicDatabase)); !os.IsNotExist(err) {
		t.Error("the record was filed under the workspace id even though a tracker binding exists")
	}
}

// Unbound -- no tracker project -- falls back to the workspace id, which is
// newPlanHarness's default ("orion-pay"): every other test in this package
// already exercises that path, so this test only has to show the switch
// happens on the presence of a binding.
func TestRecordsAreFiledUnderTheWorkspaceIDWhenUnbound(t *testing.T) {
	h := newPlanHarness(t, choiceReport)

	if err := h.run(t); err != nil {
		t.Fatalf("planDBA: %v", err)
	}
	if _, err := os.Stat(h.pending(planDBATopicDatabase)); err != nil {
		t.Errorf("an unbound workspace did not file its record under the workspace id: %v", err)
	}
}

// The question goes to the workspace's own task channel -- the room the
// people who asked for this work are already in -- and not some other
// channel the record might otherwise default to.
func TestTheSlackQuestionIsPostedToTheWorkspacesTaskChannel(t *testing.T) {
	h := newPlanHarness(t, choiceReport)

	if err := h.run(t); err != nil {
		t.Fatalf("planDBA: %v", err)
	}
	if len(h.slack.posted) != 1 {
		t.Fatalf("expected one Slack question, got %d", len(h.slack.posted))
	}
	// The record's own "- Slack: channel/ts" line is what a later confirm
	// reads the channel back out of, so it is also the honest place to
	// check what channel the question actually went to.
	body, err := os.ReadFile(h.pending(planDBATopicDatabase))
	if err != nil {
		t.Fatalf("reading the pending record: %v", err)
	}
	if !strings.Contains(string(body), "- Slack: "+h.ws.Task.Slack.ID+"/") {
		t.Errorf("the record does not show the question went to the workspace's task "+
			"channel %q:\n%s", h.ws.Task.Slack.ID, body)
	}
}

// When the workspace carries no task channel, there is nowhere to ask: the
// record is still written -- unconfirmed is the safe state to be stuck in
// -- but nothing is posted to Slack, because posting to an empty channel is
// not "ask nobody", it is a call that would fail or land somewhere nobody
// is watching.
func TestNoTaskChannelMeansNoSlackQuestionIsPosted(t *testing.T) {
	h := newPlanHarness(t, choiceReport)
	h.ws.Task.Slack = nil

	if err := h.run(t); err != nil {
		t.Fatalf("planDBA: %v", err)
	}
	if len(h.slack.posted) != 0 {
		t.Errorf("a question was posted with no task channel to post it to: %v", h.slack.posted)
	}
	if _, err := os.Stat(h.pending(planDBATopicDatabase)); err != nil {
		t.Errorf("the recommendation was not recorded even though Slack could not be asked: %v", err)
	}
}

// Confirmation reads slack.merge_approvers as it stood when the question
// was asked (writeApprovers puts "U-APPROVER" there). A reaction from
// anyone NOT on that allowlist is not a confirmation -- the record stays
// exactly where it is, in PendingDir, and the schema is not designed
// against it.
func TestConfirmationOnlyAcceptsReactionsFromTheMergeApproversAllowlist(t *testing.T) {
	h := newPlanHarness(t, choiceReport, schemaReport)
	if err := h.run(t); err != nil {
		t.Fatalf("planDBA: %v", err)
	}

	h.slack.reactions = []slack.Reaction{
		{Name: "white_check_mark", Users: []string{"U-RANDOM"}},
	}
	if err := h.run(t); err != nil {
		t.Fatalf("planDBA, with an unapproved reaction: %v", err)
	}
	if _, err := os.Stat(h.confirmed(planDBATopicDatabase)); !os.IsNotExist(err) {
		t.Error("a reaction from someone off slack.merge_approvers confirmed the record anyway")
	}
	if len(h.sent) != 1 {
		t.Fatalf("the schema was designed even though the database was never confirmed "+
			"(%d dispatches)", len(h.sent))
	}

	h.slack.reactions = append(h.slack.reactions,
		slack.Reaction{Name: "white_check_mark", Users: []string{"U-APPROVER"}})
	if err := h.run(t); err != nil {
		t.Fatalf("planDBA, with an approved reaction added: %v", err)
	}
	if _, err := os.Stat(h.confirmed(planDBATopicDatabase)); err != nil {
		t.Errorf("a reaction from someone on slack.merge_approvers did not confirm the record: %v", err)
	}
}

// DBA BECAUSE before DBA RECOMMENDS is not a decoration difference: it means
// the reasoning was written for a choice that has not been named yet, so
// what was recommended cannot be told from why. That is an error, not a
// report to record -- the same way a choice with no reasoning at all is.
func TestBecauseBeforeRecommendsIsAnError(t *testing.T) {
	h := newPlanHarness(t,
		"DBA BECAUSE the ledger is relational and needs transactions.\n"+
			"DBA RECOMMENDS PostgreSQL 16")

	err := h.run(t)
	if err == nil {
		t.Fatal("BECAUSE before RECOMMENDS was accepted as an ordered proposal")
	}
	if !strings.Contains(err.Error(), supervisor.DBABecause) ||
		!strings.Contains(err.Error(), supervisor.DBARecommends) {
		t.Errorf("the refusal does not name both markers: %v", err)
	}
	if _, statErr := os.Stat(h.pending(planDBATopicDatabase)); !os.IsNotExist(statErr) {
		t.Error("a proposal with the markers out of order was recorded anyway")
	}
	if len(h.slack.posted) != 0 {
		t.Error("Slack was asked to confirm a proposal that was refused for its marker order")
	}
}

// A rejection is final and it is not a state a record can hold -- Confirm
// leaves the pending file exactly where it was. Whether the rejection came
// as a :x: reaction or as a refusal typed in the thread, the schema must not
// be designed and a re-run must still find the choice pending, not gone and
// not confirmed.
func TestARejectedReactionDoesNotPromoteAndAReRunStillSeesItPending(t *testing.T) {
	h := newPlanHarness(t, choiceReport, schemaReport)
	if err := h.run(t); err != nil {
		t.Fatalf("planDBA: %v", err)
	}

	h.slack.reactions = []slack.Reaction{
		{Name: "x", Users: []string{"U-APPROVER"}},
	}
	if err := h.run(t); err != nil {
		t.Fatalf("planDBA, with a rejected reaction: %v", err)
	}
	if _, err := os.Stat(h.confirmed(planDBATopicDatabase)); !os.IsNotExist(err) {
		t.Error("a rejected reaction confirmed the record anyway")
	}
	if _, err := os.Stat(h.pending(planDBATopicDatabase)); err != nil {
		t.Errorf("the rejected record is no longer where a pending choice lives: %v", err)
	}
	if len(h.sent) != 1 {
		t.Fatalf("the schema was designed even though the database was rejected, "+
			"not confirmed (%d dispatches)", len(h.sent))
	}

	// A second look at the same rejection: still pending, still not designed.
	if err := h.run(t); err != nil {
		t.Fatalf("planDBA, re-run after the rejection: %v", err)
	}
	if _, err := os.Stat(h.confirmed(planDBATopicDatabase)); !os.IsNotExist(err) {
		t.Error("a re-run promoted a record that was rejected")
	}
	if _, err := os.Stat(h.pending(planDBATopicDatabase)); err != nil {
		t.Errorf("the re-run lost the pending record: %v", err)
	}
	if len(h.sent) != 1 {
		t.Fatalf("a re-run of a rejected choice dispatched the architect again "+
			"(%d dispatches)", len(h.sent))
	}
}
