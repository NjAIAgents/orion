package tracker

import (
	"strconv"
	"strings"
)

// Building JQL, and why no caller may interpolate a value itself.
//
// JQL has reserved words -- AND, OR, NOT, EMPTY, NULL, ORDER and friends --
// and a value that collides with one is a PARSE ERROR, not a mismatch. A
// project keyed OR is legal in Jira and fatal here:
//
//	project = OR AND labels in (...)
//	  Error in JQL Query: Expecting either a value, list or function but
//	  got 'OR'. You must surround 'OR' in quotation marks...
//
// That was OR-120: `orion queue` interpolated the project key bare while
// `orion watch` happened to quote it, so the same project worked for one
// command and not the other. Per-call-site discipline is what produced that
// split, so these helpers emit the WHOLE clause -- field, operator and
// quoted values -- and no caller assembles one from a format string. The
// grep in jql_test.go enforces that: a JQL clause written by hand anywhere
// else fails the build.
//
// Quoting is strconv.Quote: JQL's quoted-string escapes (\" \\ \n \t \uXXXX)
// are the ones Go's syntax produces, so a key with a quote or a backslash in
// it survives rather than terminating the string early.

// JQLQuote renders one value as a JQL quoted string.
func JQLQuote(value string) string { return strconv.Quote(value) }

// JQLEq builds `field = "value"`.
func JQLEq(field, value string) string { return field + " = " + JQLQuote(value) }

// JQLIn builds `field IN ("a", "b")`.
func JQLIn(field string, values ...string) string { return jqlSet(field, "IN", values) }

// JQLNotIn builds `field NOT IN ("a", "b")`.
func JQLNotIn(field string, values ...string) string { return jqlSet(field, "NOT IN", values) }

func jqlSet(field, op string, values []string) string {
	quoted := make([]string, len(values))
	for i, v := range values {
		quoted[i] = JQLQuote(v)
	}
	return field + " " + op + " (" + strings.Join(quoted, ", ") + ")"
}

// JQLNotDone builds `statusCategory != "Done"`.
//
// The clause that keeps a resolved ticket out of the queue. A label is the
// claim criterion, and a label outlives the work: OR-119 was fixed by hand,
// merged and transitioned to Done with its ORION label still attached, so the
// next watch tick claimed it and paid an agent to re-investigate a fixed bug.
// The merged-branch guard does not cover that -- a hand fix lands on a branch
// Orion never named.
//
// statusCategory rather than status, because it is the one axis every Jira
// workflow shares: Done and Cancelled and any other custom terminal status a
// project invented all sit in the Done category, and enumerating status NAMES
// would need editing for each new one.
func JQLNotDone() string { return "statusCategory != " + JQLQuote(StatusCategoryDone) }

// JQLAnd joins clauses, dropping empty ones so an optional clause needs no
// bookkeeping at the call site.
func JQLAnd(clauses ...string) string {
	var kept []string
	for _, c := range clauses {
		if strings.TrimSpace(c) != "" {
			kept = append(kept, c)
		}
	}
	return strings.Join(kept, " AND ")
}
