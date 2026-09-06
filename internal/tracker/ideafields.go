package tracker

import (
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strings"
)

// Filling in a discovery idea's own fields.
//
// A Jira Product Discovery idea has more than a summary and a description:
// a short description, a documents link, a theme, a roadmap horizon, a state.
// Orion filed only the first two and left a form that looks abandoned.
//
// EVERY ONE OF THESE IS A CUSTOM FIELD, and its id differs per Jira instance
// -- customfield_10094 is Theme here and something else elsewhere. So nothing
// is hardcoded: the ids are read from the project's own create metadata by
// FIELD NAME, and an option value is matched against what the project
// actually offers. A field the project does not have, or a value it does not
// allow, is skipped rather than guessed at, because a rejected write loses
// the whole update and a wrong one files work under a theme nobody chose.

// IdeaField names a field to set by its human name, with a value expressed
// the way a person would say it.
//
// By name rather than by id because the caller -- ultimately a planning agent
// reading a product's website -- knows "Theme: Delight users" and cannot know
// customfield_10094.
type IdeaField struct {
	Name  string
	Value string
}

// fieldMeta is what createmeta says about one field.
type fieldMeta struct {
	FieldID string `json:"fieldId"`
	Name    string `json:"name"`
	Schema  struct {
		Type   string `json:"type"`
		Custom string `json:"custom"`
	} `json:"schema"`
	AllowedValues []struct {
		ID    string `json:"id"`
		Value string `json:"value"`
		Name  string `json:"name"`
	} `json:"allowedValues"`
}

// label is what a person would call this option.
func (f fieldMeta) optionID(want string) (string, bool) {
	want = strings.ToLower(strings.TrimSpace(want))
	for _, v := range f.AllowedValues {
		label := v.Value
		if label == "" {
			label = v.Name
		}
		if strings.ToLower(strings.TrimSpace(label)) == want {
			return v.ID, true
		}
	}
	return "", false
}

