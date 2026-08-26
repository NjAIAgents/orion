// Package lessons is Orion's cross-project memory.
//
// The playbook's two-strike rule says a mistake seen twice becomes a line
// in CLAUDE.md. That rule is per-repo, so the same mistake gets re-learned
// in every new project. This package lifts the store to $ORION_HOME and
// shares it across every workspace.
//
// Two failure modes govern the design, and both are easy to walk into:
//
//  1. Over-generalization. "Money is BigDecimal here" is true of one repo
//     and wrong advice everywhere else. So a lesson starts project-scoped
//     and is promoted only when it actually recurs in a DIFFERENT project.
//     Promotion is evidence, never inference.
//
//  2. Context bloat. The agent reads CLAUDE.md in full every session, so
//     an unbounded lesson list degrades every session slightly and
//     invisibly. The rendered block is capped, ranked and expiring.
//
// The store is append-only JSONL. Nothing is ever edited in place, so the
// provenance of a rule the agent is following can always be reconstructed:
// what happened, in which project, on which date.
package lessons

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Scope controls which projects a lesson applies to.
type Scope string

const (
	ScopeProject Scope = "project" // this project only
	ScopeStack   Scope = "stack"   // any project with the same stack
	ScopeGlobal  Scope = "global"  // everywhere
)

// Kind records where a lesson came from, which determines how much
// weight it earns. A human correction is worth more than an inferred one.
type Kind string

const (
	KindCorrection Kind = "correction" // a human said "you got this wrong"
	KindBreaker    Kind = "breaker"    // a circuit breaker tripped
	KindReview     Kind = "review"     // a review finding recurred
	KindIncident   Kind = "incident"   // a production incident
)

// Lesson is one durable record. Records are never mutated; a change is a
// new record with the same Signature, and the reducer takes the latest.
type Lesson struct {
	Signature string    `json:"sig"`
	Text      string    `json:"text"`
	Kind      Kind      `json:"kind"`
	Scope     Scope     `json:"scope"`
	Stack     string    `json:"stack,omitempty"`
	Project   string    `json:"project"`
	Evidence  string    `json:"evidence,omitempty"`
	At        time.Time `json:"at"`
	Retired   bool      `json:"retired,omitempty"`
}

// Record is the reduced view of every entry sharing a signature.
type Record struct {
	Lesson
	Hits     int       `json:"hits"`
	Projects []string  `json:"projects"`
	LastSeen time.Time `json:"last_seen"`
}

const (
	// MaxRendered caps the injected block. The number is a judgement call:
	// large enough to be useful, small enough that CLAUDE.md stays roughly
	// a page as the playbook requires.
	MaxRendered = 25
	// Expiry retires a lesson that has not recurred. A rule nothing has
	// tripped in three months is either fixed or wrong.
	Expiry = 90 * 24 * time.Hour

	beginMarker = "<!-- orion:lessons:begin -->"
	endMarker   = "<!-- orion:lessons:end -->"
)

type Store struct{ dir string }

func New(home string) *Store { return &Store{dir: filepath.Join(home, "lessons")} }

func (s *Store) logPath() string { return filepath.Join(s.dir, "lessons.jsonl") }

