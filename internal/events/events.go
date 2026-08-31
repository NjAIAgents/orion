// Package events is Orion's live orchestration log.
//
// Separate from the agent transcript on purpose. The transcript is what the
// model said and did -- tens of thousands of tokens per run, already written
// to .orion/logs/*.log. This is what ORION did: claimed a ticket, cut a
// branch, asked the architect, escalated, opened a pull request. A dozen
// lines where the transcript has thousands.
//
// Merging them would be the obvious choice and the wrong one. The line that
// matters ("architect answered: by issuer, per spec.md §4") becomes
// invisible inside a wall of tool output, and the whole point of a live log
// is that a person can glance at it and know where things stand.
//
// JSON Lines rather than prose so the same file serves three readers: a
// human watching `orion tail`, a Slack relay picking out the events worth
// posting, and whatever dashboard comes later. Prose cannot be filtered.
package events

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Kind is what happened. Kept as a closed vocabulary so a reader can filter
// on it without pattern-matching free text.
const (
	KindClaimed  = "claimed"   // a ticket was taken out of the queue
	KindBranch   = "branch"    // a worktree and branch were created
	KindRunStart = "run-start" // an agent was launched
	KindRunEnd   = "run-end"   // it exited, with a code
	KindAsk      = "ask"       // the implementer asked a question
	KindAnswer   = "answer"    // an advisor answered it, in the advisor's own words
	KindRefuse   = "refuse"    // an advisor could not ground an answer
	KindEscalate = "escalate"  // it went to a human
	KindDecision = "decision"  // a choice between alternatives, and why -- see below
	KindCommit   = "commit"    // commits were produced
	KindPush     = "push"      // a branch reached the remote
	KindPR       = "pr"        // a pull request was opened
	KindCI       = "ci"        // a CI verdict arrived
	KindQA       = "qa"        // QA reported on the change: findings, or clean
	KindMerge    = "merge"     // a pull request was merged
	KindRefresh  = "refresh"   // the user's checkout was fast-forwarded
	KindBlocked  = "blocked"   // stopped, needs a person
	KindFailed   = "failed"    // stopped, something broke
	KindBudget   = "budget"    // a spend checkpoint fired
	KindUsage    = "usage"     // what one agent run consumed, per actor
	KindTool     = "tool"      // an agent used a tool, as it used it
	KindSay      = "say"       // an agent said what it was doing
	KindStage    = "stage"     // the run crossed from one stage into the next
	KindNote     = "note"      // anything else worth seeing
)

// STAGE IS A BOUNDARY, NOT AN ACTION. Every other kind reports something that
// happened inside a stage; this one reports the moment between two of them,
// and it is the only kind whose two consecutive occurrences are a DURATION.
//
// That is what it is for. "How long did this run spend in QA" and "how long
// did it sit waiting for a human" are the two questions the log could not
// answer, because a handoff left no trace at all -- the reader had to know
// which actor holds which role and infer the crossing from the names
// changing between two ordinary status lines (OR-189).
//
// Its Detail carries the two stage names and the ACTOR IDENTIFIERS on each
// side, never their display names: the identifiers are what this file
// promises never to change, and internal/actors resolves them to names at
// render time. A boundary whose next side is ci or human is a boundary where
// NO AGENT IS RUNNING, and internal/ui renders it saying so.

// ASK AND ANSWER ARE A PAIR. Every ask is closed by an answer or a refuse,
// on the same ticket, before the path that raised it returns. An ask with
// neither leaves the log saying a question was raised and never saying what
// became of it -- and whatever the implementer then did on the strength of
// the reply is unexplainable afterwards (OR-201).
//
// The answer carries WHAT WAS SAID, the way KindSay carries the agent's own
// prose. "the advisor responded" is worth nothing: the text is the point,
// and it is the only copy outside a transcript nobody reads.
//
// DECISION VERSUS NOTE. These two are one keystroke apart and note is the
// catch-all, so without a rule every decision drifts into a note and the
// reasoning leaves the log. That is exactly how "decision" came to be
// defined, documented and never once emitted (OR-201).
//
// A decision needs BOTH halves:
//
//	an alternative that was available and not taken, and
//	the reason it was not.
//
// "routed to the frontend developer: matched the ui label" is a decision --
// another actor could have had the ticket, and the label says why this one
// did. "asked for merge approval in Slack" is a note: it reports what
// happened and nothing was chosen. Missing either half makes it a note.
//
// Rejecting the borderline case is the right default. A decision stream that
// admits everything is a second note stream, and the only reason filtering on
// "decision" is worth anything is that it stays the short list.

