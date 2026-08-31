package tracker

import (
	"fmt"
	"strings"
)

// The second half of the claim criterion: a ticket is claimable only when it
// is BOTH labelled and scheduled.
//
// The two signals mean different things and neither implies the other. The
// queue label means "ready to be worked". A fixVersion means "scheduled to
// ship in a named release". Work that is ready but unscheduled is work nobody
// has decided to pay for yet, and Orion must not be the thing that decides
// (OR-221).
//
// This is not tidiness. Every piece of Orion's release machinery reconciles
// BY fixVersion: `release close` collates changelog fragments against a
// version (OR-187), its verify checks that every shipped ticket has a release
// note (OR-188), and `release status` reports what is in a milestone. A
// ticket worked with no version lands on the work branch invisible to all
// three -- its changelog fragment becomes an orphan, and the release it
// accidentally rides in cannot account for it. Both failures were observed on
// 2026-08-30, one in each direction: the concurrency-default change shipped
// with no ticket at all and OR-208 had to be filed retroactively to make the
// milestone reconcile, while OR-105 sat tagged to v0.8.1 with its work already
// shipped in v0.8.0.
//
// TWO CHOICES ARE MADE HERE RATHER THAN LEFT TO FALL OUT OF THE CODE.
//
// A CLOSED release is refused, not accepted. A ticket tagged to a version
// already marked released is scheduled for a train that has left: working it
// puts a changelog fragment against a milestone that has already been
// collated and dated, which is precisely the OR-105 failure. So "scheduled"
// means an OPEN milestone -- not released, not archived -- and a closed one
// is held back with its own reason rather than silently treated as no version
// at all.
//
// A PROJECT THAT DOES NOT USE VERSIONS IS UNAFFECTED. Orion adopts arbitrary
// repositories and FCIA is registered alongside OR; enforcing a convention a
// project never opted into would halt it completely. Enforcement is DETECTED
// (A5): a project with at least one open milestone is enforced, a project
// with none -- because it has no versions, or because every one it has is
// closed -- is queried exactly as before. The second half of that matters as
// much as the first: a project between releases has no open milestone to
// attach to, and halting it until somebody creates one would be a gate on the
// wrong thing.

// Schedules maps a project key to the milestone names a ticket in that
// project may be claimed against. A project absent from the map, or present
// with no names, is NOT enforced.
//
// Keys are upper-cased on the way in, because a project key reaches this from
// three places -- the registry (upper), orion.json (as typed) and a ticket key
// (as Jira returned it) -- and a lookup that missed on case alone would report
// a project as unenforced and claim exactly the work this is here to refuse.
type Schedules map[string][]string

// LoadSchedules reads each project's OPEN milestones.
//
// An error is returned rather than swallowed. Degrading to "unenforced" on a
// failed read would mean a network blip re-opens the gate and lets an
// unscheduled ticket be claimed -- and the watcher already treats a transient
// tracker error as "retry next tick", which is the correct response to not
// knowing.
func LoadSchedules(j *Jira, projectKeys []string) (Schedules, error) {
	out := Schedules{}
	for _, k := range projectKeys {
		k = strings.ToUpper(strings.TrimSpace(k))
		if k == "" || out[k] != nil {
			continue
		}
		vs, err := j.ListVersions(k)
		if err != nil {
			return nil, err
		}
		out[k] = OpenVersions(vs)
	}
	return out, nil
}

// OpenVersions are the milestone names a ticket may be scheduled against:
// not released, not archived.
func OpenVersions(vs []Version) []string {
	var out []string
	for _, v := range vs {
		if v.Released || v.Archived {
			continue
		}
		if n := strings.TrimSpace(v.Name); n != "" {
			out = append(out, n)
		}
	}
	return out
}

// Claimable returns the milestone names a project's tickets may carry. Nil
// means this project is not enforced.
func (s Schedules) Claimable(projectKey string) []string {
	return s[strings.ToUpper(strings.TrimSpace(projectKey))]
}

