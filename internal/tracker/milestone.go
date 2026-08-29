package tracker

// Reading a milestone's ticket set.
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

// There is deliberately no Done() here. Issue.Resolved() already answers
// "is this finished?" against the coarse status CATEGORY, handles the
// per-project naming (Done, Closed, Cancelled, Won't Do), and treats an
// unknown category as not-finished. A second method meaning the same thing
// is how two answers to one question drift apart (OR-176).
