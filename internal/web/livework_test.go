package web

import (
	"testing"
	"time"

	"github.com/orion-sdlc/orion/internal/session"
)

// liveWorkKeys collects every ticket key a currently-beating KindWork
// session claims -- proven here directly against real session.Start/Stop,
// not a fixture, so a real change to the session package's Stale semantics
// would be caught here too.
func TestLiveWorkKeysNamesTicketsAKindWorkSessionClaims(t *testing.T) {
	home := t.TempDir()
	now := time.Now()
	s := session.Start(home, session.KindWork, []string{"OR-1", "OR-2"})
	defer s.Stop()

	keys, err := liveWorkKeys(home, now)
	if err != nil {
		t.Fatal(err)
	}
	if !keys["OR-1"] || !keys["OR-2"] {
		t.Errorf("keys = %v, want OR-1 and OR-2 both live", keys)
	}
}

// A KindWatch session's Projects is project-scoped, not ticket-scoped, and
// must not leak into the ticket-keyed live set -- the exact distinction
// cards.go's Scan doc explains: a watcher being alive says nothing about
// which ticket inside it is currently running.
func TestLiveWorkKeysIgnoresKindWatchSessions(t *testing.T) {
	home := t.TempDir()
	now := time.Now()
	s := session.Start(home, session.KindWatch, []string{"OR-1"})
	defer s.Stop()

	keys, err := liveWorkKeys(home, now)
	if err != nil {
		t.Fatal(err)
	}
	if keys["OR-1"] {
		t.Error("a KindWatch session's project leaked into the ticket-keyed live set")
	}
}

// A stale KindWork record (nothing has beaten it recently) must not read
// as live -- the same Enumerate-filters-stale-records rule session.go
// documents for every other reader.
func TestLiveWorkKeysExcludesAStaleRecord(t *testing.T) {
	home := t.TempDir()
	future := time.Now().Add(session.StaleAfter + time.Minute)
	s := session.Start(home, session.KindWork, []string{"OR-1"})
	defer s.Stop()

	keys, err := liveWorkKeys(home, future)
	if err != nil {
		t.Fatal(err)
	}
	if keys["OR-1"] {
		t.Error("a stale session's key still read as live")
	}
}

// A fresh install with no sessions directory at all is empty, not an error
// -- the same posture session.Enumerate itself takes.
func TestLiveWorkKeysOnAFreshInstallIsEmpty(t *testing.T) {
	home := t.TempDir()
	keys, err := liveWorkKeys(home, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 0 {
		t.Errorf("keys = %v, want empty", keys)
	}
}
