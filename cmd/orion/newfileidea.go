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
	SetIdeaFields(key, projectKey, typeID string, want []tracker.IdeaField) ([]string, error)
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
func fileIdea(t ideaFiler, project, summary, description, link string) (string, error) {
	types, err := t.IssueTypes(project)
	if err != nil {
		return "", fmt.Errorf("reading %s's issue types: %w", project, err)
	}
	typeID := preferredIdeaType(types)
	if typeID == "" {
		return "", fmt.Errorf("%s has no issue type an idea could be filed as", project)
	}
	key, err := t.CreateIssue(tracker.NewIssue{
		Project:     project,
		TypeID:      typeID,
		Summary:     summary,
		Description: description,
	})
	if err != nil {
		return "", err
	}

	// Fill what can be DERIVED here, and nothing that needs judgement.
	//
	// A short description and a link are facts already in hand. Theme,
	// roadmap horizon and the rest are judgements about a product, and this
	// command has made no model call and read no URL -- it has the sentences
	// the operator typed and nothing more. The intent stage fills those,
	// after it has actually researched the idea (see supervisor's intent
	// prompt); guessing them here would put a confident wrong answer where a
	// blank was honest.
	fields := []tracker.IdeaField{
		{Name: "Idea short description", Value: firstSentence(description)},
	}
	if link != "" {
		fields = append(fields, tracker.IdeaField{Name: "Documents", Value: link})
	}
	// The idea exists by now, so a field that did not land is reported, never
	// fatal: losing the idea to a rejected optional field would be trading
	// the record for the trimmings.
	skipped, err := t.SetIdeaFields(key, project, typeID, fields)
	return key, fieldNote(skipped, err)
}

// fieldNote turns a partial field write into one line worth printing, or nil
// when everything landed.
//
// Returned as an error the caller WARNS on rather than fails on: it is
// information about a secondary write, and the only alternative -- silence --
// is how a field quietly stops being set and nobody notices for months.
func fieldNote(skipped []string, err error) error {
	switch {
	case err != nil:
		return fmt.Errorf("the idea was filed, but its fields were not set: %w", err)
	case len(skipped) > 0:
		return fmt.Errorf("the idea was filed; these fields were skipped: %s",
			strings.Join(skipped, "; "))
	}
	return nil
}

// firstSentence is the one-line form of the idea, for the field that shows in
// a list rather than on the idea's own page.
//
// The originator's own opening sentence rather than a summary of it: this is
// what they said the thing was, and a paraphrase in the list view that
// disagrees with the description below it is worse than a long line.
func firstSentence(s string) string {
	s = strings.TrimSpace(s)
	// Skip a provenance header if one is there.
	if i := strings.Index(s, "\n\n"); i > 0 && strings.HasPrefix(s, "From ") {
		s = strings.TrimSpace(s[i:])
	}
	for _, end := range []string{". ", ".\n", "! ", "? "} {
		if i := strings.Index(s, end); i > 0 {
			s = s[:i+1]
			break
		}
	}
	if i := strings.Index(s, "\n"); i > 0 {
		s = s[:i]
	}
	s = strings.TrimSpace(s)
	if len(s) > 255 {
		s = strings.TrimSpace(s[:252]) + "..."
	}
	return s
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
