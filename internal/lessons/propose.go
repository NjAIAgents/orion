package lessons

// Proposing lessons automatically.
//
// The store was designed for cross-project memory and then never written to:
// every writer was a command a human had to remember to type, so nothing ever
// counted to two and nothing was ever lifted. The machinery downstream --
// ranking, expiry, promotion -- all worked, on an empty list.
//
// This file is the missing half. Something in a run OBSERVES a moment where a
// mistake is already visible to the system, and files a proposal. Nothing here
// writes a lesson: proposals live in their own log and only reach lessons.jsonl
// when a person approves one.
//
// Two rules keep the store from filling with noise, and both are the point:
//
//  1. Propose, never auto-write. The store is append-only and injected into
//     every session's CLAUDE.md, so a wrong lesson is durable and re-read
//     forever. An agent free to record what it believes would spend the
//     rendered budget on restatements of one-off environment noise.
//
//  2. Two strikes. Once is circumstance. The same signature observed twice is
//     a pattern, and only then is it worth a human's attention. The rule
//     already governed cross-project promotion; it governs recording too, so
//     the reviewer is never asked about a one-off.

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Strikes is how many times the same lesson must be observed before it is
// offered for approval.
const Strikes = 2

// Decision is a human's answer to a proposal. An empty decision means the
// entry is a sighting rather than an answer.
type Decision string

const (
	DecisionApproved Decision = "approved"
	DecisionRejected Decision = "rejected"
)

// Proposal is one line of the proposals log: either a sighting of a
// lesson-worthy event, or a human's decision about one.
//
// Grounding is enforced, not encouraged. A sighting that cannot say what
// happened, in which project, on what date is rejected at the door -- that is
// the difference between a recorded observation and an agent's opinion.
type Proposal struct {
	Signature string    `json:"sig"`
	Text      string    `json:"text"`
	Kind      Kind      `json:"kind,omitempty"`
	Project   string    `json:"project"`
	Stack     string    `json:"stack,omitempty"`
	Evidence  string    `json:"evidence,omitempty"`
	At        time.Time `json:"at"`
	Decision  Decision  `json:"decision,omitempty"`
	By        string    `json:"by,omitempty"`
}

// Candidate is the reduced view of every sighting sharing a signature.
type Candidate struct {
	Signature string
	Text      string
	Kind      Kind
	Stack     string
	Strikes   int
	Projects  []string
	Evidence  []string
	Decision  Decision
	FirstSeen time.Time
	LastSeen  time.Time
}

// Ready reports whether the two-strike rule has been met and no one has
// answered yet.
func (c Candidate) Ready() bool { return c.Decision == "" && c.Strikes >= Strikes }

func (s *Store) proposalsPath() string { return filepath.Join(s.dir, "proposals.jsonl") }

// DetectStack identifies a project's ecosystem from its manifest, which is
// what decides whether a stack-scoped lesson applies there. Lives here rather
// than in the CLI because both the session hook and the automatic proposal
// path need the same answer, and two copies would drift.
func DetectStack(root string) string {
	if root == "" {
		return ""
	}
	for file, stack := range map[string]string{
		"go.mod": "go", "package.json": "node", "Cargo.toml": "rust",
		"pyproject.toml": "python", "requirements.txt": "python",
		"pom.xml": "java", "build.gradle": "java", "Gemfile": "ruby",
	} {
		if _, err := os.Stat(filepath.Join(root, file)); err == nil {
			return stack
		}
	}
	return ""
}

// Observe records one sighting of a lesson-worthy event and returns the
// candidate it belongs to, so the caller can tell whether this sighting is the
// one that crossed the two-strike line.
//
// Idempotent per event, not per signature: the caller is responsible for
// observing once per real occurrence. Observing the same merge twice would
// manufacture a second strike out of one event, which is the failure this
// whole design is trying to avoid.
func (s *Store) Observe(p Proposal) (Candidate, error) {
	if strings.TrimSpace(p.Text) == "" {
		return Candidate{}, errors.New("a proposed lesson needs text saying what happened")
	}
	if strings.TrimSpace(p.Project) == "" {
		return Candidate{}, errors.New("a proposed lesson needs the project it happened in")
	}
	if strings.TrimSpace(p.Evidence) == "" {
		return Candidate{}, errors.New("a proposed lesson needs evidence of the event")
	}
	p.Decision = ""
	if p.At.IsZero() {
		p.At = time.Now().UTC()
	}
	if p.Signature == "" {
		p.Signature = Signature(p.Text)
	}
	if err := s.appendProposal(p); err != nil {
		return Candidate{}, err
	}
	cs, err := s.Candidates()
	if err != nil {
		return Candidate{}, err
	}
	for _, c := range cs {
		if c.Signature == p.Signature {
			return c, nil
		}
	}
	return Candidate{}, nil
}

