package web

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"testing"
	"time"
)

// Two routes registered from two separate init functions in this one file --
// standing in for two page files (OR-53, OR-54) that would otherwise need to
// be listed in server.go. Distinct patterns from server_test.go/routes_test.go
// so this file's cases don't share state with theirs.
func init() {
	Handle("/or-60-reg-a", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, "a")
	}))
}

func init() {
	Handle("/or-60-reg-b", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, "b")
	}))
}

// A route registered via init in a file other than server.go -- this file --
// must be reachable with no edit to server.go itself. That is the whole
// point of the Handle seam.
func TestRouteRegisteredFromSeparateFileViaInit(t *testing.T) {
	s, err := Listen(0)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	go s.Serve()

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get("http://" + s.Addr() + "/or-60-reg-a")
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
	if got := string(body); got != "a" {
		t.Errorf("body = %q, want %q -- route registered from a separate file's init did not answer", got, "a")
	}
}

// Two routes registered independently (different init funcs, different
// patterns) must each answer as themselves -- one registration must not
// clobber, merge with, or shadow the other.
func TestMultipleRoutesRegisterIndependently(t *testing.T) {
	s, err := Listen(0)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	go s.Serve()

	client := &http.Client{Timeout: 5 * time.Second}

	cases := []struct {
		path string
		want string
	}{
		{"/or-60-reg-a", "a"},
		{"/or-60-reg-b", "b"},
	}
	for _, c := range cases {
		resp, err := client.Get("http://" + s.Addr() + c.path)
		if err != nil {
			t.Fatalf("GET %s: %v", c.path, err)
		}
		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			t.Fatalf("GET %s: read body: %v", c.path, err)
		}
		if resp.StatusCode != http.StatusOK {
			t.Errorf("GET %s: status = %d, want %d", c.path, resp.StatusCode, http.StatusOK)
		}
		if got := string(body); got != c.want {
			t.Errorf("GET %s: body = %q, want %q", c.path, got, c.want)
		}
	}
}

// Addr must be a valid host:port pair as net.SplitHostPort understands it --
// the format callers rely on to print an address or dial one in a test,
// rather than some other net.Addr.String() shape.
func TestAddrFormatIsValidHostPort(t *testing.T) {
	s, err := Listen(0)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	host, port, err := net.SplitHostPort(s.Addr())
	if err != nil {
		t.Fatalf("Addr() = %q is not a valid host:port: %v", s.Addr(), err)
	}
	if host == "" {
		t.Errorf("Addr() = %q: empty host", s.Addr())
	}
	if port == "" {
		t.Errorf("Addr() = %q: empty port", s.Addr())
	}
}
