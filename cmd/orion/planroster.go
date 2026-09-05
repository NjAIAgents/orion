package main

// Which actors a planning run uses, and why each one is on it (OR-150).
//
// DELIBERATELY NOT A MODEL CALL, for the reason internal/dba and
// internal/work/route.go are not: this decides whether to SPEND, so a
// classifier that costs a model call to reach the decision has already spent
// on every run, forever. It would also make two similar ideas cost different
// amounts for reasons nobody can reconstruct afterwards, which is the whole
// property a roster printed before dispatch exists to give.
//
// THERE IS NO LIST OF ACTORS IN THIS FILE. The candidates are
// actors.ConfigurableIDs(), which globs the shipped roster, so registering an
// actor -- the database architect in OR-135, and whatever follows it -- puts
// that actor into planning without an edit here. A literal slice would mean
// the next actor is announced by `orion config agents`, routed by
// `orion routes`, and invisible to planning until somebody noticed.
//
// TWO SIGNALS, both checkable by a reader against something they can see:
//
//   A STAGE. The actor runs one of planStages, so it is on every run whatever
//   the idea says. Read from planStages rather than restated, for the reason
//   that slice exists at all.
//
//   THE IDEA'S OWN WORDS. The idea names a word that belongs to exactly one
//   actor -- its identifier, or a word of its designation. "a payments API
//   with a Postgres schema" says "schema"... which belongs to nobody, and
//   "database" would reach the database architect. That is the deliberate
//   ceiling: this reads the vocabulary the roster already publishes, not a
//   synonym table somebody has to remember to extend. A word two actors share
//   ("developer", "engineer") selects NEITHER, computed rather than listed, so
//   the ambiguity is resolved by construction and a new actor cannot quietly
//   make an existing word ambiguous without losing it.
//
// Matched by word EQUALITY, the rule internal/work/route.go settled on: a
// substring match would read "we should index the docs page" as a database
// idea, and a signal nobody can predict is worse than one that misses.

import (
	"sort"
	"strings"

	"github.com/orion-sdlc/orion/internal/actors"
)

// planActor is one actor on this run, and the signal that put it there.
//
// The signal is carried rather than reduced to a boolean for internal/dba's
// reason: "this run needs the database architect" is not something a reader
// can verify, and `the idea says "database"` is.
type planActor struct {
	ID     string
	Signal string
	// FromIdea distinguishes an actor the idea selected from one that runs a
	// stage on every run. The announcement prints them differently: the chain
	// is fixed and ordered, the rest varies per idea and has to say why.
	FromIdea bool
}

// planRoster is every actor this planning run uses, in a deterministic order.
//
// Deterministic in both senses: the same idea always produces the same roster,
// and the order is ConfigurableIDs' own -- sorted by identifier -- so two runs
// of the same idea print the same lines rather than the same set shuffled.
func planRoster(idea string) []planActor {
	stages := map[string][]string{}
	for _, s := range planStages {
		stages[s.Actor] = append(stages[s.Actor], s.Stage)
	}

	vocab := planVocabulary()
	words := ideaWords(idea)

	ids := actors.ConfigurableIDs()
	out := make([]planActor, 0, len(ids))
	for _, id := range ids {
		if names, ok := stages[id]; ok {
			out = append(out, planActor{ID: id, Signal: "runs the " + andJoin(names) + " stage"})
			continue
		}
		// An actor that runs on no model is not a participant. Orion itself is
		// Go and is the narrator of this output, not an agent that can be
		// dispatched -- a property, not a name, so nothing here needs editing
		// when the roster changes.
		if actors.Model(id) == "" {
			continue
		}
		if w, ok := firstWord(vocab[id], words); ok {
			out = append(out, planActor{ID: id, Signal: `the idea says "` + w + `"`, FromIdea: true})
		}
	}
	return out
}

// planVocabulary is each configurable actor's own words: its identifier, and
// the words of its designation, minus any word a second actor also answers to.
//
// Built from the CONFIGURED roster, so an operator who renamed a designation
// gets selection on the words they chose rather than on the ones this build
// shipped. That is the same rule the announcement follows for names, and the
// alternative -- selecting on shipped words while printing configured ones --
// would print a roster that does not explain itself.
func planVocabulary() map[string][]string {
	owners := map[string][]string{}
	for _, id := range actors.ConfigurableIDs() {
		for _, w := range append([]string{id}, strings.Fields(actors.Get(id).Designation)...) {
			w = strings.ToLower(strings.Trim(strings.TrimSpace(w), ".,;:()"))
			if w == "" {
				continue
			}
			if n := len(owners[w]); n == 0 || owners[w][n-1] != id {
				owners[w] = append(owners[w], id)
			}
		}
	}

	out := map[string][]string{}
	for w, ids := range owners {
		if len(ids) != 1 {
			continue // two actors answer to it, so it names neither
		}
		out[ids[0]] = append(out[ids[0]], w)
	}
	for _, ws := range out {
		sort.Strings(ws)
	}
	return out
}

// ideaWords is the idea reduced to the words a signal can match.
//
// A hyphenated token is kept whole AND split, because both forms are real:
// the identifiers include log-triage and case-derive, while "database-backed"
// is an idea about a database. Keeping only one form would lose one of them.
func ideaWords(idea string) map[string]bool {
	out := map[string]bool{}
	for _, f := range strings.FieldsFunc(strings.ToLower(idea), func(r rune) bool {
		return !(r == '-' || r == '_' || r >= 'a' && r <= 'z' || r >= '0' && r <= '9')
	}) {
		f = strings.Trim(f, "-_")
		if f == "" {
			continue
		}
		out[f] = true
		for _, part := range strings.FieldsFunc(f, func(r rune) bool { return r == '-' || r == '_' }) {
			out[part] = true
		}
	}
	return out
}

// firstWord reports the first of an actor's words the idea uses. vocab is
// sorted, so an idea naming two of them selects on the same one every time.
func firstWord(vocab []string, words map[string]bool) (string, bool) {
	for _, w := range vocab {
		if words[w] {
			return w, true
		}
	}
	return "", false
}

// andJoin renders a list the way a sentence does, so an actor on two stages
// reads as "the spec and plan stage" rather than as a slice.
func andJoin(s []string) string {
	switch len(s) {
	case 0:
		return ""
	case 1:
		return s[0]
	}
	return strings.Join(s[:len(s)-1], ", ") + " and " + s[len(s)-1]
}