// Candidates reduces the proposals log, newest sighting first.
func (s *Store) Candidates() ([]Candidate, error) {
	entries, err := s.readProposals()
	if err != nil {
		return nil, err
	}
	byS := map[string]*Candidate{}
	var order []string
	for _, p := range entries {
		c, ok := byS[p.Signature]
		if !ok {
			c = &Candidate{Signature: p.Signature, Text: p.Text, Kind: p.Kind, FirstSeen: p.At}
			byS[p.Signature] = c
			order = append(order, p.Signature)
		}
		if p.Decision != "" {
			c.Decision = p.Decision
			continue
		}
		c.Strikes++
		c.Text = p.Text
		if p.Kind != "" {
			c.Kind = p.Kind
		}
		if p.Stack != "" {
			c.Stack = p.Stack
		}
		if !contains(c.Projects, p.Project) {
			c.Projects = append(c.Projects, p.Project)
		}
		c.Evidence = append(c.Evidence, p.Evidence)
		if p.At.After(c.LastSeen) {
			c.LastSeen = p.At
		}
	}
	out := make([]Candidate, 0, len(order))
	for _, sig := range order {
		out = append(out, *byS[sig])
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].LastSeen.After(out[j].LastSeen) })
	return out, nil
}

// Pending is the candidates a human should be asked about: two strikes and no
// answer yet.
func (s *Store) Pending() ([]Candidate, error) {
	cs, err := s.Candidates()
	if err != nil {
		return nil, err
	}
	var out []Candidate
	for _, c := range cs {
		if c.Ready() {
			out = append(out, c)
		}
	}
	return out, nil
}

// Decide answers a pending proposal. Approving is the ONLY automatic path into
// lessons.jsonl, and it exists so a durable, every-session rule is never
// written without someone saying yes to it.
//
// A candidate below the strike threshold cannot be approved. That is the
// two-strike rule governing recording rather than only promotion: if the
// reviewer could wave a one-off through, the rule would be advisory.
func (s *Store) Decide(sig string, d Decision, by string) (Candidate, error) {
	if d != DecisionApproved && d != DecisionRejected {
		return Candidate{}, fmt.Errorf("unknown decision %q", d)
	}
	cs, err := s.Candidates()
	if err != nil {
		return Candidate{}, err
	}
	var found *Candidate
	for i := range cs {
		if cs[i].Signature == sig {
			found = &cs[i]
			break
		}
	}
	if found == nil {
		return Candidate{}, fmt.Errorf("no proposal with signature %s", sig)
	}
	if found.Decision != "" {
		return Candidate{}, fmt.Errorf("that proposal was already %s", found.Decision)
	}
	if found.Strikes < Strikes {
		return Candidate{}, fmt.Errorf(
			"seen %d time(s); a lesson is only offered after %d, because once may be circumstance",
			found.Strikes, Strikes)
	}
	now := time.Now().UTC()
	if d == DecisionApproved {
		// One lesson per project it was seen in, so the record carries the
		// same recurrence the proposal was built from -- and so the existing
		// cross-project promotion has the projects it needs to reason about.
		for _, project := range found.Projects {
			if err := s.Append(Lesson{
				Signature: found.Signature,
				Text:      found.Text,
				Kind:      found.Kind,
				Stack:     found.Stack,
				Project:   project,
				Evidence:  strings.Join(found.Evidence, "; "),
				At:        now,
			}); err != nil {
				return Candidate{}, err
			}
		}
	}
	if err := s.appendProposal(Proposal{
		Signature: found.Signature, Text: found.Text, Project: "review",
		At: now, Decision: d, By: by,
	}); err != nil {
		return Candidate{}, err
	}
	found.Decision = d
	return *found, nil
}

// Health answers the question an empty `orion lessons list` cannot: is this
// store empty because nothing worth recording has happened, or because nothing
// is writing to it?
//
// Those two look identical from the outside, and for the whole life of this
// package it was the second one. A subsystem that reports cleanly while doing
// nothing is worse than one that fails, because nobody goes looking.
type Health struct {
	Sightings    int
	Decisions    int
	Pending      int
	LastObserved time.Time
}

// Observed reports whether any automatic path has ever filed a sighting.
func (h Health) Observed() bool { return h.Sightings > 0 }

func (s *Store) Health() (Health, error) {
	entries, err := s.readProposals()
	if err != nil {
		return Health{}, err
	}
	var h Health
	for _, p := range entries {
		if p.Decision != "" {
			h.Decisions++
			continue
		}
		h.Sightings++
		if p.At.After(h.LastObserved) {
			h.LastObserved = p.At
		}
	}
	pending, err := s.Pending()
	if err != nil {
		return Health{}, err
	}
	h.Pending = len(pending)
	return h, nil
}

func (s *Store) appendProposal(p Proposal) error {
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return err
	}
	// Same 0600 as the lesson log: a proposal quotes real build output.
	f, err := os.OpenFile(s.proposalsPath(), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	b, err := json.Marshal(p)
	if err != nil {
		return err
	}
	_, err = f.Write(append(b, '\n'))
	return err
}

func (s *Store) readProposals() ([]Proposal, error) {
	f, err := os.Open(s.proposalsPath())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()

	var out []Proposal
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var p Proposal
		if json.Unmarshal([]byte(line), &p) != nil {
			continue // a corrupt line must not lose the rest of the log
		}
		out = append(out, p)
	}
	return out, sc.Err()
}
