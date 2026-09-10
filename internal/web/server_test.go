package web

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"testing"
	"time"
)

// A route registered the way a page will register one (OR-53, OR-54): from an
// init in the route's OWN file, naming nothing in server.go. This test file
// standing in for a page file is the point -- if adding a route needed an
// edit to a list somewhere else, this init could not work on its own.
func init() {
	HandleReadOnly("/or-60-seam", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, "served")
	}))
}

// OR-60's done-when, first half: the server listens on loopback, and on
// nothing else. 0.0.0.0 would put a surface with no authentication on every
// interface the machine has, including the coffee-shop wifi.
func TestListenBindsLoopbackAndNotAllInterfaces(t *testing.T) {
	s, err := Listen(0)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	host, _, err := net.SplitHostPort(s.Addr())
	if err != nil {
		t.Fatalf("Addr = %q, not a host:port: %v", s.Addr(), err)
	}
	if host != "127.0.0.1" {
		t.Errorf("bound host = %q, want 127.0.0.1", host)
	}
	ip := net.ParseIP(host)
	if ip == nil {
		t.Fatalf("bound host = %q, not an IP address", host)
	}
	if ip.IsUnspecified() {
		t.Errorf("bound host = %q: that is every interface, not loopback", host)
	}
	if !ip.IsLoopback() {
		t.Errorf("bound host = %q, want a loopback address", host)
	}
}

// The second half: a route that registered itself is reachable, over the real
// listener, with no line of server.go mentioning it.
func TestRouteRegisteredElsewhereIsServed(t *testing.T) {
	s, err := Listen(0)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	go s.Serve()

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get("http://" + s.Addr() + "/or-60-seam")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(body); got != "served" {
		t.Errorf("body = %q, want %q -- the registered handler did not answer", got, "served")
	}
}

// Serve must actually accept connections once running -- a listener that
// exists but never serves is indistinguishable from a bug until a client
// tries to talk to it.
func TestServeAcceptsHTTPConnections(t *testing.T) {
	s, err := Listen(0)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	go s.Serve()

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get("http://" + s.Addr() + "/or-60-seam")
	if err != nil {
		t.Fatalf("connection to a serving listener failed: %v", err)
	}
	resp.Body.Close()
}

// Close must release the port: a caller that closed the server and expects
// the address free (or the process to exit cleanly) would otherwise find a
// listener still quietly answering.
func TestCloseStopsAcceptingNewConnections(t *testing.T) {
	s, err := Listen(0)
	if err != nil {
		t.Fatal(err)
	}
	addr := s.Addr()
	go s.Serve()

	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	client := &http.Client{Timeout: 5 * time.Second}
	if _, err := client.Get("http://" + addr + "/or-60-seam"); err == nil {
		t.Error("connection after Close succeeded, want refused")
	}
}

// Close is idempotent: shutdown paths call it defensively (defer plus an
// explicit call on an error branch, say), and a second call must not panic.
func TestCloseIsIdempotent(t *testing.T) {
	s, err := Listen(0)
	if err != nil {
		t.Fatal(err)
	}
	go s.Serve()

	if err := s.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}
