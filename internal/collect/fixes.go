package collect

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Tracking CI fix attempts.
//
// The loop this bounds: CI fails, Orion sends the failure back to an agent on
// the same branch, the agent pushes a fix, CI runs again. Left unbounded that
// is a machine that spends money forever on a problem it cannot solve, and
// the failure mode is not a crash -- it is a quiet, expensive, all-night
// oscillation between two broken states.
//
// Two independent brakes, because a count alone is not enough:
//
//   - a hard attempt ceiling, which catches slow divergence
//   - a repeated-failure check, which catches a stuck agent IMMEDIATELY
//
// The second matters more in practice. An agent that pushes a "fix" and gets
// back a byte-identical failure has learned nothing, and letting it burn the
// remaining attempts proves only that it will fail the same way three times.

// Attempt is one round of the fix loop.
type Attempt struct {
	At          time.Time `json:"at"`
	Fingerprint string    `json:"fingerprint"`
	Detail      string    `json:"detail"`
	// RootCause is the fix run's own diagnosis -- the first line of its
	// closing message, which OR-157 requires it to lead with -- set only
	// once the run actually pushed a fix. Empty for a round that never
	// stated one: an older run, or one that gave up.
	RootCause string `json:"root_cause,omitempty"`
}

// FixState is the history for one ticket.
type FixState struct {
	Key      string    `json:"key"`
	Branch   string    `json:"branch"`
	Attempts []Attempt `json:"attempts"`
}

// Count is how many fix runs have been spent.
func (s FixState) Count() int { return len(s.Attempts) }

// Repeating reports whether this failure is identical to the last one.
//
// Compared on a fingerprint of the NORMALISED failure text: raw CI output
// carries timestamps, durations and run ids that differ every time, so a
// literal comparison would never match and this brake would never engage.
func (s FixState) Repeating(fingerprint string) bool {
	if len(s.Attempts) == 0 {
		return false
	}
	return s.Attempts[len(s.Attempts)-1].Fingerprint == fingerprint
}

// Fingerprint reduces CI output to what identifies the failure.
//
// Drops anything that varies between otherwise identical runs. Getting this
// wrong in either direction is costly: too loose and two different failures
// look the same, so the loop stops while it was still making progress; too
// strict and it never stops early at all.
func Fingerprint(detail string) string {
	var keep []string
	for _, line := range strings.Split(detail, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// Timing and identifiers, which change on every run.
		if strings.Contains(line, "seconds") || strings.Contains(line, "elapsed") ||
			strings.Contains(line, "http") || strings.Contains(line, "/runs/") {
			continue
		}
		// Leading timestamps, as GitHub Actions logs prefix every line.
		if len(line) > 20 && line[4] == '-' && line[7] == '-' {
			if i := strings.IndexByte(line, ' '); i > 0 && i < 32 {
				line = strings.TrimSpace(line[i:])
			}
		}
		keep = append(keep, line)
	}
	sum := sha256.Sum256([]byte(strings.Join(keep, "\n")))
	return hex.EncodeToString(sum[:8])
}

type fixFile struct {
	Version int                 `json:"version"`
	States  map[string]FixState `json:"states"`
}

func fixPath(wsDir string) string {
	return filepath.Join(wsDir, ".orion", "ci-fixes.json")
}

func loadFixes(wsDir string) fixFile {
	f := fixFile{Version: 1, States: map[string]FixState{}}
	b, err := os.ReadFile(fixPath(wsDir))
	if err != nil {
		return f
	}
	if json.Unmarshal(b, &f) != nil || f.States == nil {
		return fixFile{Version: 1, States: map[string]FixState{}}
	}
	return f
}

// recordAttempt appends one attempt and returns the updated state.
//
// Written BEFORE the fix run, not after. A crash mid-run would otherwise
// leave the attempt uncounted, and the ceiling that exists to bound spending
// would reset every time the process died -- which is exactly the condition
// under which a runaway loop is most likely.
func recordAttempt(wsDir, key, branch, fingerprint, detail string) (FixState, error) {
	f := loadFixes(wsDir)
	s := f.States[key]
	s.Key, s.Branch = key, branch
	s.Attempts = append(s.Attempts, Attempt{
		At: time.Now().UTC(), Fingerprint: fingerprint, Detail: firstLine(detail),
	})
	f.States[key] = s
	return s, writeFixes(wsDir, f)
}

// recordRootCause attaches the fix run's stated diagnosis to the attempt
// that just pushed. Attempts are written BEFORE the run starts (see
// recordAttempt, and why), so this fills in the one field that only exists
// once the run has finished -- and only when it actually stated one.
func recordRootCause(wsDir, key, rootCause string) error {
	if strings.TrimSpace(rootCause) == "" {
		return nil
	}
	f := loadFixes(wsDir)
	s, ok := f.States[key]
	if !ok || len(s.Attempts) == 0 {
		return nil
	}
	s.Attempts[len(s.Attempts)-1].RootCause = rootCause
	f.States[key] = s
	return writeFixes(wsDir, f)
}

// clearFixes forgets a ticket's history, once it has merged or been given up
// on. Without this a ticket reopened months later would start with its
// attempts already spent.
func clearFixes(wsDir, key string) error {
	f := loadFixes(wsDir)
	if _, ok := f.States[key]; !ok {
		return nil
	}
	delete(f.States, key)
	return writeFixes(wsDir, f)
}

// retractAttempt undoes the single most recent attempt for a ticket.
//
// recordAttempt is deliberately written BEFORE the fix run, so a crash mid-run
// still spends the attempt. A policy denial is not a crash: the run finished
// cleanly and reported, with certainty, that the sandbox itself refused the
// only edit the agent tried. That proves nothing about the agent, so it must
// not spend part of a three-attempt budget meant to measure the agent (OR-174).
//
// Only the LAST attempt is removed, not the whole history -- a prior genuine
// attempt on this ticket still counts.
func retractAttempt(wsDir, key string) error {
	f := loadFixes(wsDir)
	s, ok := f.States[key]
	if !ok || len(s.Attempts) == 0 {
		return nil
	}
	s.Attempts = s.Attempts[:len(s.Attempts)-1]
	f.States[key] = s
	return writeFixes(wsDir, f)
}

// PolicyDenial records that a fix run was refused by the sandbox itself,
// rather than abandoned by the agent.
//
// The distinction is the point of OR-174: "produced no change" collapses two
// different failures into one message -- an agent that does not know how to
// fix something, and an agent that knew exactly what to change and was not
// permitted to touch the file. The first calls for another attempt or a
// human to think about it; the second calls for a human to apply the diff,
// and no further attempt will ever succeed.
type PolicyDenial struct {
	// Tool, Path and Rule name what was refused and why, so the message is
	// actionable rather than a restatement of "blocked": "Blocked by policy:
	// Edit(.github/workflows/**)" tells a reader exactly what to go change.
	Tool, Path, Rule string
	// HandOff is the agent's own closing message, kept in full rather than
	// reduced to a one-line summary. On OR-172 the agent had already written
	// the exact one-line fix in prose; this is what carries it to the human
	// who has to apply it, instead of throwing it away with the run.
	HandOff string
}

func writeFixes(wsDir string, f fixFile) error {
	p := fixPath(wsDir)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}
