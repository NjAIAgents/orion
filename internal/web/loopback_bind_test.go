package web

import (
	"net"
	"testing"
)

// Loopback is the only option Listen offers -- 127.0.0.1, not 0.0.0.0. This
// asserts the exact string, not just "some loopback address", because Listen
// hardcodes "127.0.0.1" and a regression there (e.g. to net.JoinHostPort with
// an empty host, which net.Listen resolves to all interfaces) would still
// satisfy a looser ip.IsLoopback() check on some platforms.
func TestListenBindsExactly127001NotWildcard(t *testing.T) {
	s, err := Listen(0)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	host, _, err := net.SplitHostPort(s.Addr())
	if err != nil {
		t.Fatalf("Addr = %q, not a host:port: %v", s.Addr(), err)
	}
	if host == "0.0.0.0" || host == "" || host == "::" {
		t.Fatalf("bound host = %q, want 127.0.0.1 -- that is every interface, not loopback", host)
	}
	if host != "127.0.0.1" {
		t.Errorf("bound host = %q, want 127.0.0.1", host)
	}
}

// Nothing but 127.0.0.1 -- not another loopback-adjacent address, not a LAN
// or public IP the process happens to also own.
func TestListenBindsLoopbackNotOtherAddresses(t *testing.T) {
	s, err := Listen(0)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	host, _, err := net.SplitHostPort(s.Addr())
	if err != nil {
		t.Fatalf("Addr = %q, not a host:port: %v", s.Addr(), err)
	}
	for _, other := range []string{"192.168.1.1", "10.0.0.1", "0.0.0.0", "::"} {
		if host == other {
			t.Fatalf("bound host = %q, want 127.0.0.1", host)
		}
	}
	if host != "127.0.0.1" {
		t.Errorf("bound host = %q, want 127.0.0.1", host)
	}
}

// Port 0 means "OS, pick one" -- Addr must report the real assigned port,
// never the literal 0 that was passed in.
func TestListenPort0GetsOSAssignedPort(t *testing.T) {
	s, err := Listen(0)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	_, portStr, err := net.SplitHostPort(s.Addr())
	if err != nil {
		t.Fatalf("Addr = %q, not a host:port: %v", s.Addr(), err)
	}
	if portStr == "0" {
		t.Fatalf("Addr = %q, want a real assigned port, not 0", s.Addr())
	}
}

// Addr must be valid as soon as Listen returns -- before Serve is ever
// called -- since a caller prints the address to the operator first, then
// starts serving.
func TestAddrValidBeforeServeAfterListen(t *testing.T) {
	s, err := Listen(0)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	addr := s.Addr()
	if addr == "" {
		t.Fatal("Addr = \"\" before Serve was ever called")
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatalf("Addr = %q before Serve, not a host:port: %v", addr, err)
	}
	if host != "127.0.0.1" || port == "" || port == "0" {
		t.Errorf("Addr = %q before Serve, want a real 127.0.0.1:<port>", addr)
	}
}
