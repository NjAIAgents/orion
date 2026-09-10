package web

import (
	"bufio"
	"net"
	"testing"
	"time"
)

// A request that never finishes sending its headers must not hold its
// connection open forever -- ReadHeaderTimeout closes it around 10 seconds
// in. A client that only ever sends a request line (no terminating blank
// line) is exactly that case.
func TestIncompleteHeadersTimeOut(t *testing.T) {
	s, err := Listen(0)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	go s.Serve()

	conn, err := net.Dial("tcp", s.Addr())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	if _, err := conn.Write([]byte("GET /or-60-seam HTTP/1.1\r\nHost: 127.0.0.1\r\n")); err != nil {
		t.Fatal(err)
	}

	conn.SetReadDeadline(time.Now().Add(15 * time.Second))
	start := time.Now()
	_, err = bufio.NewReader(conn).ReadByte()
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("read succeeded on a connection with incomplete headers, want the server to close it")
	}
	if elapsed < 9*time.Second {
		t.Errorf("connection closed after %s, want it held open until ~10s (ReadHeaderTimeout)", elapsed)
	}
	if elapsed > 15*time.Second {
		t.Errorf("connection closed after %s, want it closed by ~10s (ReadHeaderTimeout), not our own deadline", elapsed)
	}
}