// Actor is who did it. "orion" is the supervisor itself; the rest are the
// roles it spawns or defers to.
//
// These strings are PERSISTED. They are written into the append-only event
// log, and `orion logs` and `orion report` read that history back, so a
// rename would leave old entries saying one thing and new ones another and
// every reader would have to know both spellings forever. The display form
// -- the name and job title a person reads -- lives in internal/actors and
// is applied at render time, which is what lets a name change without
// migrating a single line of history.
const (
	ActorOrion       = "orion"
	ActorRouter      = "router"      // decides which advisor a question belongs to
	ActorImplementer = "implementer" // works a backend ticket
	ActorFrontend    = "frontend"    // works a UI ticket
	ActorDocs        = "docs"        // works a documentation ticket
	ActorArchitect   = "architect"
	ActorPM          = "pm"
	ActorDevOps      = "devops"      // repairs a red build
	ActorQA          = "qa"          // derives test cases, writes the tests, runs them
	ActorDescriber   = "describer"   // writes the pull request description
	ActorLogTriage   = "log-triage"  // reads a failing CI log, reports what broke and why
	ActorExplore     = "explore"     // answers one question about the repository, citing the paths
	ActorCaseDerive  = "case-derive" // reads the acceptance criteria and the diff, lists the cases to cover
	ActorAIOps       = "aiops"       // reads a finished run's event log, reports what is worth filing
	ActorDoneTriage  = "done-triage" // reads a green run against its diff: genuinely done, or only looks done
	ActorHuman       = "human"
	ActorCI          = "ci"
)

// Event is one line of the log.
type Event struct {
	At      time.Time `json:"at"`
	Kind    string    `json:"kind"`
	Actor   string    `json:"actor"`
	Project string    `json:"project,omitempty"` // FCIA
	Key     string    `json:"key,omitempty"`     // FCIA-6
	Run     string    `json:"run,omitempty"`     // run id, groups a job's events
	// Model is which model did this, when a model did it. Recorded per event
	// rather than per run because a single ticket is worked by several: opus
	// implements, haiku routes the question it stops on, sonnet answers it.
	// Attributing a decision to "the agent" loses the only detail that
	// explains why one answer was careful and another was cheap.
	Model  string         `json:"model,omitempty"`
	Msg    string         `json:"msg"`
	Detail map[string]any `json:"detail,omitempty"`
}

// Rotation limits. A live log runs unattended for days, so it needs a
// ceiling; but the recent past is what anyone actually reads, so the ceiling
// is small and the history shallow.
// Variables rather than constants so tests can shrink the ceiling instead of
// writing megabytes to reach it. Emit fsyncs every event -- correct for a
// live log, and slow enough that brute-forcing rotation in a test cost 99
// seconds of CI time to prove something a 4KB ceiling proves in one.
var (
	MaxBytes int64 = 2 << 20 // 2 MiB per file
	MaxFiles       = 5       // the current file plus four archives
)

// Log appends events to a file, rotating it by size.
type Log struct {
	mu   sync.Mutex
	f    *os.File
	path string
	size int64
	// base fields stamped onto every event, so callers pass only what varies.
	base Event
}

// Open creates or appends to a log.
func Open(path string, base Event) (*Log, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	// 0600: questions and answers quote the artifacts, and the artifacts are
	// the product. This sits in ORION_HOME and inherits its posture.
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	var size int64
	if fi, statErr := f.Stat(); statErr == nil {
		size = fi.Size()
	}
	return &Log{f: f, path: path, size: size, base: base}, nil
}

// rotate renames the current file aside and starts a new one, keeping at
// most MaxFiles generations.
//
// Renaming rather than truncating is what makes this safe for a reader: a
// tailer holding the old descriptor keeps reading a file that still exists
// and is complete, instead of having the bytes pulled out from under it.
// Follow then notices the path now points at a different inode and reopens.
//
// Called with the lock held.
func (l *Log) rotate() {
	_ = l.f.Close()

	// Drop the oldest, then shift each generation down. Errors are ignored
	// throughout: a rotation that cannot complete must not stop the run, and
	// the worst case is a log slightly over its ceiling.
	oldest := fmt.Sprintf("%s.%d", l.path, MaxFiles-1)
	_ = os.Remove(oldest)
	for n := MaxFiles - 2; n >= 1; n-- {
		_ = os.Rename(fmt.Sprintf("%s.%d", l.path, n), fmt.Sprintf("%s.%d", l.path, n+1))
	}
	_ = os.Rename(l.path, l.path+".1")

	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		// Reopen the old path so emitting keeps working rather than panicking
		// on a nil file. Losing rotation is survivable; losing the log is not.
		if reopened, err2 := os.OpenFile(l.path+".1", os.O_WRONLY|os.O_APPEND, 0o600); err2 == nil {
			l.f = reopened
		}
		return
	}
	l.f = f
	l.size = 0
}

