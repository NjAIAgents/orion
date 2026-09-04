package decompose

// The routing marker, set here rather than asked for in a prompt.
//
// Routing reads a marker off a created ticket and infers nothing from its
// summary (internal/work/route.go). For as long as creation was delegated,
// the only way to get one written was a prompt telling the agent to run
// `orion routes` and apply what it printed -- which works, and is still what
// the decompose stage prompt says, but it is an instruction rather than a
// guarantee: the run that forgets produces a tree where every item defaults
// to the backend developer while the log correctly announces the default
// (OR-191). Creating the items natively means the marker can simply be set.
//
// WHAT IT IS SET FROM matters as much as that it is set. route.go warns
// against inferring an actor from a summary, and the warning is right: prose
// reads as docs or UI work about as often as it does not. So this reads only
// the STRUCTURAL parts of a task line -- the exact file paths and the phase
// heading -- and matches whole tokens against the published vocabulary by
// equality, the same rule routing itself applies. A path of docs/api.md is a
// documentation ticket; "improve the documentation story" in a description
// is not evidence of anything. No match means no marker, which is the
// default the table already documents.
//
// The vocabulary is work.Rules() itself, never a copy. A second copy is the
// thing OR-191 was about.

import (
	"path"
	"regexp"
	"strings"

	"github.com/orion-sdlc/orion/internal/work"
)

var tokenSplit = regexp.MustCompile(`[^a-z0-9]+`)

// marker returns the routing label for the given structural text, or "" when
// nothing in the published vocabulary matches.
//
// The label written is the rule's FIRST keyword rather than whichever
// synonym happened to appear in a path, so two tasks that route to the same
// actor carry the same label on the board. Every keyword in a rule routes
// identically, so the choice is cosmetic to routing and not to a human
// reading the tracker.
func marker(texts ...string) string {
	tokens := map[string]bool{}
	for _, t := range texts {
		for _, tok := range tokenSplit.Split(strings.ToLower(t), -1) {
			if tok != "" {
				tokens[tok] = true
			}
		}
	}
	// Rules are in precedence order, and the first match wins -- the same
	// precedence routing applies, so a marker set here reaches the actor
	// this predicted.
	for _, r := range work.Rules() {
		for _, kw := range r.Keywords {
			if tokens[strings.ToLower(kw)] {
				return r.Keywords[0]
			}
		}
	}
	return ""
}

// label puts the identity label and the routing marker on every item.
func label(t *Tree) {
	id := t.Label()

	_ = t.Walk(func(it, _ *Item) error {
		it.Labels = []string{id}
		switch it.Kind {
		case KindTask:
			if m := marker(signals(it)...); m != "" {
				it.Labels = append(it.Labels, m)
			}
		case KindStory:
			// A story is the unit an agent claims and works -- it works its
			// own children, in order, in one branch (internal/tracker/
			// children.go). So the story's marker is the one that decides
			// who does the work, and it is taken from its tasks when they
			// AGREE. A story whose tasks disagree is mixed work, and
			// picking one of them would send the whole story to an actor
			// that is wrong for the rest of it.
			it.Labels = appendIf(it.Labels, agreedMarker(it))
		case KindEpic:
			// Deliberately unmarked. An epic is a container: nothing works
			// it, so a marker on it would route nothing and only invite the
			// belief that it did.
		}
		return nil
	})
}

// signals are the structural parts of a task the marker may be read from:
// the DIRECTORIES its file paths sit in, and its phase heading.
//
// Directories rather than whole paths, and this is the difference between a
// working marker and a wrong one. A directory is a structural statement --
// docs/, web/ui/ -- while a file's base name is prose that happens to end in
// an extension: internal/catalogue/product.go would otherwise route to the
// PRODUCT MANAGER on the strength of a struct's name, which is precisely the
// infer-from-the-summary mistake route.go warns about, arriving through a
// path instead of a sentence.
func signals(t *Item) []string {
	out := make([]string, 0, len(t.Paths)+1)
	for _, p := range t.Paths {
		if dir := path.Dir(p); dir != "." && dir != "/" {
			out = append(out, dir)
		}
	}
	return append(out, t.Phase)
}

func agreedMarker(story *Item) string {
	agreed := ""
	for i, t := range story.Children {
		m := marker(signals(t)...)
		if i == 0 {
			agreed = m
			continue
		}
		if m != agreed {
			return ""
		}
	}
	return agreed
}

func appendIf(labels []string, s string) []string {
	if s == "" {
		return labels
	}
	return append(labels, s)
}
