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
	KindAnswer   = "answer"    // an advisor answered it
	KindRefuse   = "refuse"    // an advisor could not ground an answer
	KindEscalate = "escalate"  // it went to a human
	KindDecision = "decision"  // a decision record was written
	KindCommit   = "commit"    // commits were produced
	KindPush     = "push"      // a branch reached the remote
	KindPR       = "pr"        // a pull request was opened
	KindCI       = "ci"        // a CI verdict arrived
	KindMerge    = "merge"     // a pull request was merged
	KindRefresh  = "refresh"   // the user's checkout was fast-forwarded
	KindBlocked  = "blocked"   // stopped, needs a person
	KindFailed   = "failed"    // stopped, something broke
	KindBudget   = "budget"    // a spend checkpoint fired
	KindNote     = "note"      // anything else worth seeing
)

// Actor is who did it. "orion" is the supervisor itself; the rest are the
// roles it spawns or defers to.
const (
	ActorOrion       = "orion"
	ActorImplementer = "implementer"
	ActorArchitect   = "architect"
	ActorPM          = "pm"
	ActorHuman       = "human"
	ActorCI          = "ci"
)

// Event is one line of the log.
type Event struct {
	At      time.Time      `json:"at"`
	Kind    string         `json:"kind"`
	Actor   string         `json:"actor"`
	Project string         `json:"project,omitempty"` // FCIA
	Key     string         `json:"key,omitempty"`     // FCIA-6
	Run     string         `json:"run,omitempty"`     // run id, groups a job's events
	Msg     string         `json:"msg"`
	Detail  map[string]any `json:"detail,omitempty"`
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
