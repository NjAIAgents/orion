package web

// The empty-machine test (OR-65): a machine where nothing has ever run must
// not look broken.
//
// A VERIFICATION, NOT A NEW BEHAVIOUR. This file creates no server, no
// command, no endpoint -- OR-60 owns the server skeleton, OR-62 the snapshot
// endpoint, OR-63 the stream endpoint, and each already carries its own
// empty-source test as a unit test against buildSnapshot / streamHandler
// directly. What none of them proves is the thing OR-65 exists to prove:
// that the REAL BOOT PATH -- Listen binding a port, every file's init
// registering its route, an actual client dialing in -- produces the same
// answer. A unit test calling a handler function directly cannot catch a
// route that was never registered, a mux collision Listen panics on, or a
// port bind that fails before any handler runs at all.
//
// ORION_HOME POINTED AT AN EMPTY TEMP DIR is the literal fixture the ticket
// names, not merely "no registry": internal/sessions already states the
// rule this test exercises at the HTTP boundary -- "a missing source is not
// an error... a fresh install is a normal state" -- and this proves that
// rule survives the trip through Listen, the mux, and net/http's own
// encoding of a response.

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"testing"
	"time"
)

// TestOnAFreshInstallEveryEndpointRespondsEmptyButValid is OR-65's done-when,
// almost verbatim: with ORION_HOME pointed at an empty temp dir, the server
// starts and every endpoint returns an empty-but-valid body.
func TestOnAFreshInstallEveryEndpointRespondsEmptyButValid(t *testing.T) {
	restoreHome := setTestHome(t, t.TempDir())
	defer restoreHome()

	srv, err := Listen(0)
	if err != nil {
		t.Fatalf("the server must start on a machine with nothing ever run: %v", err)
	}
	defer srv.Close()
	go func() { _ = srv.Serve() }()

	base := "http://" + srv.Addr()

	t.Run("the placeholder page", func(t *testing.T) {
		resp := get(t, base+"/")
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("expected 200, got %d", resp.StatusCode)
		}
	})

	t.Run("the snapshot endpoint", func(t *testing.T) {
		resp := get(t, base+"/api/snapshot")
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("expected 200, got %d", resp.StatusCode)
		}
		var snap Snapshot
		body, _ := io.ReadAll(resp.Body)
		if err := json.Unmarshal(body, &snap); err != nil {
			t.Fatalf("body is not valid JSON: %v\nbody: %s", err, body)
		}
		if len(snap.Cards) != 0 {
			t.Errorf("expected no cards on a fresh install, got %v", snap.Cards)
		}
	})

	t.Run("the stream endpoint", func(t *testing.T) {
		// EMPTY-BUT-VALID FOR A STREAM MEANS "CONNECTS AND STAYS OPEN", not
		// "returns a body" -- there is nothing to push yet on a machine that
		// has never run anything, and that absence of data is the valid
		// answer. A non-200 or an immediately-closed connection would be the
		// invalid one; this proves neither happens.
		req, err := http.NewRequest(http.MethodGet, base+"/api/stream", nil)
		if err != nil {
			t.Fatal(err)
		}
		client := &http.Client{Timeout: 2 * time.Second}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("the stream endpoint must accept a connection on a fresh install: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("expected 200, got %d", resp.StatusCode)
		}
		if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
			t.Errorf("expected an SSE content type, got %q", ct)
		}
	})
}

// A FRESH INSTALL, not merely an empty registry: no projects directory
// either. sessions.Scan's own doc distinguishes "no registry, empty
// projects dir" from "neither source exists at all", and this ticket's
// fixture is explicitly the second, stricter case.
func TestOnAFreshInstallTheProjectsDirectoryDoesNotExistEither(t *testing.T) {
	home := t.TempDir()
	if _, err := os.Stat(home + "/projects"); !os.IsNotExist(err) {
		t.Fatalf("the fixture must not accidentally create a projects dir: %v", err)
	}
	restoreHome := setTestHome(t, home)
	defer restoreHome()

	snap, err := buildSnapshot(home, time.Now())
	if err != nil {
		t.Fatalf("no projects directory at all must still be a normal state: %v", err)
	}
	if len(snap.Cards) != 0 {
		t.Errorf("expected no cards, got %v", snap.Cards)
	}
}

func get(t *testing.T, url string) *http.Response {
	t.Helper()
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	return resp
}
