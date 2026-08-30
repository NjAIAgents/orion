// Package actors is the one place that knows what an actor is CALLED.
//
// A ticket is worked by several agents on several models -- opus implements,
// haiku routes the question it stops on, sonnet answers it and writes the
// pull request description -- and the output used to present all of them as
// one anonymous voice. "created a pull request description" says nothing
// about who created it or what it cost.
//
// So every rendered line names the actor. Name AND job title, together, on
// every line: the name alone ("Navjyot escalated to Priya") is opaque to
// anyone who has not memorised the cast, and naming on first mention only
// breaks the moment a line is scrolled back to, grepped, or forwarded to
// somebody who did not see the start of the run.
//
// The identifiers themselves are NOT here. They live in internal/events and
// are persisted into the append-only log, so they never change. This package
// is a presentation layer applied at render time, which is what buys three
// things at once: logs written before the names existed render with them,
// renaming an agent later migrates nothing, and user configuration is a
// change to this map rather than a change to the system.
//
// The constraint that follows, and it is the important one: A NAME MUST
// NEVER APPEAR ANYWHERE BUT THIS FILE. Not in a prompt, not in a Slack
// template, not in a test fixture asserting on output. The moment "Ravi" is
// written into a prompt, a team that renames the developer gets an agent
// addressed by a name that appears nowhere in their config and nobody will
// work out why. Prompts refer to roles; only the renderer knows names.
// TestNoDefaultNameAppearsOutsideTheRegistry enforces it.
package actors

import (
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/orion-sdlc/orion/internal/config"
	"github.com/orion-sdlc/orion/internal/events"
)

// Actor is one agent's display form.
type Actor struct {
	ID          string
	Name        string
	Designation string
	// Model is what this actor runs on when the event itself did not record
	// one. A recorded model always wins: this is a default for display, not
	// a claim about what actually ran.
	Model string
	// Effort is the `claude --effort` level this actor runs at. Empty means
	// the CLI's own default, same convention as an empty Model.
	Effort string
}

// Display is what a reader sees: name and job title together, or the job
// title alone when the actor has no name.
//
// Two actors are deliberately nameless. ci is GitHub Actions -- a machine
// that renders no judgement, and naming it implies one -- and human is the
// person reading the output, who has a name already.
func (a Actor) Display() string {
	switch {
	case a.Name != "" && a.Designation != "":
		return a.Name + Separator + a.Designation
	case a.Name != "":
		return a.Name
	case a.Designation != "":
		return a.Designation
	}
	// Never blank. An actor this build has never heard of still has to be
	// attributable, and its identifier is the honest answer.
	return a.ID
}

// Separator joins a name to its job title. One string, so the day it is
// argued about it is argued about once.
const Separator = " · "

// Attribution names the acting role on a surface that has no actor column.
//
// A Slack message is read with no surrounding context, often on a phone, by
// somebody who did not watch the run; a tracker comment is read months
// later. Neither has the columns the terminal uses, so the actor has to
// travel inside the text.
//
// It also settles a collision that the terminal resolves and these surfaces
// do not. Orion posts to the tracker under its operator's account, so
// tickets already carry comments reading as though the operator wrote them,
// and the architect defaults to the operator's own name. Without the role
// spelled out, a reader cannot tell which of the two acted.
func Attribution(id string) string {
	a := Get(id)
	if a.Name != "" {
		return a.Name + Separator + a.Designation + ", an Orion agent"
	}
	if id == events.ActorOrion {
		return "Orion"
	}
	return a.Display()
}

// Comment prefixes a tracker comment with who wrote it.
//
// Every comment Orion files, not only the interesting ones: a prefix that
// appears sometimes tells a reader nothing about the comments without it.
func Comment(id, body string) string {
	return Attribution(id) + ":\n\n" + strings.TrimSpace(body)
}