// Enforced reports whether any project in scope restricts claims at all.
func (s Schedules) Enforced(projectKeys []string) bool {
	for _, k := range projectKeys {
		if len(s.Claimable(k)) > 0 {
			return true
		}
	}
	return false
}

// Scope is the project clause of the CLAIM query: which projects, and within
// an enforced one, which milestones.
//
// Built into the JQL rather than filtered after the fetch, which is the
// difference between a ticket that never enters the candidate set and one
// that is fetched, ranked, raced for and only then discarded.
func (s Schedules) Scope(projectKeys []string) string {
	if !s.Enforced(projectKeys) {
		return JQLIn("project", projectKeys...)
	}
	groups := make([]string, 0, len(projectKeys))
	for _, k := range projectKeys {
		if vs := s.Claimable(k); len(vs) > 0 {
			groups = append(groups, JQLAnd(JQLEq("project", k), JQLIn("fixVersion", vs...)))
			continue
		}
		groups = append(groups, JQLEq("project", k))
	}
	return JQLOr(groups...)
}

// HeldScope is the exact inverse of Scope over the ENFORCED projects: the
// labelled tickets this gate is keeping back.
//
// It exists so the queue can say WHY rather than silently omit them. A ticket
// that never runs and is never mentioned is how somebody spends an afternoon
// wondering whether the watcher is broken; route.go already holds the same
// line, that the default must never be silent.
//
// `fixVersion NOT IN (...)` alone would not do it: in Jira a NOT IN excludes
// rows where the field is empty, so the very case this ticket is about --
// no fixVersion at all -- would fall out of both queries and be reported by
// neither. IS EMPTY takes no value, so no builder is needed and the
// reserved-word hazard, which lives in the VALUE, does not apply.
func (s Schedules) HeldScope(projectKeys []string) string {
	var groups []string
	for _, k := range projectKeys {
		vs := s.Claimable(k)
		if len(vs) == 0 {
			continue
		}
		groups = append(groups, JQLAnd(
			JQLEq("project", k),
			JQLOr(JQLNotIn("fixVersion", vs...), "fixVersion IS EMPTY"),
		))
	}
	return JQLOr(groups...)
}

// Hold reasons, as whole sentences rather than codes.
//
// The SAME sentence in `orion queue` and in the watcher, and deliberately
// free of the ticket's own version names: an operator who saw one recognises
// the other, and the watcher can group every ticket held for one reason onto
// a single line instead of printing one line per ticket on every tick.
const (
	holdUnscheduled = "labelled %s but not attached to a release, so it will not be claimed"
	holdClosed      = "labelled %s but attached only to a release that has already closed, " +
		"so it will not be claimed"
)

// HoldReason says why the queue will not claim this labelled ticket, or ""
// when it will claim it.
//
// Answered from the issue's own fixVersions against its project's open
// milestones, so it agrees with Scope by construction rather than by a second
// reading of the same rule.
func (s Schedules) HoldReason(i Issue, label string) string {
	claimable := s.Claimable(ProjectOf(i.Key))
	if len(claimable) == 0 {
		return "" // this project does not use releases; nothing is enforced
	}
	if label == "" {
		label = QueueLabelDefault
	}
	for _, v := range i.FixVersions {
		for _, c := range claimable {
			if v == c {
				return ""
			}
		}
	}
	if len(i.FixVersions) == 0 {
		return fmt.Sprintf(holdUnscheduled, label)
	}
	return fmt.Sprintf(holdClosed, label)
}

// ProjectOf is the project key an issue key belongs to, upper-cased. Empty
// for anything that is not a KEY-123.
//
// A ticket carries its project in its key and nothing else on Issue does, so
// this is how a per-project rule is applied to an issue that came back from a
// query spanning several.
func ProjectOf(issueKey string) string {
	k := strings.ToUpper(strings.TrimSpace(issueKey))
	if i := strings.LastIndex(k, "-"); i > 0 {
		return k[:i]
	}
	return ""
}
