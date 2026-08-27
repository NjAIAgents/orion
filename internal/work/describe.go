package work

import (
	"encoding/json"
	"strings"

	"github.com/orion-sdlc/orion/internal/actors"
	"github.com/orion-sdlc/orion/internal/events"
)

// Writing the pull request description with nj-agents' pr-describe.
//
// Orion's own description was assembled from two things it already had: the
// ticket summary as the title, and a commit count in the body. That is
// accurate and nearly useless -- it restates what the ticket said and what
// the commit list shows, and tells a reviewer nothing about what the change
// actually does or how to convince themselves it is right.
//
// pr-describe reads the branch's whole delta against its base plus its
// commit messages, and drafts a structured title and body: summary, what
// changed, why, test plan. It is a WORKFLOW-class skill -- it proposes and
// never acts, never pushes, never opens a non-draft pull request -- which
// makes it exactly the right shape here. Orion asks for the text and opens
// the pull request itself.
//
// Read-only, and on the same seam as the advisors. The description of a
// change must not be able to alter the change: an agent that "just fixed a
// typo while it was in there" has produced a diff nobody reviewed, on a
// branch already declared finished.

// maxPRBody bounds what is sent to the tracker and GitHub. A body that has
// to be scrolled past to reach the diff is one nobody reads to the end of.
const maxPRBody = 8000

// Describer drafts a pull request title and body. Same signature as
// advise.Runner, because it is the same kind of thing: a read-only agent
// turn in a directory.
type Describer func(dir, model, prompt string) (string, error)

// describePR asks for a description, and returns whether it got one.
//
// Every failure path returns false rather than an error. A pull request that
// opens with a plainer description is enormously better than one that does
// not open at all -- the branch is already pushed, the work is already done,
// and refusing to open the PR would strand it for a cosmetic reason.
func describePR(run Describer, dir, key, fallbackTitle, fallbackBody string) (string, string, bool) {
	if run == nil {
		return fallbackTitle, fallbackBody, false
	}
	// The describer's own model, from the registry rather than a literal, so
	// the line that reports what wrote the description cannot disagree with
	// what actually did.
	out, err := run(dir, actors.Model(events.ActorDescriber), describePrompt(key))
	if err != nil {
		return fallbackTitle, fallbackBody, false
	}
	title, body, ok := parseDescription(out)
	if !ok {
		return fallbackTitle, fallbackBody, false
	}
	// Keep Orion's own trailer. The skill writes for a human reviewer and
	// knows nothing about the ticket or the run that produced the branch,
	// and those two links are what let anyone reading this in six months
	// find out why it exists.
	return title, body + "\n\n---\n" + fallbackBody, true
}

func describePrompt(key string) string {
	return strings.Join([]string{
		"Use the pr-describe skill to draft a pull request description for the",
		"current branch. It is already committed and pushed; you are describing",
		"it, not changing it.",
		"",
		"Do NOT open the pull request, push, or run any command that writes.",
		"Orion opens it with the text you return.",
		"",
		"The ticket is " + key + ".",
		"",
		"Reply with ONLY a JSON object, no prose around it:",
		`  {"title": "...", "body": "..."}`,
		"",
		"The title should read as one line in a list of pull requests: what",
		"changed, not that something changed. The body is markdown.",
		"",
		"If you cannot run the skill, say so in one line instead of returning",
		"JSON. A plain description that Orion writes itself is a better outcome",
		"than an invented one that looks authoritative.",
	}, "\n")
}

// parseDescription pulls the JSON object out of the reply.
//
// Tolerant of a model that wraps its answer in a fence or a sentence, and
// strict about the result: a description with no title is not a partial
// success to paper over, because the title is the half a reviewer reads
// first and often the only half they read at all.
func parseDescription(out string) (string, string, bool) {
	s := strings.TrimSpace(out)
	if i := strings.Index(s, "{"); i >= 0 {
		if j := strings.LastIndex(s, "}"); j > i {
			s = s[i : j+1]
		}
	}
	var d struct {
		Title string `json:"title"`
		Body  string `json:"body"`
	}
	if json.Unmarshal([]byte(s), &d) != nil {
		return "", "", false
	}
	title := strings.TrimSpace(d.Title)
	body := strings.TrimSpace(d.Body)
	if title == "" || body == "" {
		return "", "", false
	}
	if len(body) > maxPRBody {
		body = body[:maxPRBody] + "\n\n_(truncated)_"
	}
	// A title that spans lines breaks `gh pr create --title`, and a wrapped
	// one reads badly in a list. Take the first line and bound it.
	if i := strings.IndexByte(title, '\n'); i > 0 {
		title = strings.TrimSpace(title[:i])
	}
	if len(title) > 120 {
		title = title[:117] + "..."
	}
	return title, body, true
}