// defaults are the shipped roster.
//
// Five Indian names, five Western, deliberately mixed. Every initial is
// distinct and no two are similar in length or silhouette, so nothing
// collides in a fast-scrolling log.
//
// Only the developer's model is a real cost decision: it runs 120 to 600
// turns and dominates per-ticket spend. Everyone else reads a diff and
// returns, so their models are chosen for quality rather than economy. The
// architect gets opus because blast radius decides where to spend -- an
// architecture answer is built upon and is expensive to reverse, where a
// scope answer is a judgement a human corrects cheaply.
func defaults() map[string]Actor {
	return map[string]Actor{
		// Orion itself is Go, not a model, and renders as one word: it is
		// the narrator of this output, and giving the narrator a job title
		// on every line spends width on the thing nobody is looking for.
		events.ActorOrion:       {ID: events.ActorOrion, Designation: "orion"},
		events.ActorRouter:      {ID: events.ActorRouter, Name: "Sam", Designation: "dispatcher", Model: "haiku"},
		events.ActorImplementer: {ID: events.ActorImplementer, Name: "Ravi", Designation: "backend developer", Model: "opus"},
		events.ActorFrontend:    {ID: events.ActorFrontend, Name: "Kai", Designation: "frontend developer", Model: "opus"},
		// Docs writes ADRs and other prose from a codebase that already exists.
		// Sonnet, the same argument as QA's: the source of truth is already
		// committed, so the work is careful reading and structured writing
		// rather than design, and opus would pay implementer prices for it.
		events.ActorDocs:      {ID: events.ActorDocs, Name: "Iris", Designation: "docs engineer", Model: "sonnet"},
		events.ActorArchitect: {ID: events.ActorArchitect, Name: "Navjyot", Designation: "architect", Model: "opus"},
		events.ActorPM:        {ID: events.ActorPM, Name: "Priya", Designation: "product manager", Model: "sonnet"},
		events.ActorDevOps:    {ID: events.ActorDevOps, Name: "Arjun", Designation: "devops engineer", Model: "sonnet"},
		events.ActorDescriber: {ID: events.ActorDescriber, Name: "Dana", Designation: "PR writer", Model: "sonnet"},
		// QA derives its cases from the ticket's acceptance criteria and
		// writes tests against them. Sonnet: the specification is written
		// down, so the work is careful reading rather than design, and opus
		// would be paying implementer prices to author test files.
		events.ActorQA: {ID: events.ActorQA, Name: "Anita", Designation: "QA engineer", Model: "sonnet"},
		// Log triage reads a failing CI log and reports what broke, so the fix
		// run that follows carries that report instead of the raw log riding
		// along on every turn (OR-143). Haiku: the reading is mechanical --
		// find the failure in a wall of output -- not the judgement the fix
		// itself takes, and it runs on every red build, so it is the one
		// actor for which the model is a real cost decision besides the
		// developer.
		events.ActorLogTriage: {ID: events.ActorLogTriage, Name: "Milo", Designation: "log triage", Model: "haiku"},
		// Explore answers one narrow question about a repository the asking
		// run has not read -- where a thing is defined, whether a pattern
		// already exists, what a config actually contains -- so the greps and
		// files it takes to find out stay in ITS context instead of the
		// asker's for the rest of the run (OR-183). Haiku, for the same
		// reason log triage is: locating a definition is mechanical reading
		// rather than judgement, and this is the question that recurs most
		// often within a single run, so its model is a real cost decision.
		events.ActorExplore: {ID: events.ActorExplore, Name: "Zoya", Designation: "code explorer", Model: "haiku"},
		// Case derivation reads the ticket's acceptance criteria and the diff
		// and returns the list of cases QA has to cover, so that reading stops
		// riding along on every turn of the test authoring that follows
		// (OR-182). Haiku, the same argument as the two above: turning a
		// written specification into a list of what to check is reading with a
		// short answer, not the judgement of writing the tests, and QA runs on
		// every ticket, so its model is a real cost decision.
		events.ActorCaseDerive: {ID: events.ActorCaseDerive, Name: "Tara", Designation: "test case analyst", Model: "haiku"},
		// AIOps reads a FINISHED run's event log and says what is worth
		// filing. Almost all of that reading is rules -- a breaker trip and an
		// exhausted fix loop are typed events, and a rule cannot hallucinate
		// one -- so this actor is reserved for the one part rules cannot do:
		// judging whether a pattern nothing recognises is worth a person's
		// attention (OR-168).
		//
		// Sonnet, unlike the three cheap readers above it. Their job is to
		// locate something already written down; this one's is to decide
		// whether to propose creating something, and a cheap model that
		// answers yes too readily makes a backlog nobody can scan -- which is
		// the exact failure this pass exists to avoid. Opus is not warranted:
		// the input is a few dozen structured lines, not a codebase.
		events.ActorAIOps: {ID: events.ActorAIOps, Name: "Baba", Designation: "AIOps engineer", Model: "sonnet"},
		events.ActorCI:    {ID: events.ActorCI, Designation: "ci"},
		events.ActorHuman: {ID: events.ActorHuman, Designation: "you"},
	}
}