// Emit appends one event and flushes it immediately.
//
// No buffering, deliberately. A live log that batches is not live, and the
// moment the events matter most -- a run that is about to be killed, or a
// process that crashes -- is exactly the moment a buffer is lost. One write
// per event costs nothing at this volume.
func (l *Log) Emit(e Event) {
	if l == nil {
		return // logging must never be the reason a run fails
	}
	if e.At.IsZero() {
		e.At = time.Now().UTC()
	}
	if e.Actor == "" {
		e.Actor = l.base.Actor
	}
	if e.Project == "" {
		e.Project = l.base.Project
	}
	if e.Key == "" {
		e.Key = l.base.Key
	}
	if e.Run == "" {
		e.Run = l.base.Run
	}
	b, err := json.Marshal(e)
	if err != nil {
		return
	}
	b = append(b, '\n')

	l.mu.Lock()
	defer l.mu.Unlock()
	// Rotate BEFORE writing, so an event is never split across two files.
	// A reader stitching generations back together should never have to
	// handle half a line at a boundary.
	if l.size > 0 && l.size+int64(len(b)) > MaxBytes {
		l.rotate()
	}
	n, _ := l.f.Write(b)
	l.size += int64(n)
	_ = l.f.Sync()
}

// Emitf is the common case: a kind, an actor and a message.
func (l *Log) Emitf(kind, actor, format string, a ...any) {
	l.Emit(Event{Kind: kind, Actor: actor, Msg: fmt.Sprintf(format, a...)})
}

func (l *Log) Close() error {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.f.Close()
}

func (l *Log) Path() string {
	if l == nil {
		return ""
	}
	return l.path
}

// Read parses a log file. Malformed lines are skipped rather than failing
// the read: a log truncated by a kill still has value up to the cut, and
// refusing to show any of it would discard the evidence at the moment it is
// most wanted.
func Read(path string) ([]Event, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return parse(f), nil
}

func parse(r io.Reader) []Event {
	var out []Event
	sc := bufio.NewScanner(r)
	// Events carry quoted artifact text; the default 64KB token limit is
	// reachable, and a silently dropped line is worse than a large buffer.
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var e Event
		if json.Unmarshal([]byte(line), &e) != nil {
			continue
		}
		out = append(out, e)
	}
	return out
}

// Follow streams events as they are appended, calling fn for each.
//
// Polls rather than using filesystem notifications: one stat per interval is
// cheaper than the portability cost of fsevents/inotify, and this is a log
// someone watches for minutes, not a hot path.
func Follow(path string, from int64, interval time.Duration, stop <-chan struct{}, fn func(Event)) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	if _, err := f.Seek(from, io.SeekStart); err != nil {
		return err
	}
	rd := bufio.NewReader(f)
	var pending strings.Builder

	// Remember which file we are reading. Rotation renames this one aside and
	// creates a new one at the same path, so a follower that only holds the
	// descriptor keeps reading a file nothing writes to any more and silently
	// goes quiet -- the failure looking exactly like "nothing is happening".
	cur := identity(f)

	for {
		select {
		case <-stop:
			return nil
		default:
		}

		line, err := rd.ReadString('\n')
		if err == io.EOF {
			// A partial line means the writer is mid-append. Hold it and
			// finish it next pass rather than parsing half an event.
			if line != "" {
				pending.WriteString(line)
			}
			// Drain the old file completely before following the rotation,
			// so the last events written before the rename are not lost.
			if rotated(path, cur) {
				nf, nerr := os.Open(path)
				if nerr == nil {
					_ = f.Close()
					f, rd, cur = nf, bufio.NewReader(nf), identity(nf)
					pending.Reset()
					continue
				}
			}
			time.Sleep(interval)
			continue
		}
		if err != nil {
			return err
		}
		if pending.Len() > 0 {
			line = pending.String() + line
			pending.Reset()
		}
		var e Event
		if json.Unmarshal([]byte(strings.TrimSpace(line)), &e) == nil {
			fn(e)
		}
	}
}

// identity fingerprints an open file so a rotation can be detected. Uses
// os.SameFile semantics rather than comparing sizes, because a rotated log
// can briefly be the same size as the one it replaced.
func identity(f *os.File) os.FileInfo {
	fi, err := f.Stat()
	if err != nil {
		return nil
	}
	return fi
}

func rotated(path string, cur os.FileInfo) bool {
	if cur == nil {
		return false
	}
	fi, err := os.Stat(path)
	if err != nil {
		return false // the path is gone entirely; keep reading what we have
	}
	return !os.SameFile(fi, cur)
}

// Path is where a workspace's event log lives.
func Path(workspaceDir string) string {
	return filepath.Join(workspaceDir, ".orion", "events.jsonl")
}
