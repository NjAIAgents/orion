package main

// OR-61: network-behavior coverage for `orion web` -- loopback-only binding,
// concurrent instances via --port 0, and the error path when a fixed port is
// already taken. web_test.go and web_defaultport_test.go cover parsing and
// the happy-path serve; these three cases are about the listener's behavior
// under the network, not the flag parsing in front of it.

import (
	"net"
	"strings"
	"testing"
	"time"

	"github.com/orion-sdlc/orion/internal/web"
)

// Case 14: the server binds only the loopback interface, not every interface
// on the machine. web.Listen hardcodes 127.0.0.1, but that is exactly the
// kind of thing a future edit could quietly widen to "" (all interfaces)
// without any existing test noticing -- an internal tool with no auth would
// then be reachable from the rest of the network.
func TestWebBindsLoopbackOnlyNotAllInterfaces(t *testing.T) {
	var out strings.Builder
	srv, err := startWeb(&out, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()

	host, _, err := net.SplitHostPort(srv.Addr())
	if err != nil {
		t.Fatal(err)
	}
	if host != "127.0.0.1" {
		t.Errorf("bound host = %q, want 127.0.0.1 (loopback only)", host)
	}

	// Belt and braces: dialing the same port on a non-loopback local address
	// must fail to connect, because nothing is listening there.
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		t.Fatal(err)
	}
	_, port, err := net.SplitHostPort(srv.Addr())
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range addrs {
		ipnet, ok := a.(*net.IPNet)
		if !ok || ipnet.IP.IsLoopback() || ipnet.IP.To4() == nil {
			continue
		}
		conn, err := net.DialTimeout("tcp", net.JoinHostPort(ipnet.IP.String(), port), 200*time.Millisecond)
		if err == nil {
			conn.Close()
			t.Errorf("connected to %s:%s -- server should not be reachable off loopback", ipnet.IP, port)
		}
	}
}

// Case 15: several `orion web --port 0` instances started at once each get a
// distinct port from the OS and none fails to start. Nothing in web.Listen
// coordinates across instances -- the OS's ephemeral-port allocator is what
// makes this safe -- so this proves that guarantee actually holds rather
// than assuming it.
func TestWebPortZeroConcurrentInstancesDoNotConflict(t *testing.T) {
	const n = 5
	var out [n]strings.Builder
	srvs := make([]*web.Server, 0, n)
	defer func() {
		for _, s := range srvs {
			s.Close()
		}
	}()

	seen := make(map[string]bool, n)
	for i := 0; i < n; i++ {
		port, err := webPort([]string{"--port", "0"})
		if err != nil {
			t.Fatal(err)
		}
		srv, err := startWeb(&out[i], port)
		if err != nil {
			t.Fatalf("instance %d: startWeb failed: %v", i, err)
		}
		srvs = append(srvs, srv)

		if seen[srv.Addr()] {
			t.Fatalf("instance %d: addr %q was already handed to an earlier instance", i, srv.Addr())
		}
		seen[srv.Addr()] = true
	}
}

// Case 16: binding a port already in use fails with an error, not a silent
// success on some other address. web.Listen wraps net.Listen's error rather
// than swallowing it, and this is what would catch a regression that lost
// that wrapping.
func TestWebBindingAnAlreadyUsedPortFails(t *testing.T) {
	holder, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Close()
	_, port, err := net.SplitHostPort(holder.Addr().String())
	if err != nil {
		t.Fatal(err)
	}

	n, err := webPort([]string{"--port", port})
	if err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	srv, err := startWeb(&out, n)
	if err == nil {
		srv.Close()
		t.Fatalf("startWeb on already-bound port %s succeeded, want an error", port)
	}
	if out.Len() != 0 {
		t.Errorf("printed %q on a failed bind, want nothing announced", out.String())
	}
}