// fixed are the actors configuration may not touch. One is a machine and one
// is the reader; naming either creates the confusion this design avoids.
var fixed = map[string]bool{events.ActorCI: true, events.ActorHuman: true}

var (
	mu      sync.RWMutex
	current = defaults()
)

// Get resolves an identifier. An unknown one renders as itself rather than
// as a blank column, so an actor added by a newer build -- or a log from
// one -- is still attributable.
func Get(id string) Actor {
	mu.RLock()
	defer mu.RUnlock()
	if a, ok := current[id]; ok {
		return a
	}
	return Actor{ID: id}
}

// Display is the shorthand every renderer uses.
func Display(id string) string { return Get(id).Display() }

// Model is the model to show for an actor when the event recorded none.
func Model(id string) string { return Get(id).Model }

// Effort is the reasoning-effort level to run an actor at, or "" for the
// `claude` CLI's own default.
func Effort(id string) string { return Get(id).Effort }

// Configure overlays a project's `agents` block on the defaults.
//
// Field by field, so overriding one name does not silently reset a model.
// Unknown identifiers are ignored rather than refused: an actor that a later
// release defines (the security reviewers, for one) must be configurable
// ahead of arriving, and a config that fails the whole run over a key this
// build has not heard of would make upgrading worse than not upgrading.
func Configure(agents map[string]config.Agent) error {
	next := defaults()
	for id, over := range agents {
		base, known := next[id]
		if !known {
			continue
		}
		if fixed[id] {
			return fmt.Errorf("agents.%s is not configurable: %s is %s, and naming it "+
				"would imply a judgement it does not make", id, id, notConfigurableWhy(id))
		}
		merged, _ := overlay(base, over)
		next[id] = merged
	}
	if err := noDuplicateNames(next); err != nil {
		return err
	}
	mu.Lock()
	defer mu.Unlock()
	current = next
	return nil
}

// overlay applies one override onto a shipped default, field by field, and
// reports which fields the override decided.
//
// Field by field, so overriding one name does not silently reset a model.
// A Name of "" is a real decision and not an absence -- it clears the
// persona, which is why config.Agent.Name is a pointer -- while an empty
// designation, model or effort means the override said nothing about that
// field and the shipped value stands.
//
// Configure and Roster share this deliberately. A listing that resolved
// values by its own rules would eventually disagree with what actually
// runs, and a roster that disagrees with the run is worse than no roster:
// it is read precisely when somebody is trying to find out what a run will
// cost before starting it.
func overlay(base Actor, over config.Agent) (Actor, Overridden) {
	var set Overridden
	if over.Name != nil {
		base.Name = strings.TrimSpace(*over.Name)
		set.Name = true
	}
	if s := strings.TrimSpace(over.Designation); s != "" {
		base.Designation = s
		set.Designation = true
	}
	if s := strings.TrimSpace(over.Model); s != "" {
		base.Model = s
		set.Model = true
	}
	if s := strings.TrimSpace(over.Effort); s != "" {
		base.Effort = s
		set.Effort = true
	}
	return base, set
}