// ideaFieldMeta reads the create metadata for one issue type, keyed by
// lower-cased field name.
func (j *Jira) ideaFieldMeta(projectKey, typeID string) (map[string]fieldMeta, error) {
	code, body, err := j.do("GET", fmt.Sprintf(
		"/rest/api/3/issue/createmeta/%s/issuetypes/%s?maxResults=200",
		url.PathEscape(projectKey), url.PathEscape(typeID)), nil)
	if err != nil {
		return nil, err
	}
	if code >= 400 {
		return nil, fmt.Errorf("reading %s's fields: %d %s", projectKey, code, snippet(body))
	}
	var out struct {
		Fields []fieldMeta `json:"fields"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, err
	}
	byName := make(map[string]fieldMeta, len(out.Fields))
	for _, f := range out.Fields {
		byName[strings.ToLower(strings.TrimSpace(f.Name))] = f
	}
	return byName, nil
}

// IdeaFieldInfo is one writable field and, when it has them, the values it
// will accept. Returned so a caller can be told what to write instead of
// discovering it by having a write refused.
type IdeaFieldInfo struct {
	Name     string
	Options  []string
	Writable bool
}

// IdeaFields lists what a project's issue type offers.
//
// Numeric and computed fields are marked unwritable: Polaris's insight and
// comment counts, its delivery progress and its formula scores are derived
// from other data, and a caller offered them as writable would keep trying.
func (j *Jira) IdeaFields(projectKey, typeID string) ([]IdeaFieldInfo, error) {
	meta, err := j.ideaFieldMeta(projectKey, typeID)
	if err != nil {
		return nil, err
	}
	out := make([]IdeaFieldInfo, 0, len(meta))
	for _, f := range meta {
		out = append(out, IdeaFieldInfo{
			Name:     f.Name,
			Options:  f.optionLabels(),
			Writable: writableIdeaField(f),
		})
	}
	sort.Slice(out, func(a, b int) bool { return out[a].Name < out[b].Name })
	return out, nil
}

// derivedFields are reported by createmeta but are not a caller's to write.
//
// Polaris DERIVES these -- the counts from the idea's own comments and
// insights, the delivery figures from its linked delivery tickets, the scores
// from its formulas. Offering them as writable sends an agent to set a
// progress bar rather than to link the work that would move it.
//
// Matched by Jira's own custom-field type, not by name, so the rule holds on
// a project that renamed its fields.
var derivedFields = map[string]bool{
	"jira.polaris:count-insights":           true,
	"jira.polaris:count-issue-comments":     true,
	"jira.polaris:count-linked-issues":      true,
	"jira.polaris:delivery-progress":        true,
	"jira.polaris:delivery-status":          true,
	"jira.polaris:formula":                  true,
	"jira.polaris:atlassian-project":        true,
	"jira.polaris:atlassian-project-status": true,
}

// systemFields are real fields that are simply not part of describing an
// idea. Set through the commands that own them, not through this one.
var systemFields = map[string]bool{
	"assignee": true, "reporter": true, "labels": true, "summary": true,
	"description": true, "rank": true, "team": true, "issuelinks": true,
	"start date": true, "goals": true,
	// These define what the issue IS. Changing them here would move the idea
	// to another project or turn it into something else.
	"issue type": true, "project": true, "priority": true,
}

func writableIdeaField(f fieldMeta) bool {
	if f.Schema.Type == "number" || derivedFields[f.Schema.Custom] {
		return false
	}
	if systemFields[strings.ToLower(strings.TrimSpace(f.Name))] {
		return false
	}
	// Archival is a lifecycle action, not a description of the idea.
	return !strings.HasPrefix(strings.ToLower(f.Name), "idea archived")
}

// SetIdeaFields writes the named fields onto an issue, and reports which ones
// it could not set and why.
//
// Skipping rather than failing is deliberate and load-bearing. These fields
// are a convenience on an idea that already exists with its summary and
// description intact; one unknown theme must not cost the other four writes,
// and it must certainly not fail the command that filed the idea. The skipped
// list is returned so the caller can say what did not land instead of
// pretending everything did.
func (j *Jira) SetIdeaFields(key, projectKey, typeID string, want []IdeaField) (skipped []string, err error) {
	if len(want) == 0 {
		return nil, nil
	}
	meta, err := j.ideaFieldMeta(projectKey, typeID)
	if err != nil {
		return nil, err
	}

	fields := map[string]any{}
	for _, w := range want {
		if strings.TrimSpace(w.Value) == "" {
			continue
		}
		f, ok := meta[strings.ToLower(strings.TrimSpace(w.Name))]
		if !ok {
			skipped = append(skipped, fmt.Sprintf("%s (this project has no such field)", w.Name))
			continue
		}
		switch f.Schema.Type {
		case "option":
			id, ok := f.optionID(w.Value)
			if !ok {
				skipped = append(skipped, fmt.Sprintf("%s=%q (not one of its options: %s)",
					w.Name, w.Value, strings.Join(f.optionLabels(), ", ")))
				continue
			}
			fields[f.FieldID] = map[string]any{"id": id}
		case "array":
			// A multi-select takes a list even for one value.
			id, ok := f.optionID(w.Value)
			if !ok {
				skipped = append(skipped, fmt.Sprintf("%s=%q (not one of its options: %s)",
					w.Name, w.Value, strings.Join(f.optionLabels(), ", ")))
				continue
			}
			fields[f.FieldID] = []any{map[string]any{"id": id}}
		case "number":
			skipped = append(skipped, fmt.Sprintf("%s (numeric fields are not set from text)", w.Name))
		default:
			// string, url, and anything else Jira hands back as plain text.
			fields[f.FieldID] = w.Value
		}
	}
	if len(fields) == 0 {
		return skipped, nil
	}

	code, body, err := j.do("PUT", "/rest/api/3/issue/"+url.PathEscape(key),
		map[string]any{"fields": fields})
	if err != nil {
		return skipped, err
	}
	if code >= 400 {
		return skipped, fmt.Errorf("setting fields on %s: %d %s", key, code, snippet(body))
	}
	return skipped, nil
}

// optionLabels is every value a field will accept, for a message that says
// what to use instead of only what was wrong.
func (f fieldMeta) optionLabels() []string {
	out := make([]string, 0, len(f.AllowedValues))
	for _, v := range f.AllowedValues {
		if v.Value != "" {
			out = append(out, v.Value)
			continue
		}
		out = append(out, v.Name)
	}
	return out
}