// Append writes one lesson. Signature is derived from the text so the
// same lesson phrased identically collapses into one record; near
// duplicates are the human reviewer's problem, handled by `orion lessons`.
func (s *Store) Append(l Lesson) error {
	if strings.TrimSpace(l.Text) == "" {
		return errors.New("a lesson needs text")
	}
	if l.At.IsZero() {
		l.At = time.Now().UTC()
	}
	if l.Scope == "" {
		// Always start narrow. Anything else assumes a generality that has
		// not been demonstrated yet.
		l.Scope = ScopeProject
	}
	if l.Signature == "" {
		l.Signature = Signature(l.Text)
	}
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(s.logPath(), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	b, err := json.Marshal(l)
	if err != nil {
		return err
	}
	_, err = f.Write(append(b, '\n'))
	return err
}

// Load reduces the append-only log into current records.
func (s *Store) Load() ([]Record, error) {
	f, err := os.Open(s.logPath())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()

	byS := map[string]*Record{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var l Lesson
		if json.Unmarshal([]byte(line), &l) != nil {
			continue // a corrupt line must not lose the rest of the log
		}
		r, ok := byS[l.Signature]
		if !ok {
			r = &Record{Lesson: l}
			byS[l.Signature] = r
		}
		r.Hits++
		r.Lesson.Text = l.Text
		r.Lesson.Kind = l.Kind
		r.Lesson.Retired = l.Retired
		if l.Scope != "" && scopeRank(l.Scope) > scopeRank(r.Lesson.Scope) {
			r.Lesson.Scope = l.Scope
			r.Lesson.Stack = l.Stack
		}
		if l.At.After(r.LastSeen) {
			r.LastSeen = l.At
		}
		if l.Project != "" && !contains(r.Projects, l.Project) {
			r.Projects = append(r.Projects, l.Project)
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}

	out := make([]Record, 0, len(byS))
	for _, r := range byS {
		out = append(out, *r)
	}
	return out, nil
}

// AutoPromote raises scope when the evidence supports it, and returns the
// promotions so they can be logged and reviewed.
//
// The rule is deliberately strict. Recurring twice inside one project
// proves the lesson matters there; it proves nothing about anywhere else.
// Only recurrence in a second, distinct project is evidence of generality.
func (s *Store) AutoPromote(records []Record) []Record {
	var promoted []Record
	for _, r := range records {
		if r.Retired || len(r.Projects) < 2 {
			continue
		}
		want := ScopeStack
		if len(r.Projects) >= 3 || r.Stack == "" {
			want = ScopeGlobal
		}
		if scopeRank(want) <= scopeRank(r.Scope) {
			continue
		}
		p := r
		p.Scope = want
		p.At = time.Now().UTC()
		p.Evidence = fmt.Sprintf("recurred in %d projects: %s", len(r.Projects), strings.Join(r.Projects, ", "))
		promoted = append(promoted, p)
	}
	return promoted
}

// Applicable filters records to those that should reach a given project.
func Applicable(records []Record, projectID, stack string) []Record {
	now := time.Now()
	var out []Record
	for _, r := range records {
		if r.Retired || now.Sub(r.LastSeen) > Expiry {
			continue
		}
		switch r.Scope {
		case ScopeGlobal:
			out = append(out, r)
		case ScopeStack:
			if stack != "" && strings.EqualFold(r.Stack, stack) {
				out = append(out, r)
			}
		case ScopeProject:
			if contains(r.Projects, projectID) {
				out = append(out, r)
			}
		}
	}
	rank(out)
	if len(out) > MaxRendered {
		out = out[:MaxRendered]
	}
	return out
}

// rank orders by recurrence weighted toward the recent. A lesson that
// fired ten times two years ago should not outrank one that fired twice
// last week, because the codebase it described is gone.
func rank(rs []Record) {
	now := time.Now()
	score := func(r Record) float64 {
		ageDays := now.Sub(r.LastSeen).Hours() / 24
		decay := 1.0 / (1.0 + ageDays/30.0)
		// Recurrence counts with diminishing returns. Linear Hits let an
		// ancient lesson with a big count outrank a fresh human correction:
		// 20 hits at 80 days beat 3 hits today, even though the codebase the
		// old one described may not exist any more. The 20th recurrence is
		// not twenty times more informative than the first.
		w := math.Log1p(float64(r.Hits)) * decay
		if r.Kind == KindCorrection || r.Kind == KindIncident {
			w *= 1.5 // a human correction and a real incident outrank a heuristic
		}
		return w
	}
	sort.SliceStable(rs, func(i, j int) bool { return score(rs[i]) > score(rs[j]) })
}

// Render produces the managed CLAUDE.md block.
func Render(rs []Record) string {
	if len(rs) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString(beginMarker + "\n")
	b.WriteString("## Lessons Orion has already learned\n\n")
	b.WriteString("Corrections carried over from previous work. Do not relearn these.\n\n")
	for _, r := range rs {
		b.WriteString("- " + strings.TrimSpace(r.Text))
		if r.Hits > 1 {
			b.WriteString(fmt.Sprintf(" _(seen %dx)_", r.Hits))
		}
		b.WriteString("\n")
	}
	b.WriteString("\n" + endMarker + "\n")
	return b.String()
}

// Inject writes the rendered block into a CLAUDE.md, replacing any
// previous block and leaving every hand-written line outside it intact.
// Editing the whole file would silently destroy the team's own notes,
// which is exactly the kind of quiet damage that makes people distrust a
// tool and turn it off.
func Inject(claudeMDPath, block string) error {
	existing, err := os.ReadFile(claudeMDPath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	content := string(existing)

	start := strings.Index(content, beginMarker)
	end := strings.Index(content, endMarker)

	switch {
	case start >= 0 && end > start:
		content = content[:start] + block + content[end+len(endMarker)+1:]
	case block == "":
		return nil
	case content == "":
		content = block
	default:
		if !strings.HasSuffix(content, "\n") {
			content += "\n"
		}
		content += "\n" + block
	}
	if strings.TrimSpace(content) == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(claudeMDPath), 0o755); err != nil {
		return err
	}
	return os.WriteFile(claudeMDPath, []byte(content), 0o644)
}

// Signature normalizes a lesson to a dedup key: lowercase, collapsed
// whitespace, trailing punctuation removed.
func Signature(text string) string {
	t := strings.ToLower(strings.TrimSpace(text))
	t = strings.Join(strings.Fields(t), " ")
	t = strings.TrimRight(t, ".!;: ")
	return fmt.Sprintf("%016x", fnv1a(t))
}

func fnv1a(s string) uint64 {
	var h uint64 = 14695981039346656037
	for i := 0; i < len(s); i++ {
		h ^= uint64(s[i])
		h *= 1099511628211
	}
	return h
}

func scopeRank(s Scope) int {
	switch s {
	case ScopeGlobal:
		return 3
	case ScopeStack:
		return 2
	case ScopeProject:
		return 1
	}
	return 0
}

func contains(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}
