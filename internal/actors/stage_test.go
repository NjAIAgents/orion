package actors

import (
	"strings"
	"testing"

	"github.com/orion-sdlc/orion/internal/config"
	"github.com/orion-sdlc/orion/internal/events"
)

// THE REASON THIS LABEL CARRIES AN ID.
//
// A Jira label is persisted data with no render step between it and a human
// eye, so a name written into one is frozen at the moment it was written.
// Names are an operator setting: the QA actor answered to three different
// ones inside a single day of this project's own run logs, and had labels
// carried names the tracker would now hold three of them for one role with
// nothing saying they mean the same thing.
//
// This is the same rule TestNoDefaultNameAppearsOutsideTheRegistry enforces
// for prompts and templates, applied to the one other place a name would be
// frozen rather than rendered.
func TestStageLabelCarriesTheActorIDNeverTheName(t *testing.T) {
	Reset()
	for _, id := range ConfigurableIDs() {
		got := StageLabel(id)
		if want := StageLabelPrefix + id; got != want {
			t.Errorf("StageLabel(%q) = %q, want %q", id, got, want)
		}
	}
	for _, l := range StageLabels() {
		for _, name := range DefaultNames() {
			if strings.Contains(strings.ToLower(l), strings.ToLower(name)) {
				t.Errorf("the stage label %q carries the display name %q. "+
					"A rename would leave it pointing at somebody who no longer exists", l, name)
			}
		}
	}
}

// A rename must not move the label. This is the whole benefit of the id: the
// run log keeps saying the operator's chosen name because it renders, and
// the tracker keeps saying the role because it does not.
func TestRenamingAnActorDoesNotMoveItsStageLabel(t *testing.T) {
	Reset()
	before := StageLabel(events.ActorQA)

	// Any name the shipped roster does not use. Written here rather than
	// taken from the registry precisely because this is the operator's
	// choice, which is the thing that moves.
	renamed := "Winsome"
	if err := Configure(map[string]config.Agent{
		events.ActorQA: {Name: &renamed},
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(Reset)

	if got := Display(events.ActorQA); !strings.Contains(got, renamed) {
		t.Fatalf("the rename did not take: Display = %q", got)
	}
	if got := StageLabel(events.ActorQA); got != before {
		t.Errorf("StageLabel moved with the name: %q -> %q. "+
			"Every ticket already labelled would now be orphaned", before, got)
	}
}

// ci and human never hold a claim -- one is a machine and one is the person
// reading the output -- so neither has a stage to report. handoff() relies
// on the empty answer: it is what stops the boundary into CI writing a label
// to a ticket whose claim was released in the same breath.
func TestNoStageLabelForTheTwoPartiesThatNeverHoldAClaim(t *testing.T) {
	Reset()
	for _, id := range []string{events.ActorCI, events.ActorHuman, ""} {
		if got := StageLabel(id); got != "" {
			t.Errorf("StageLabel(%q) = %q, want empty", id, got)
		}
	}
	for _, l := range StageLabels() {
		if l == StageLabelPrefix+events.ActorCI || l == StageLabelPrefix+events.ActorHuman {
			t.Errorf("StageLabels() offers %q for clearing, which is never set", l)
		}
	}
}

// ADDING AN ACTOR MUST COST NOTHING. OR-135 and OR-168 each add one, and the
// hazard this design exists to avoid is a list somebody has to remember to
// update -- so the labels are derived from the roster rather than written
// down beside it.
func TestEveryConfigurableActorGetsAStageLabelWithoutBeingListed(t *testing.T) {
	Reset()
	have := map[string]bool{}
	for _, l := range StageLabels() {
		have[l] = true
	}
	for _, id := range ConfigurableIDs() {
		if !have[StageLabel(id)] {
			t.Errorf("%s has no stage label; adding an actor should not require "+
				"editing a second list", id)
		}
	}
	if len(have) != len(ConfigurableIDs()) {
		t.Errorf("StageLabels() = %v, which is not one per configurable actor", StageLabels())
	}
}
