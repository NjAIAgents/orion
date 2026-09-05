package tracker

// Creating issues, as distinct from creating a project.
//
// Orion has read and mutated issues since the queue existed, and created
// PROJECTS since `orion new`, but never created an issue: the tree came from
// a skill talking to Jira through an MCP connector. Bringing that in means
// the client needs the one verb it was missing, and the interesting part is
// not the POST -- it is the issue TYPE, which is per-project and not
// guessable. A company-managed project calls the bottom level "Sub-task", a
// team-managed one calls it "Subtask", a project can rename any of them, and
// a project can have no Epic type at all. So the types are read from the
// project rather than assumed, once, and matched by Jira's own hierarchy
// level instead of by a name Orion hopes is there.

import (
	"encoding/json"
	"fmt"
	"strings"
)

// IssueType is one issue type a project actually offers.
type IssueType struct {
	ID   string
	Name string
	// Subtask is Jira's own flag for the level below Story. It exists
	// separately from HierarchyLevel because it is the field the API has
	// always returned, and it is the one that decides whether an issue can
	// take a Story as its parent.
	Subtask bool
	// HierarchyLevel is 1 for Epic, 0 for Story/Task/Bug, -1 for a subtask.
	// A renamed Epic is still level 1, which is why this is what the mapping
	// keys off.
	HierarchyLevel int
}

// IssueTypes reports the issue types the project offers for creation.
//
// Read from createmeta rather than from the global type list: the global
// list includes types this project cannot use, and creating with one of
// those fails with a message about a field rather than about the type.
func (j *Jira) IssueTypes(project string) ([]IssueType, error) {
	project = strings.TrimSpace(project)
	if project == "" {
		return nil, fmt.Errorf("which project? an issue-type lookup needs a project key")
	}
	code, body, err := j.do("GET", "/rest/api/3/issue/createmeta/"+project+"/issuetypes", nil)
	if err != nil {
		return nil, err
	}
	if code == 404 {
		return nil, fmt.Errorf("Jira has no project %s, or this account cannot create in it", project)
	}
	if code >= 400 {
		return nil, fmt.Errorf("reading the issue types of %s: %d %s", project, code, snippet(body))
	}
	var res struct {
		IssueTypes []struct {
			ID             string `json:"id"`
			Name           string `json:"name"`
			Subtask        bool   `json:"subtask"`
			HierarchyLevel int    `json:"hierarchyLevel"`
		} `json:"issueTypes"`
	}
	if err := json.Unmarshal(body, &res); err != nil {
		return nil, fmt.Errorf("reading the issue types of %s: %w", project, err)
	}
	out := make([]IssueType, 0, len(res.IssueTypes))
	for _, t := range res.IssueTypes {
		out = append(out, IssueType{ID: t.ID, Name: t.Name, Subtask: t.Subtask, HierarchyLevel: t.HierarchyLevel})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%s offers no issue types this account can create", project)
	}
	return out, nil
}

// NewIssue is one issue to create.
type NewIssue struct {
	Project string
	// TypeID is the issue type's id, from IssueTypes. An id rather than a
	// name because a name is per-project and a mapping that guessed one
	// would fail on the projects that renamed it.
	TypeID      string
	Summary     string
	Description string
	Labels      []string
	// ParentKey hangs this issue off another. Jira uses the same field for
	// both of its hierarchies -- a subtask under a Story, and a Story under
	// an Epic -- which is what internal/tracker/children.go already relies
	// on when it reads them back.
	ParentKey string
}

// CreateIssue creates one issue and returns its key.
func (j *Jira) CreateIssue(in NewIssue) (string, error) {
	if strings.TrimSpace(in.Project) == "" || strings.TrimSpace(in.TypeID) == "" {
		return "", fmt.Errorf("an issue needs a project and an issue type")
	}
	if strings.TrimSpace(in.Summary) == "" {
		return "", fmt.Errorf("an issue needs a summary")
	}

	fields := map[string]any{
		"project":   map[string]any{"key": in.Project},
		"issuetype": map[string]any{"id": in.TypeID},
		// Jira caps a summary at 255 characters and rejects a longer one
		// outright. A task line naming three file paths can exceed that, and
		// losing the whole tree to it would be absurd -- the full text is in
		// the description either way.
		"summary": truncate(in.Summary, 255),
	}
	if in.Description != "" {
		fields["description"] = adfParagraphs(in.Description)
	}
	if len(in.Labels) > 0 {
		fields["labels"] = jiraLabels(in.Labels)
	}
	if in.ParentKey != "" {
		fields["parent"] = map[string]any{"key": in.ParentKey}
	}

	code, body, err := j.do("POST", "/rest/api/3/issue", map[string]any{"fields": fields})
	if err != nil {
		return "", err
	}
	if code >= 400 {
		// Jira's own message names the field it objected to, which is the
		// difference between "fix the label" and "creation failed".
		return "", fmt.Errorf("Jira refused the issue: %d %s", code, snippet(body))
	}
	var res struct{ Key string }
	if err := json.Unmarshal(body, &res); err != nil || res.Key == "" {
		return "", fmt.Errorf("Jira accepted the issue but returned no key: %s", snippet(body))
	}
	return res.Key, nil
}

// jiraLabels makes labels Jira will accept: it rejects any label containing
// whitespace, with a message about the field rather than about the value.
func jiraLabels(in []string) []string {
	out := make([]string, 0, len(in))
	for _, l := range in {
		l = strings.Join(strings.Fields(l), "-")
		if l != "" {
			out = append(out, l)
		}
	}
	return out
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}
