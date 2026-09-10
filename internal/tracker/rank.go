package tracker

// Rank is the queue's ORDER BY, so writing it is how the order changes.
//
// `orion queue` and the watcher both read "priority DESC, Rank ASC": priority
// says how important a ticket is, Rank says what goes first among the ones
// that are equally important. Until now Orion could only READ that order --
// the one way to change it was to drag a ticket in Jira's backlog, which is
// not something a web UI shelling out to this CLI can do (OR-280).
//
// THE AGILE API, NOT AN ISSUE EDIT. Rank is a custom field whose value is a
// LexoRank string; setting it through /rest/api/3/issue would mean computing
// a rank between two neighbours by hand, and a wrong one silently reorders
// the whole backlog. /rest/agile/1.0/issue/rank takes the neighbour instead
// and lets Jira compute the value, which is the same operation the drag
// performs.

import (
	"fmt"
)

// RankAfter puts key immediately after target in the tracker's backlog order.
//
// One issue per call rather than the batch the endpoint also accepts: a batch
// preserves the caller's order only as an undocumented side effect, and an
// ordering written on a guess about that is an ordering nobody can check.
// Pairwise calls say exactly what they mean and fail one ticket at a time.
func (j *Jira) RankAfter(key, target string) error {
	payload := map[string]any{
		"issues":         []string{key},
		"rankAfterIssue": target,
	}
	code, body, err := j.do("PUT", "/rest/agile/1.0/issue/rank", payload)
	if err != nil {
		return err
	}
	switch {
	case code == 404:
		// The agile API is a Jira SOFTWARE surface. On a project without it
		// this 404s, and "404 ranking OR-1" reads as a missing ticket -- so
		// name the other possibility, because it is the one the operator
		// cannot fix by correcting a key.
		return fmt.Errorf("ranking %s after %s: %d %s\n"+
			"  Either one of those tickets does not exist, or this site has no\n"+
			"  Jira Software backlog, which is what holds the Rank the queue orders by.",
			key, target, code, snippet(body))
	case code == 207:
		// Multi-status: the request was accepted and this issue was not
		// ranked. Success and failure share a 2xx, so it must be read.
		return fmt.Errorf("ranking %s after %s: the tracker refused it: %s",
			key, target, snippet(body))
	case code >= 300:
		return fmt.Errorf("ranking %s after %s: %d %s", key, target, code, snippet(body))
	}
	return nil
}
