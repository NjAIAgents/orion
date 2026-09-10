package web

import (
	"net"
	"net/http"
	"strconv"
	"testing"
)

// Registering the same pattern twice must panic when Listen builds the mux
// -- at startup, where it is one obvious failure, rather than on whichever
// request happens to find the collision. routes is package state shared
// across tests, so the duplicate entries are trimmed back off afterward:
// left in place, every later Listen() call in this binary would panic too.
func TestListenPanicsOnDuplicateRoutePattern(t *testing.T) {
	originalLen := len(routes)
	defer func() { routes = routes[:originalLen] }()

	pattern := "/or-60-duplicate"
	Handle(pattern, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	Handle(pattern, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))

	defer func() {
		if r := recover(); r == nil {
			t.Fatal("Listen with a duplicate route pattern did not panic")
		}
	}()
	Listen(0)
}

// A negative port is never valid: net.Listen must reject it rather than
// Listen silently binding somewhere unexpected.
func TestListenReturnsErrorOnInvalidPort(t *testing.T) {
	s, err := Listen(-1)
	if err == nil {
		s.Close()
		t.Fatal("Listen(-1) succeeded, want an error for an invalid port")
	}
}

// A port already held by another listener must fail, not silently pick a
// different one -- the caller asked for a specific port.
func TestListenReturnsErrorOnUnavailablePort(t *testing.T) {
	first, err := Listen(0)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()

	_, portStr, err := net.SplitHostPort(first.Addr())
	if err != nil {
		t.Fatalf("Addr = %q, not a host:port: %v", first.Addr(), err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatalf("port = %q, not a number: %v", portStr, err)
	}

	second, err := Listen(port)
	if err == nil {
		second.Close()
		t.Fatalf("Listen(%d) succeeded while the port was already bound, want an error", port)
	}
}

// Close must actually release the port back to the OS -- a caller that
// restarts the server on the same port (a config reload, a retry) would
// otherwise find it still held.
func TestPortIsReusableAfterClose(t *testing.T) {
	first, err := Listen(0)
	if err != nil {
		t.Fatal(err)
	}
	_, portStr, err := net.SplitHostPort(first.Addr())
	if err != nil {
		t.Fatalf("Addr = %q, not a host:port: %v", first.Addr(), err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatalf("port = %q, not a number: %v", portStr, err)
	}

	if err := first.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	second, err := Listen(port)
	if err != nil {
		t.Fatalf("Listen(%d) after Close failed, want the port to be free: %v", port, err)
	}
	defer second.Close()
}
