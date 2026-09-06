package main

import (
	"bufio"
	"fmt"
	"io"
	"strings"

	"github.com/orion-sdlc/orion/internal/creds"
	"github.com/orion-sdlc/orion/internal/tracker"
	"github.com/orion-sdlc/orion/internal/ui"
)

// Filing an interviewed idea back into the discovery project.
//
// `orion new PRIOR-3` reads an idea somebody already wrote down (OR-349).
// `orion new` with no key interviews instead -- and those answers went only
// into the new project's description, so an idea typed at the prompt never
// joined the list of ideas, and the discovery board showed a gap where a
// week's work had actually started.
//
// This closes that: whichever way the idea arrived, it ends up in the same
// place.
//
// ASKED FOR ONCE, THEN REMEMBERED. Which project holds ideas is a fact about
// someone's tracker, not something Orion can derive, and defaulting to one
// would file work into a project the operator did not choose. Declining is
// remembered too -- an unasked question and a stored "no" look identical
// otherwise, and the difference is whether to ask again next time.

// ideaFiler is the slice of the tracker this needs.
type ideaFiler interface {
	CreateIssue(in tracker.NewIssue) (string, error)
	IssueTypes(projectKey string) ([]tracker.IssueType, error)
}

// ideasProject resolves where interviewed ideas are filed, asking once and
// saving the answer.
//
// Returns "" when ideas are not to be filed -- declined now, declined before,
// or no terminal to ask in. Never an error: this is a convenience, and a
// command that fails because a nice-to-have could not be configured is worse
// than one that quietly does less.
func ideasProject(home string, r *bufio.Reader, out io.Writer, interactive bool) string {
	switch v := strings.TrimSpace(creds.Get(home, creds.IdeasProject)); {
	case v == creds.IdeasNone:
		return "" // asked before, declined
	case v != "":
		return v
	}
	if !interactive {
		return ""
	}

	fmt.Fprintln(out)
	fmt.Fprintln(out, ui.Heading(out, "Ideas"))
	fmt.Fprintln(out, "Orion can file this idea in your tracker's discovery project, so it")
	fmt.Fprintln(out, "sits with the rest of your ideas rather than only in the new project's")
	fmt.Fprintln(out, "description. Asked once; the answer is saved.")
	fmt.Fprintln(out)
	answer, _ := ask(r, out, "Which project? (e.g. PRIOR -- blank to skip, and stop asking)")

	key := strings.ToUpper(strings.TrimSpace(answer))
	if key == "" {
		key = creds.IdeasNone
	}
	// Saved either way. A declined question that is asked again every run is
	// a question that gets answered wrongly to make it stop.
	if err := creds.Save(home, map[string]string{creds.IdeasProject: key}); err != nil {
		ui.Warn(out, "could not save the ideas project, so this will be asked again: %v", err)
	}
	if key == creds.IdeasNone {
		return ""
	}
	return key
}

// fileIdea creates the idea and returns its key.
//
// The issue type is resolved from the project rather than assumed: a
// discovery project's type is "Idea", an ordinary one's is "Story" or "Task",
// and the id differs per instance. Preferring "Idea" when it exists means the
// same call works on both without the caller knowing which it has.
func fileIdea(t ideaFiler, project, summary, description string) (string, error) {
	types, err := t.IssueTypes(project)
	if err != nil {
		return "", fmt.Errorf("reading %s's issue types: %w", project, err)
	}
	typeID := preferredIdeaType(types)
	if typeID == "" {
		return "", fmt.Errorf("%s has no issue type an idea could be filed as", project)
	}
	return t.CreateIssue(tracker.NewIssue{
		Project:     project,
		TypeID:      typeID,
		Summary:     summary,
		Description: description,
	})
}

// preferredIdeaType picks the type an idea should be, from what the project
// actually has: Idea, else Story, else Task, else the first non-subtask.
//
// A subtask is never eligible -- it cannot exist without a parent, and an
// idea has none.
func preferredIdeaType(types []tracker.IssueType) string {
	byName := map[string]string{}
	first := ""
	for _, t := range types {
		if t.Subtask {
			continue
		}
		byName[strings.ToLower(strings.TrimSpace(t.Name))] = t.ID
		if first == "" {
			first = t.ID
		}
	}
	for _, want := range []string{"idea", "story", "task"} {
		if id, ok := byName[want]; ok {
			return id
		}
	}
	return first
}

// ideaSummary is the one line the idea is listed under.
//
// The project name, because that is what the operator just chose to call this
// thing and it is what they will scan the board for. The description carries
// the full answers.
func ideaSummary(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return "Untitled idea"
	}
	return name
}
