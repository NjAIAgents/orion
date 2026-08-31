package tracker

import (
	"fmt"
	"net/url"
)

// Reading a milestone's ticket set, and putting a ticket into one.
//
// Every clause goes through the JQL builders rather than fmt.Sprintf. That is
// not style: OR is both a valid Jira project key and a JQL boolean operator,
// so an unquoted `project = OR` is a syntax error on the key this repository
// actually uses. jql.go owns that problem and a lint test enforces the rule.

// IssuesInVersion returns every ticket carrying this fixVersion.
func (j *Jira) IssuesInVersion(projectKey, version string) ([]Issue, error) {
	jql := JQLAnd(
		JQLEq("project", projectKey),
		JQLEq("fixVersion", version),
	) + " ORDER BY key ASC"
	return j.Search(jql, 100)
}

// IssuesWithoutVersion returns unresolved tickets carrying no fixVersion.
//
// Unresolved only: a project accumulates finished work that predates the
// milestone convention, and listing all of it would bury the handful of open
// tickets that genuinely fell through the gap -- which is the thing worth
// looking at before a release is cut.
func (j *Jira) IssuesWithoutVersion(projectKey string) ([]Issue, error) {
	jql := JQLAnd(
		JQLEq("project", projectKey),
		// IS EMPTY takes no value, so there is no builder for it and none is
		// needed: the reserved-word hazard is in the VALUE, and this clause
		// has none.
		"fixVersion IS EMPTY",
		JQLNotDone(),
	) + " ORDER BY key ASC"
	return j.Search(jql, 100)
}

// SetFixVersion puts a ticket on exactly this milestone, by version ID.
//
// REPLACES rather than appends, and that is the whole semantics `release add`
// reports as a MOVE: a ticket already carrying v0.8.2 leaves that milestone
// when it joins v0.8.3. Appending instead would leave it counted in both, and
// `release status` would then reconcile the same changelog fragment against
// two versions and report a mismatch on whichever one did not ship it
// (OR-222). A ticket that genuinely belongs to two milestones is not a case
// this command has ever needed, and inventing multi-version bookkeeping for it
// would make the common operation unreadable.
//
// The ID, not the name: FindVersion already resolved the name case-exactly and
// the ID cannot then be re-resolved to a near-miss on the way to the write.
func (j *Jira) SetFixVersion(key, versionID string) error {
	payload := map[string]any{
		"fields": map[string]any{
			"fixVersions": []any{map[string]any{"id": versionID}},
		},
	}
	code, body, err := j.do("PUT", "/rest/api/3/issue/"+url.PathEscape(key), payload)
	if err != nil {
		return err
	}
	if code == 403 {
		return ErrNoPermission
	}
	if code >= 400 {
		return fmt.Errorf("setting fixVersion on %s: %d %s", key, code, snippet(body))
	}
	return nil
}

// There is deliberately no Done() here. Issue.Resolved() already answers
// "is this finished?" against the coarse status CATEGORY, handles the
// per-project naming (Done, Closed, Cancelled, Won't Do), and treats an
// unknown category as not-finished. A second method meaning the same thing
// is how two answers to one question drift apart (OR-176).
