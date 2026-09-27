package actors

// One person per ticket within a role (OR-544 G).
//
// With four tickets in flight the implementer role was four agents that all
// rendered as the same name, so "<name> edited store.py" said nothing about
// which ticket it meant without reading the key column. Each ticket now gets
// its own teammate from a small roster per role, kept for the ticket's whole
// life -- implementation, every fix round, retries -- which matches OR-171:
// a fix round resumes the same actor's session, so the same person fixes what
// they wrote.
//
// Names are labels only. The role decides the prompt and the model; nothing
// here changes what runs.

import (
	"fmt"
	"strings"
	"sync"
)

// defaultTeams are the teammates after the role's own name, for the roles
// that run in parallel. Distinct from every name in defaults() and from each
// other, so no two people in one log share an initial silhouette.
var defaultTeams = map[string][]string{
	"implementer": {"Divya", "Omar", "Leo"},
	"frontend":    {"Lena", "Tomas"},
	"qa":          {"Chen", "Maya"},
	"docs":        {"Noor"},
	"devops":      {"Felix"},
}

var assigned struct {
	mu    sync.Mutex
	on    bool
	byKey map[string]map[string]string // ticket -> actor id -> name
}

// EnableTeams turns per-ticket names on. A watcher does, because it runs
// tickets side by side; a single `orion work` run has one ticket and keeps
// the role's own name.
func EnableTeams() {
	assigned.mu.Lock()
	assigned.on = true
	assigned.mu.Unlock()
}

func teamsOn() bool {
	assigned.mu.Lock()
	defer assigned.mu.Unlock()
	return assigned.on
}

// team is the role's roster: its configured or default name first, then its
// teammates, without repeats.
func team(id string, a Actor) []string {
	seen := map[string]bool{}
	var out []string
	for _, n := range append([]string{a.Name}, defaultTeams[id]...) {
		if n != "" && !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	return out
}

// nameFor is the person working this ticket in this role, assigned on first
// use: the first teammate no other in-flight ticket holds, or the role's name
// numbered when the roster is exhausted.
func nameFor(key, id string, a Actor) string {
	assigned.mu.Lock()
	defer assigned.mu.Unlock()
	if assigned.byKey == nil {
		assigned.byKey = map[string]map[string]string{}
	}
	if n := assigned.byKey[key][id]; n != "" {
		return n
	}
	used := map[string]bool{}
	for k, roles := range assigned.byKey {
		if k != key && roles[id] != "" {
			used[roles[id]] = true
		}
	}
	name := ""
	for _, n := range team(id, a) {
		if !used[n] {
			name = n
			break
		}
	}
	for i := 2; name == ""; i++ {
		if c := fmt.Sprintf("%s #%d", a.Name, i); !used[c] {
			name = c
		}
	}
	if assigned.byKey[key] == nil {
		assigned.byKey[key] = map[string]string{}
	}
	assigned.byKey[key][id] = name
	return name
}

// Release frees every name a ticket held, when it leaves the watcher.
func Release(key string) {
	assigned.mu.Lock()
	delete(assigned.byKey, key)
	assigned.mu.Unlock()
}

// DisplayFor is Display for one ticket: that ticket's teammate in the role.
// No key, or a nameless actor (orion, ci), is Display unchanged.
func DisplayFor(key, id string) string {
	a := Get(id)
	if strings.TrimSpace(key) == "" || a.Name == "" || !teamsOn() {
		return a.Display()
	}
	return Actor{Name: nameFor(key, id, a), Designation: a.Designation}.Display()
}

// AttributionFor is Attribution for one ticket.
func AttributionFor(key, id string) string {
	a := Get(id)
	if strings.TrimSpace(key) == "" || a.Name == "" || !teamsOn() {
		return Attribution(id)
	}
	return nameFor(key, id, a) + Separator + a.Designation + ", an Orion agent"
}

// CommentFor is Comment for one ticket.
func CommentFor(key, id, body string) string {
	return AttributionFor(key, id) + ":\n\n" + strings.TrimSpace(body)
}

// ResetTeams turns per-ticket names off and forgets every assignment. For
// tests, and anything else that has to leave the process as it found it.
func ResetTeams() { resetAssigned() }

// resetAssigned clears assignments, for tests.
func resetAssigned() {
	assigned.mu.Lock()
	assigned.byKey, assigned.on = nil, false
	assigned.mu.Unlock()
}
