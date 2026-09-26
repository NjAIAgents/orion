package web

import (
	"net"
	"strconv"
	"testing"
)

// Close before Serve was ever called must still release the underlying
// socket. http.Server.Close only closes listeners it is tracking, and it
// only starts tracking a listener once Serve(ln) registers it -- a caller
// that binds, decides not to serve, and cleans up (Listen's own doc comment
// describes printing the address before Serve runs) would otherwise leak the
// file descriptor and the port. This isolates the defect TestPortIsReusable-
// AfterClose already reports, without depending on a second Listen() racing
// the OS to reclaim the port.
func TestCloseBeforeServeReleasesTheListener(t *testing.T) {
	s, err := Listen(0)
	if err != nil {
		t.Fatal(err)
	}
	_, portStr, err := net.SplitHostPort(s.Addr())
	if err != nil {
		t.Fatalf("Addr = %q, not a host:port: %v", s.Addr(), err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatalf("port = %q, not a number: %v", portStr, err)
	}

	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	ln, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		t.Fatalf("port %d still held after Close without Serve ever being called: %v", port, err)
	}
	ln.Close()
}

// A port above the valid 0-65535 range is never valid: net.Listen must
// reject it just as it rejects a negative one, rather than silently
// truncating or wrapping it into some other port number.
func TestListenReturnsErrorOnPortAboveValidRange(t *testing.T) {
	s, err := Listen(70000)
	if err == nil {
		s.Close()
		t.Fatal("Listen(70000) succeeded, want an error for a port above the valid range")
	}
}
