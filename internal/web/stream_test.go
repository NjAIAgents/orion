package web

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/orion-sdlc/orion/internal/events"
)

// DONE-WHEN, CLAUSE ONE: an appended line reaches a connected client.
//
// A real net listener rather than httptest.NewRecorder, because a Recorder
// buffers writes and never delivers them mid-request -- it cannot exercise
// "the client is already connected when the append happens", which is the
// entire property under test.
func TestAnAppendedLineReachesAConnectedClient(t *testing.T) {
	home := t.TempDir()
	ws := mkTestWorkspace(t, home, "proj-a")
	restoreHome := setTestHome(t, home)
	defer restoreHome()

	srv := httptest.NewServer(http.HandlerFunc(streamHandler))
	defer srv.Close()

	resp := connectSSE(t, srv.URL+"/api/stream")
	defer resp.Body.Close()

	appendEvent(t, ws, events.Event{
		Kind: events.KindRunStart, Key: "OR-1", Actor: "implementer", Run: "r1",
	})

	got := readOneSSEEvent(t, resp.Body, 5*time.Second)
	if got.Key != "OR-1" {
		t.Fatalf("expected the appended event's key, got %q", got.Key)
	}
}

// DONE-WHEN, CLAUSE TWO: ticket and actor filters apply.
func TestTheKeyFilterOnlyDeliversMatchingEvents(t *testing.T) {
	home := t.TempDir()
	ws := mkTestWorkspace(t, home, "proj-a")
	restoreHome := setTestHome(t, home)
	defer restoreHome()

	srv := httptest.NewServer(http.HandlerFunc(streamHandler))
	defer srv.Close()

	resp := connectSSE(t, srv.URL+"/api/stream?key=OR-2")
	defer resp.Body.Close()

	appendEvent(t, ws, events.Event{
		Kind: events.KindRunStart, Key: "OR-1", Actor: "implementer", Run: "r1",
	})
	appendEvent(t, ws, events.Event{
		Kind: events.KindRunStart, Key: "OR-2", Actor: "qa", Run: "r2",
	})

	got := readOneSSEEvent(t, resp.Body, 5*time.Second)
	if got.Key != "OR-2" {
		t.Fatalf("the filter let OR-1 through; expected only OR-2, got %q", got.Key)
	}
}

func TestTheActorFilterOnlyDeliversMatchingEvents(t *testing.T) {
	home := t.TempDir()
	ws := mkTestWorkspace(t, home, "proj-a")
	restoreHome := setTestHome(t, home)
	defer restoreHome()

	srv := httptest.NewServer(http.HandlerFunc(streamHandler))
	defer srv.Close()

	resp := connectSSE(t, srv.URL+"/api/stream?actor=qa")
	defer resp.Body.Close()

	appendEvent(t, ws, events.Event{
		Kind: events.KindRunStart, Key: "OR-1", Actor: "implementer", Run: "r1",
	})
	appendEvent(t, ws, events.Event{
		Kind: events.KindRunStart, Key: "OR-1", Actor: "qa", Run: "r1",
	})

	got := readOneSSEEvent(t, resp.Body, 5*time.Second)
	if got.Actor != "qa" {
		t.Fatalf("the filter let the implementer's line through; expected only qa, got %q", got.Actor)
	}
}

// DONE-WHEN, CLAUSE THREE: a disconnect test shows no leaked goroutine.
//
// Asserted on the actual goroutine count, before and after, rather than on
// an internal flag -- a leaked follower is a leaked follower whether or not
// this file remembers to expose a counter for it.
func TestADisconnectStopsEveryFollowGoroutine(t *testing.T) {
	home := t.TempDir()
	mkTestWorkspace(t, home, "proj-a")
	mkTestWorkspace(t, home, "proj-b")
	restoreHome := setTestHome(t, home)
	defer restoreHome()

	before := runtime.NumGoroutine()

	srv := httptest.NewServer(http.HandlerFunc(streamHandler))
	defer srv.Close()

	resp := connectSSE(t, srv.URL+"/api/stream")
	// Read the SSE preamble so the handler has actually started its
	// followers before this test pulls the connection out from under it.
	_, _ = bufio.NewReader(resp.Body).Peek(1)
	resp.Body.Close()

	// Followers exit on the next Follow poll tick (followInterval) after the
	// context is cancelled, plus scheduling slack -- not instantly.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if runtime.NumGoroutine() <= before+2 { // +2: test/runtime slack, not this handler's
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Errorf("goroutines before=%d after=%d; the followers did not exit on disconnect",
		before, runtime.NumGoroutine())
}

// A workspace with no log yet does not fail the handshake for every other
// workspace connected to it -- its follower waits quietly in the background
// (proven by TestADisconnectStopsEveryFollowGoroutine, which exercises the
// same wait-then-stop path) rather than the connection itself erroring.
func TestAWorkspaceWithNoLogYetDoesNotFailTheConnection(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, "projects", "empty"), 0o700); err != nil {
		t.Fatal(err)
	}
	restoreHome := setTestHome(t, home)
	defer restoreHome()

	srv := httptest.NewServer(http.HandlerFunc(streamHandler))
	defer srv.Close()

	resp := connectSSE(t, srv.URL+"/api/stream")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 even with a log-less workspace present, got %d", resp.StatusCode)
	}
}

// --- helpers ---

// setTestHome points workspace.Home() -- and therefore sessions.Scan inside
// the handler -- at a test fixture, restoring the real environment after.
func setTestHome(t *testing.T, home string) func() {
	t.Helper()
	prev, had := os.LookupEnv("ORION_HOME")
	if err := os.Setenv("ORION_HOME", home); err != nil {
		t.Fatal(err)
	}
	return func() {
		if had {
			_ = os.Setenv("ORION_HOME", prev)
		} else {
			_ = os.Unsetenv("ORION_HOME")
		}
	}
}

func connectSSE(t *testing.T, url string) *http.Response {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func readOneSSEEvent(t *testing.T, r io.Reader, timeout time.Duration) events.Event {
	t.Helper()
	scanner := bufio.NewScanner(r)
	done := make(chan events.Event, 1)
	errc := make(chan error, 1)
	go func() {
		for scanner.Scan() {
			line := scanner.Text()
			if !strings.HasPrefix(line, "data: ") {
				continue
			}
			var e events.Event
			if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &e); err != nil {
				errc <- err
				return
			}
			done <- e
			return
		}
		if err := scanner.Err(); err != nil {
			errc <- err
		}
	}()
	select {
	case e := <-done:
		return e
	case err := <-errc:
		t.Fatalf("reading the SSE stream: %v", err)
	case <-time.After(timeout):
		t.Fatal("timed out waiting for an SSE event")
	}
	return events.Event{}
}
