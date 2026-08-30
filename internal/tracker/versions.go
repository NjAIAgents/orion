package tracker

// Jira versions, which Orion uses as release MILESTONES.
//
// Why this exists at all, given the MCP: the Jira MCP can SET fixVersions on
// an issue but cannot create a version, so a milestone had to be made by hand
// in the Jira UI. That is a limit of the MCP, not of Jira -- POST
// /rest/api/3/version is an ordinary endpoint -- and a manual step in the
// middle of an automated promotion defeats the automation it sits in: when
// OR-188 rolls unfinished tickets forward it needs the NEXT version to exist,
// unattended, at 3am (OR-190).
//
// "Release" means two different things in this repository and they must not
// be confused. A Jira version is a milestone, created here. Cutting the
// binary -- the tag, the Homebrew tap, the Scoop bucket -- is
// scripts/release.sh. Creating a version by mistake is untidy and
// reversible; publishing a release by mistake is neither, so the CLI never
// lets a bare verb reach the second one.

import (
	"encoding/json"
	"fmt"
	"strconv"
	"time"
)

// Version is one milestone on a project.
type Version struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Released    bool   `json:"released"`
	Archived    bool   `json:"archived"`
	ReleaseDate string `json:"releaseDate,omitempty"`
}

// ListVersions returns every version on a project, released or not.
//
// Needed for idempotence in CreateVersion, and worth having alone: "what is
// in this release" cannot be answered without first knowing the releases.
func (j *Jira) ListVersions(projectKey string) ([]Version, error) {
	code, body, err := j.do("GET", "/rest/api/3/project/"+projectKey+"/versions", nil)
	if err != nil {
		return nil, err
	}
	if code == 404 {
		return nil, fmt.Errorf("no such project: %s", projectKey)
	}
	if code >= 400 {
		return nil, fmt.Errorf("listing versions for %s: %d %s", projectKey, code, snippet(body))
	}
	var out []Version
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("listing versions for %s: %w", projectKey, err)
	}
	return out, nil
}

// FindVersion returns the version with this exact name, and whether it exists.
//
// Exact match, not a prefix: "v0.8" and "v0.8.1" are different milestones and
// resolving one to the other would file a ticket against the wrong release.
func (j *Jira) FindVersion(projectKey, name string) (Version, bool, error) {
	vs, err := j.ListVersions(projectKey)
	if err != nil {
		return Version{}, false, err
	}
	for _, v := range vs {
		if v.Name == name {
			return v, true, nil
		}
	}
	return Version{}, false, nil
}

// CreateVersion makes an unreleased version, and is IDEMPOTENT: creating one
// that already exists returns it with created=false rather than an error.
//
// Jira answers a duplicate name with a 400, and a command that errors on
// re-run cannot be called from automation -- which is the entire reason this
// exists. The promotion in OR-188 must be safe to retry after a partial
// failure without a human first checking what already happened.
func (j *Jira) CreateVersion(projectKey, name, description string) (Version, bool, error) {
	if existing, found, err := j.FindVersion(projectKey, name); err != nil {
		return Version{}, false, err
	} else if found {
		return existing, false, nil
	}

	_, projectID, err := j.ProjectExists(projectKey)
	if err != nil {
		return Version{}, false, err
	}
	if projectID == "" {
		return Version{}, false, fmt.Errorf("no such project: %s", projectKey)
	}
	// projectId is a NUMBER in this API. Sending the key, or the id as a
	// string, is a 400 whose message does not say which field was wrong.
	id, err := strconv.Atoi(projectID)
	if err != nil {
		return Version{}, false, fmt.Errorf("project %s has a non-numeric id %q", projectKey, projectID)
	}

	payload := map[string]any{
		"name":      name,
		"projectId": id,
		"released":  false,
	}
	if description != "" {
		payload["description"] = description
	}
	code, body, err := j.do("POST", "/rest/api/3/version", payload)
	if err != nil {
		return Version{}, false, err
	}
	if code == 403 {
		return Version{}, false, ErrNoPermission
	}
	// Lost a race with something else creating the same name. Still not an
	// error: the caller asked for the version to exist, and it does.
	if code == 400 {
		if existing, found, ferr := j.FindVersion(projectKey, name); ferr == nil && found {
			return existing, false, nil
		}
	}
	if code >= 400 {
		return Version{}, false, fmt.Errorf("creating version %s on %s: %d %s",
			name, projectKey, code, snippet(body))
	}
	var created Version
	if err := json.Unmarshal(body, &created); err != nil {
		return Version{}, false, err
	}
	return created, true, nil
}

// MarkReleased closes a version, dating it on the given day. An empty date
// means today.
//
// OR-188 needs to CLOSE a milestone at the end of a promotion, not only open
// one at the start; without this the version stays open forever and the next
// release has two candidates.
//
// The date is a parameter rather than always time.Now because a milestone is
// routinely closed AFTER the release it records: v0.8.0 shipped on the 29th
// and was closed on the 30th, and stamping the 30th would date the milestone
// to a day on which nothing was released (OR-209). The caller decides; the
// default stays today for the case where the two coincide.
func (j *Jira) MarkReleased(versionID, date string) error {
	if date == "" {
		date = time.Now().Format("2006-01-02")
	}
	payload := map[string]any{
		"released":    true,
		"releaseDate": date,
	}
	code, body, err := j.do("PUT", "/rest/api/3/version/"+versionID, payload)
	if err != nil {
		return err
	}
	if code == 403 {
		return ErrNoPermission
	}
	if code >= 400 {
		return fmt.Errorf("marking version %s released: %d %s", versionID, code, snippet(body))
	}
	return nil
}
