package web

// Timing is where a card learns whether its run is still going, and how long
// it has taken (OR-58). The types in model.go say what a session HOLDS; this
// file says how the log fills the timing half of one.
//
// THE CLOCK IS AN ARGUMENT, NEVER A CALL. A finished run's duration is a fact
// the log already contains -- first event to last -- and reading time.Now to
// compute it would make the same log report a different number every time the
// page was opened. A run still going has no last event yet, so its duration
// genuinely depends on when you ask; that "when" is passed in, so a test can
// state an expected duration and two readers handed the same instant agree.

import (
	"time"

	"github.com/orion-sdlc/orion/internal/events"
)

// Timing derives one session's Started, Last and Done from that session's
// events. The rest of a Session -- who ran it, on what model, what it is doing
// -- is filled by the reader that owns those fields; this answers only "when,
// and is it over".
//
// GIVE IT ONE RUN'S EVENTS. Grouping the log into runs is the card
// derivation's job and is not repeated here; handed two runs' events this
// would report one session spanning both.
//
// Started and Last are the minimum and maximum timestamp rather than the first
// and last line, because a log is a file and file order is not evidence of
// time order. An empty slice yields the zero Session, which reports no
// duration at all -- the honest answer for a run with nothing recorded.
func Timing(evs []events.Event) Session {
	var s Session
	for _, e := range evs {
		if e.Kind == events.KindRunEnd {
			s.Done = true
		}
		if e.At.IsZero() {
			continue
		}
		if s.Started.IsZero() || e.At.Before(s.Started) {
			s.Started = e.At
		}
		if e.At.After(s.Last) {
			s.Last = e.At
		}
	}
	return s
}

// ElapsedAt is how long the session has taken as of now.
//
// A DONE SESSION IGNORES NOW. Its span is first event to last, which is what
// Elapsed already computes and what every later reader of the same log must
// get back; a finished run whose card grows by a second each time somebody
// refreshes is reporting the reader, not the run.
//
// A session still going has no last event to end at, so it runs to now. It
// never reports LESS than its own events prove, though: if now arrives before
// the newest event -- a skewed clock, a replay of an old log -- the span the
// log recorded stands rather than a shorter one invented from the caller's
// instant.
func (s Session) ElapsedAt(now time.Time) time.Duration {
	if s.Done || s.Started.IsZero() || now.Before(s.Last) {
		return s.Elapsed()
	}
	return now.Sub(s.Started)
}