// Overridden records which of an actor's fields the roster file decided,
// rather than the build.
//
// Per field rather than per actor, because that is the granularity the
// overlay works at: an operator who set only an effort still gets the
// shipped model, and a listing that said "overridden" for the whole row
// would misreport three of four columns.
type Overridden struct {
	Name        bool
	Designation bool
	Model       bool
	Effort      bool
}

// Fields names the overridden fields in column order, or nothing at all
// when the actor is entirely shipped defaults.
func (o Overridden) Fields() []string {
	var out []string
	for _, f := range []struct {
		set  bool
		name string
	}{
		{o.Name, "name"},
		{o.Designation, "designation"},
		{o.Model, "model"},
		{o.Effort, "effort"},
	} {
		if f.set {
			out = append(out, f.name)
		}
	}
	return out
}

// RosterEntry is one actor as a run will actually see it, plus where each
// of its fields came from.
type RosterEntry struct {
	Actor
	Overridden Overridden
}

// Roster resolves the whole configurable roster against an overrides map,
// sorted by identifier.
//
// This is what makes the roster answerable without reading two files. The
// override file holds only overrides -- most actors are absent from it
// entirely -- so it cannot say what the explorer or the router runs on.
// Only the shipped defaults with the overrides applied can, and only this
// package holds both.
//
// The set is exactly ConfigurableIDs, which is what `orion config agents`
// walks, so the read-only listing and the wizard can never show different
// rosters. ci and human are in neither: one is a machine and one is the
// person reading the output, and neither runs on a model or costs
// anything.
//
// Takes the overrides rather than reading current, so a caller gets
// provenance whether or not Configure has run -- and so the listing is a
// pure function of the file, testable without global state.
func Roster(agents map[string]config.Agent) []RosterEntry {
	base := defaults()
	ids := ConfigurableIDs()
	out := make([]RosterEntry, 0, len(ids))
	for _, id := range ids {
		a, set := overlay(base[id], agents[id])
		out = append(out, RosterEntry{Actor: a, Overridden: set})
	}
	return out
}

func notConfigurableWhy(id string) string {
	if id == events.ActorCI {
		return "a machine"
	}
	return "the person reading this"
}

// noDuplicateNames refuses a roster in which two actors answer to one name.
//
// Refused rather than warned about: distinguishability is the entire reason
// names exist here, and a duplicate destroys it silently -- every line then
// says who acted and no line says which one.
func noDuplicateNames(in map[string]Actor) error {
	seen := map[string][]string{}
	for id, a := range in {
		if a.Name == "" {
			continue // no name is not a collision; it means "job title alone"
		}
		k := strings.ToLower(a.Name)
		seen[k] = append(seen[k], id)
	}
	for _, ids := range seen {
		if len(ids) < 2 {
			continue
		}
		sort.Strings(ids)
		// Both identifiers, because knowing that "two agents share a name"
		// without knowing WHICH two leaves the reader to diff the roster.
		return fmt.Errorf("two agents share the name %q: %s. "+
			"Names exist to tell them apart, so give one of them a different one",
			in[ids[0]].Name, strings.Join(ids, " and "))
	}
	return nil
}

// Reset restores the shipped roster. For tests, and for a caller moving
// between projects with different configuration.
func Reset() {
	mu.Lock()
	defer mu.Unlock()
	current = defaults()
}

// ConfigurableIDs lists every actor `orion config agents` may edit: the
// whole roster except ci and human, which are not configurable at all (see
// fixed and Configure's refusal). Sorted, so the wizard walks the roster in
// the same order every run.
func ConfigurableIDs() []string {
	ids := make([]string, 0, len(defaults()))
	for id := range defaults() {
		if !fixed[id] {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	return ids
}

// DefaultNames lists every name this build ships with, for the test that
// makes sure none of them has leaked into a prompt or a template.
func DefaultNames() []string {
	var out []string
	for _, a := range defaults() {
		if a.Name != "" {
			out = append(out, a.Name)
		}
	}
	sort.Strings(out)
	return out
}
