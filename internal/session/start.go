package session

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"sync"
	"time"
)

// Session is a live handle on one process's heartbeat record. Start returns
// one; the caller holds it for the process's lifetime and calls Stop on the
// way out.
type Session struct {
	home string
	id   string

	mu      sync.Mutex
	doing   string
	stop    chan struct{}
	stopped chan struct{}
}

// Start sweeps stale records (see Sweep's doc comment for why this is the
// only place that happens), mints a new record for this process, and begins
// beating it every BeatInterval in the background.
//
// projects names what this process has in scope -- every project for a
// bare `orion watch`, one for `orion work`. kind is KindWatch or KindWork.
//
// THE RUN PATH IS UNAFFECTED ON FAILURE: if home is unwritable, Start still
// returns a live *Session, whose beat simply keeps failing silently and
// whose Stop is a no-op. This mirrors events.Log.Emit's own posture, cited
// by name in OR-52's acceptance criteria -- a heartbeat file is a display
// convenience, not something a run should abort over. That is also why
// Start returns no error: there is nothing a caller could usefully do with
// one that Start has not already done itself (degrade and keep going).
func Start(home string, kind Kind, projects []string) *Session {
	if n, err := Sweep(home, time.Now()); err == nil {
		_ = n // nothing to report to; a caller wanting the count can call
		// Sweep itself. Start's job is only to make sure it happens.
	}

	id, err := newID()
	if err != nil {
		// A ulid/uuid library is not worth adding for this; crypto/rand
		// failing at all is a sign of a broken machine, and even then the
		// session should still run -- so this degrades to a timestamp-only
		// id rather than failing Start.
		id = fmt.Sprintf("fallback-%d", time.Now().UnixNano())
	}

	s := &Session{
		home:    home,
		id:      id,
		stop:    make(chan struct{}),
		stopped: make(chan struct{}),
	}

	rec := Record{
		ID:       id,
		Kind:     kind,
		Projects: projects,
		PID:      os.Getpid(),
		Host:     hostname(),
		Started:  time.Now(),
		LastBeat: time.Now(),
	}
	_ = write(home, rec) // failure is non-fatal; see the doc comment above

	go s.beat(rec)
	return s
}

// ID is this session's record id, for a caller that wants to correlate it
// with something else (a log line, a test assertion).
func (s *Session) ID() string { return s.id }

// SetDoing updates what this session is working on now. Picked up by the
// NEXT heartbeat -- not written immediately -- so a caller can call this as
// often as it likes (once per ticket claimed, say) without turning it into
// a second write path competing with the beat.
func (s *Session) SetDoing(doing string) {
	s.mu.Lock()
	s.doing = doing
	s.mu.Unlock()
}

// beat touches the record every BeatInterval until Stop closes s.stop, then
// removes the record and closes s.stopped so Stop can wait for the file to
// actually be gone before returning.
func (s *Session) beat(rec Record) {
	defer close(s.stopped)
	ticker := time.NewTicker(BeatInterval)
	defer ticker.Stop()

	for {
		select {
		case <-s.stop:
			_ = os.Remove(path(s.home, s.id))
			return
		case <-ticker.C:
			s.mu.Lock()
			rec.Doing = s.doing
			s.mu.Unlock()
			rec.LastBeat = time.Now()
			_ = write(s.home, rec) // non-fatal; see Start's doc comment
		}
	}
}

// Stop ends the heartbeat and removes this session's record, waiting for
// both to actually happen before returning -- so a caller that calls Stop
// right before process exit does not race its own cleanup.
func (s *Session) Stop() {
	close(s.stop)
	<-s.stopped
}

// newID mints a short random id for the record's filename. Not a UUID
// library: 16 random bytes, hex-encoded, is exactly as collision-resistant
// and needs no new dependency in a project that ships with zero external Go
// ones.
func newID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
