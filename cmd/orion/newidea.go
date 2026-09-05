package main

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/orion-sdlc/orion/internal/tracker"
)

// Starting from an idea that is already written down (OR-349).
//
// `orion new` interviews because the idea usually is not written down: five
// questions asked while a person is present, because every later stage runs
// non-interactively and this is the last point at which anything can be
// asked. But a Product Discovery idea with a filled-in PRD has already
// answered them, and asking again wastes the one resource this command
// spends, which is the human's attention.
//
// So: `orion new PRIOR-3` reads the idea and skips the interview.
//
// NO POLARIS API. A discovery project's ideas come back through the ordinary
// issue API -- verified against PRIOR, whose ideas return summary,
// description and status exactly like any other issue -- so this reuses the
// tracker client that already exists rather than growing a second one.

// ideaKey matches a tracker key: letters, then a dash, then digits.
//
// Anchored, because this decides whether the argument is a KEY or an idea
// stated in prose. "OR-42" is a key; "OR-42 needs a rewrite" is an idea about
// one, and treating it as a key would fetch the wrong thing and silently plan
// from it.
var ideaKey = regexp.MustCompile(`^[A-Z][A-Z0-9]+-[0-9]+$`)

// looksLikeIdeaKey reports whether the argument names a ticket rather than
// describing an idea.
func looksLikeIdeaKey(s string) bool {
	return ideaKey.MatchString(strings.ToUpper(strings.TrimSpace(s)))
}

// ideaReader is the slice of the tracker this path needs: find one issue, and
// say something on it. Narrower than tracker.Tracker on purpose -- a test
// drives both without standing up project creation, and the interface names
// exactly the two permissions the feature requires.
type ideaReader interface {
	Search(jql string, maxResults int) ([]tracker.Issue, error)
	Comment(key, text string) error
}

// fetchIdea reads one idea by key.
//
// Through Search rather than a get-by-key, because Search is what the Tracker
// surface already exposes and a JQL `key = X` is exactly a get. One result or
// none; more than one is impossible for a key equality.
func fetchIdea(t ideaReader, key string) (tracker.Issue, error) {
	key = strings.ToUpper(strings.TrimSpace(key))
	// Through the JQL builder rather than fmt: Go quoting is not JQL
	// quoting, and a bare value breaks on a reserved word (internal/tracker
	// has a guard test for exactly this).
	found, err := t.Search(tracker.JQLEq("key", key), 1)
	if err != nil {
		return tracker.Issue{}, fmt.Errorf("reading %s: %w", key, err)
	}
	if len(found) == 0 {
		return tracker.Issue{}, fmt.Errorf("no issue %s, or this account cannot see it.\n"+
			"  Check the key, and that the credentials in `orion doctor` reach that project.", key)
	}
	return found[0], nil
}

// templateHeadings are the headings Jira's own discovery template ships with.
//
// They have to be NAMED rather than detected by markup. Jira stores a
// description as ADF and returns it flattened, so a heading comes back as a
// bare line with no "#" on it -- indistinguishable from prose by shape
// alone. Verified against the live PRIOR project, whose ideas return:
//
//	Objective
//	What outcome are we trying to achieve? What does success look like?
//	Problem
//	Define customer problems, why they're urgent and important.
//	...
//
// Without this list those five headings count as five lines of real prose
// and an untouched template reads as a written-up idea.
var templateHeadings = []string{
	"objective", "problem", "solution", "risks", "supporting documents",
	"success metrics", "scope", "out of scope",
}

// writtenDown reports whether an idea says enough to design from, and why not
// when it does not.
//
// THIS IS THE LOAD-BEARING CHECK. Every idea in PRIOR today contains the
// literal unfilled template, and planning from boilerplate would produce a
// confident, empty plan -- the exact failure the interview exists to prevent.
// A stage reading "Define customer problems, why they're urgent" as the
// problem statement will design for it.
//
// The test is prose UNDER headings, not length: the template is long, and a
// short idea written in two real sentences is worth more than five headings
// of placeholder. Erring toward "interview me" is the safe direction, because
// the cost is five questions rather than a planning run spent on nothing.
// isTemplateHeading reports whether a line is one of the template's own
// section headings rather than something a person wrote.
//
// A markdown "#" prefix is stripped first, because an idea written in a
// client that DOES keep the marker is the same heading either way.
func isTemplateHeading(line string) bool {
	line = strings.ToLower(strings.TrimSpace(strings.TrimLeft(line, "#")))
	line = strings.TrimSpace(strings.Trim(line, ":*"))
	for _, h := range templateHeadings {
		if line == h {
			return true
		}
	}
	return false
}

func writtenDown(idea tracker.Issue) (ok bool, why string) {
	body := strings.TrimSpace(idea.Description)
	if body == "" {
		return false, "it has no description"
	}

	var prose []string
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || isTemplateHeading(line) {
			continue
		}
		prose = append(prose, line)
	}
	if len(prose) == 0 {
		return false, "its description is headings with nothing written under them"
	}

	// Placeholder prose is the template's own instruction text. Matched on
	// the distinctive opening of each, so an idea that happens to use the
	// same headings with real content is not caught by it.
	// Matched on the distinctive OPENING of each, deliberately stopping
	// before the first apostrophe: Jira returns the curly form (they’re),
	// and matching on the straight one would silently never fire.
	placeholders := []string{
		"what outcome are we trying to achieve",
		"define customer problems",
		"outline the proposed solution",
		"list key risks",
		"embed docs, design file or pdf",
	}
	real := 0
	for _, line := range prose {
		low := strings.ToLower(line)
		isPlaceholder := false
		for _, p := range placeholders {
			if strings.Contains(low, p) {
				isPlaceholder = true
				break
			}
		}
		if !isPlaceholder {
			real++
		}
	}
	if real == 0 {
		return false, "its description is still the unfilled template"
	}
	return true, ""
}

// ideaDescription is the project description built from a written idea.
//
// The idea's own text is carried VERBATIM rather than summarised. It is the
// PM's words about their own product, this command has no standing to
// improve them, and a later stage designing from a paraphrase would be
// designing from Orion's reading rather than from what was written.
//
// The provenance line is first because it is the question a reader of the
// project asks: where did this come from.
func ideaDescription(idea tracker.Issue, site string) string {
	var b strings.Builder
	b.WriteString("From " + idea.Key)
	if site != "" {
		b.WriteString(" (" + strings.TrimRight(site, "/") + "/browse/" + idea.Key + ")")
	}
	b.WriteString("\n\n")
	if s := strings.TrimSpace(idea.Summary); s != "" {
		b.WriteString(s + "\n\n")
	}
	b.WriteString(strings.TrimSpace(idea.Description))
	b.WriteString("\n")
	return b.String()
}

// ideaBackLink is what gets said on the idea once the project exists.
//
// Stated as a fact and nothing more. Orion created a project from this idea;
// whether that means the idea should move to Delivery is the board owner's
// decision, and this command deliberately does not make it (see OR-349).
func ideaBackLink(projectKey, site string) string {
	link := projectKey
	if site != "" {
		link = strings.TrimRight(site, "/") + "/browse/" + projectKey
	}
	return "Orion created project " + projectKey + " from this idea: " + link +
		"\n\nThe idea's own status is unchanged -- moving it is yours to decide."
}
