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
		events.ActorArchitect:   {ID: events.ActorArchitect, Name: "Navjyot", Designation: "architect", Model: "opus"},
		events.ActorPM:          {ID: events.ActorPM, Name: "Priya", Designation: "product manager", Model: "sonnet"},
		events.ActorDevOps:      {ID: events.ActorDevOps, Name: "Arjun", Designation: "devops engineer", Model: "sonnet"},
		events.ActorDescriber:   {ID: events.ActorDescriber, Name: "Dana", Designation: "PR writer", Model: "sonnet"},
		// QA derives its cases from the ticket's acceptance criteria and
		// writes tests against them. Sonnet: the specification is written
		// down, so the work is careful reading rather than design, and opus
		// would be paying implementer prices to author test files.
		events.ActorQA:    {ID: events.ActorQA, Name: "Anita", Designation: "QA engineer", Model: "sonnet"},
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
		if over.Name != nil {
			base.Name = strings.TrimSpace(*over.Name)
		}
		if s := strings.TrimSpace(over.Designation); s != "" {
			base.Designation = s
		}
		if s := strings.TrimSpace(over.Model); s != "" {
			base.Model = s
		}
		if s := strings.TrimSpace(over.Effort); s != "" {
			base.Effort = s
		}
		next[id] = base
	}
	if err := noDuplicateNames(next); err != nil {
		return err
	}
	mu.Lock()
	defer mu.Unlock()
	current = next
	return nil
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
