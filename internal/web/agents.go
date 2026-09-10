package web

// The agent roster behind `orion web` (OR-80): what each role runs on, at
// what effort, and which of those values the operator decided rather than the
// build. docs/design/web/04-agents.html is the consumer.
//
// THIS FILE DECLARES NOTHING. Not a model, not an effort, not a name. Every
// value is resolved by internal/actors from the shipped defaults with the
// global agents.json applied on top -- the same call `orion config agents
// --list` makes, so the page and the terminal listing cannot disagree about
// what a run is going to cost.
//
// A page holding its own copy of the roster would read correctly on the day
// it was written and then go quietly stale, and it would lose the one thing
// this page exists to show. agents.json holds only OVERRIDES, so most actors
// are absent from it entirely: a hardcoded row cannot tell a shipped default
// from an operator's decision, and provenance per field is exactly what the
// mockup marks. TestNoRoleModelIsDeclaredInTheWebPackage enforces it.

import (
	"github.com/orion-sdlc/orion/internal/actors"
	"github.com/orion-sdlc/orion/internal/config"
)

// Roster is the agents page's model: one row per configurable actor, in
// identifier order, each carrying the effective identifier, name,
// designation, model and effort plus which of those fields the override file
// decided.
//
// It returns actors.RosterEntry rather than a web-shaped copy of it. A type
// of its own here would have to be filled field by field from that one, which
// is the duplication with an extra step: the day an actor gains a field, the
// page silently keeps showing the five it knew about.
//
// home is the Orion home directory holding agents.json. A missing file is not
// an error -- it means nothing is overridden and every row is a shipped
// default, which is the state a machine that has never run the wizard is in.
func Roster(home string) ([]actors.RosterEntry, error) {
	over, err := config.LoadAgents(home)
	if err != nil {
		return nil, err
	}
	return actors.Roster(over), nil
}
