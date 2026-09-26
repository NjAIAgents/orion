package main

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

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

// A DOCUMENT IS A THIRD WAY TO ARRIVE ALREADY WRITTEN DOWN (OR-443), same
// shape as the tracker-key path above: someone has already put the idea in
// words -- a design doc on disk, a spec somebody wrote and shared as a link
// -- and re-typing it at the interview prompt would lose the actual wording
// while asking the same five questions a real document usually answers
// already. `orion new ./doc.md` or `orion new https://.../doc.md` reads the
// document's content as the idea text; the interview still runs on
// whatever the document leaves unanswered, exactly as it does for prose
// typed directly.
//
// This is deliberately NOT the same path as looksLikeIdeaKey's
// writtenDown/fetchIdea: those read a STRUCTURED Jira issue (Summary,
// Description, a template to detect). A document is unstructured prose,
// read once and handed to elaborate() as the idea text, the same as if it
// had been typed inline -- simpler, and correct for a format this command
// has no schema for.

// looksLikeFilePath reports whether s names a file that exists on disk,
// rather than describing an idea in prose. Existence-checked, not
// extension-checked: a document does not have to be .md to be a document,
// and an idea that happens to start with "./" or contain a "/" but names no
// real file is exactly what falling through to prose handles correctly.
func looksLikeFilePath(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" {
		return false
	}
	info, err := os.Stat(s)
	return err == nil && !info.IsDir()
}

// looksLikeURL reports whether s is an http(s) URL, as opposed to prose
// that merely mentions one ("check https://example.com for background" is
// an idea ABOUT a URL, not a request to fetch it -- the same distinction
// ideaKey draws between a bare key and a sentence naming one). Anchored to
// the whole trimmed argument for that reason.
var urlWholeArg = regexp.MustCompile(`^https?://\S+$`)

func looksLikeURL(s string) bool {
	return urlWholeArg.MatchString(strings.TrimSpace(s))
}

// docHTTPTimeout bounds the one network call this human-run, terminal-gated
// command ever makes -- unlike an agent stage (which is denied WebFetch/
// curl/wget entirely as an egress control, prompts.go), `orion new` is
// invoked directly by a person, the same trust level as them running curl
// themselves, so a fetch here crosses no boundary the sandbox exists to
// enforce. Still bounded: a hung server must not hang the interview.
const docHTTPTimeout = 15 * time.Second

// readDocument returns the idea text from a local file or a URL. Verbatim,
// like fetchIdea's own description -- this command has no standing to
// summarise someone else's document, and a later stage designing from a
// paraphrase would be designing from Orion's reading of it rather than
// from what was actually written.
func readDocument(s string) (string, error) {
	s = strings.TrimSpace(s)
	if looksLikeURL(s) {
		if _, err := url.Parse(s); err != nil {
			return "", fmt.Errorf("%s does not parse as a URL: %w", s, err)
		}
		client := &http.Client{Timeout: docHTTPTimeout}
		resp, err := client.Get(s)
		if err != nil {
			return "", fmt.Errorf("fetching %s: %w", s, err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return "", fmt.Errorf("fetching %s: server said %s", s, resp.Status)
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		if err != nil {
			return "", fmt.Errorf("reading the response from %s: %w", s, err)
		}
		if strings.TrimSpace(string(body)) == "" {
			return "", fmt.Errorf("%s returned an empty document", s)
		}
		return string(body), nil
	}
	body, err := os.ReadFile(s)
	if err != nil {
		return "", fmt.Errorf("reading %s: %w", s, err)
	}
	if strings.TrimSpace(string(body)) == "" {
		return "", fmt.Errorf("%s is empty", s)
	}
	return string(body), nil
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
