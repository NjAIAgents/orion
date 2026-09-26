package queue

// Prose dependencies (OR-424): a dependency stated only in a ticket's own
// description text, invisible to tracker.Ready because nothing ever wrote
// it as an issue link.
//
// OR-95 already makes link-based dependencies deterministic and correct;
// this is not a second dependency engine, it is the gap OR-95 and OR-413
// leave between them -- a ticket written by hand, written before OR-413
// existed, or written by an agent that put the dependency in prose rather
// than as a link. That prose sits in Description, already fetched
// alongside BlockedBy for every candidate (internal/tracker/issues.go), so
// this reads a field decide() already has rather than asking Jira for
// anything new.
//
// DETERMINISTIC TEXT MATCHING, NOT A MODEL CALL -- OR-95's own reasoning
// restated for a different field: a topological check over links returns
// the same answer every time and can be tested; a model reading prose
// returns a plausible one.
//
// HOLD, NEVER AUTO-LINK. Writing a link is a change to the tracker made on
// an inference, and that belongs to a person (or to OR-413's creation-time
// path) -- not to a queue tick guessing what an English sentence meant.

import (
	"regexp"
	"strings"

	"github.com/orion-sdlc/orion/internal/tracker"
)

// depPhraseRe matches the phrases OR-424 names as dependency language,
// case-insensitive. A bare key mention alone ("see OR-123 for context") is
// deliberately NOT enough -- tickets cite each other constantly, and
// treating every mention as a dependency would hold most of the backlog.
// Each phrase must be followed by something: a key, or free text naming a
// thing that has no key at all.
var depPhraseRe = regexp.MustCompile(
	`(?i)(depends on|blocked by|requires|prerequisite)\s*:?\s+([^.\n]+)|` +
		`(?i)\bafter\s+([A-Z][A-Z0-9]*-[0-9]+)\s+lands\b|` +
		`(?i)\bonce\s+([A-Z][A-Z0-9]*-[0-9]+)\s+is\s+done\b`,
)

// keyRe pulls a ticket key out of matched text -- the same shape
// cmd/orion/keys.go's ticketKeyRe uses, defined again here rather than
// imported: cmd/orion depends on internal/queue, not the other way round.
//
// CASE-INSENSITIVE ON PURPOSE, unlike ticketKeyRe: that one validates a key
// a person TYPED, where case is a reasonable thing to enforce. This one
// FINDS a key inside prose a person wrote in whatever case felt natural --
// "depends on: or-16" names OR-16 as surely as "OR-16" does, and the
// match is normalised on the way out (norm, below) either way.
var keyRe = regexp.MustCompile(`(?i)\b[A-Z][A-Z0-9]*-[0-9]+\b`)

// ProseDependency is one dependency a ticket's own description states in
// text, whether or not it could be resolved to a key.
type ProseDependency struct {
	// Sentence is the matched text, quoted back in the hold reason so a
	// reader sees exactly what the ticket said rather than a paraphrase.
	Sentence string
	// Key is the dependency's ticket key, when the prose names one
	// ("Depends on: OR-60"). Empty when the prose names a thing instead
	// ("Depends on: server skeleton") -- see Unmapped.
	Key string
}

// Unmapped reports whether this dependency could not be resolved to a key.
// A prose dependency naming a thing rather than a ticket cannot be checked
// against BlockedBy without guessing which ticket it means, so it is
// reported as unmapped rather than silently ignored or wrongly matched.
func (d ProseDependency) Unmapped() bool { return d.Key == "" }

// reason is the sentence an operator reads, quoting the ticket's own text
// per OR-424's done-when -- "the reason names the quoted sentence". The
// unmapped case says so explicitly rather than reading like a resolved,
// checked dependency: there is nothing on the other end to have checked.
func (d ProseDependency) reason() string {
	if d.Unmapped() {
		return "names an unmapped dependency in its own description: " + quote(d.Sentence)
	}
	return "names " + d.Key + " as a dependency in its own description, not recorded as a link: " + quote(d.Sentence)
}

func quote(s string) string { return "\"" + s + "\"" }

// proseDependencies scans description for the phrases OR-424 names and
// returns every match, in the order they appear. A description with no
// such phrase returns nil.
func proseDependencies(description string) []ProseDependency {
	if description == "" {
		return nil
	}
	var out []ProseDependency
	for _, m := range depPhraseRe.FindAllStringSubmatch(description, -1) {
		switch {
		case m[1] != "":
			// "depends on|blocked by|requires|prerequisite" + free text.
			rest := strings.TrimSpace(m[2])
			if rest == "" {
				continue
			}
			out = append(out, ProseDependency{
				Sentence: strings.TrimSpace(m[0]),
				Key:      firstKeyIn(rest),
			})
		case m[3] != "":
			// "after <KEY> lands"
			out = append(out, ProseDependency{Sentence: strings.TrimSpace(m[0]), Key: norm(m[3])})
		case m[4] != "":
			// "once <KEY> is done"
			out = append(out, ProseDependency{Sentence: strings.TrimSpace(m[0]), Key: norm(m[4])})
		}
	}
	return out
}

// firstKeyIn returns the first ticket key text names, or "" when it names
// none -- "Depends on: OR-60" resolves, "Depends on: server skeleton" does
// not, and guessing a key out of the latter would invent a link nobody
// wrote.
func firstKeyIn(text string) string {
	if k := keyRe.FindString(text); k != "" {
		return norm(k)
	}
	return ""
}

// proseBlockedBy checks i's description for a stated dependency absent
// from i.BlockedBy, and reports the first one found as a hold reason.
//
// TWO KINDS OF HIT: a dependency naming a key is checked against BlockedBy
// directly, exactly as tracker.Ready checks a real link. A dependency
// naming a thing cannot be checked that way at all -- it is held
// unconditionally and reported as unmapped, because a description that
// says "depends on: server skeleton" with no way to verify that dependency
// is satisfied is not a ticket the queue can vouch for either way.
//
// A DEPENDENCY ALREADY RECORDED AS A LINK DISPATCHES NORMALLY: this checks
// membership in i.BlockedBy, not whether that blocker is resolved --
// tracker.Ready (via blockedBy, called earlier in decide) already holds an
// unresolved LINKED blocker, so reaching this function with a key match
// already means the link exists, and only an unlinked prose mention is
// this function's business.
func proseBlockedBy(i tracker.Issue) (ProseDependency, bool) {
	linked := make(map[string]bool, len(i.BlockedBy))
	for _, k := range i.BlockedBy {
		linked[norm(k)] = true
	}
	for _, dep := range proseDependencies(i.Description) {
		if dep.Unmapped() {
			return dep, true
		}
		if !linked[dep.Key] {
			return dep, true
		}
	}
	return ProseDependency{}, false
}
