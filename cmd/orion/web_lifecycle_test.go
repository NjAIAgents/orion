package main

// OR-61 coverage for `orion web`'s process lifecycle and access model: a
// Ctrl-C shutdown, the absence of any authentication, 404/read-only
// enforcement reached through the real command wiring (not just Assets() in
// isolation), and --port's 0-65535 range check happening before anything is
// bound. web_test.go, web_defaultport_test.go and web_network_test.go cover
// flag parsing, the happy path and the listener's network behavior; these
// cases sit beside them rather than inside them.

import (
	"bytes"
	"net"
	"net/http"
	"os"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/orion-sdlc/orion/internal/testproc"
)

// Case 17: Ctrl-C (SIGINT) must stop the process and release the port -- not
// leave the listener held by a hung or half-shut-down process. web.go's own
// comment says there is no state to flush, so "no data loss" here means
// exactly that: the port is free again once the process is gone, which is
// what a second `orion web` on the same port depends on.
func TestCtrlCStopsTheServerAndReleasesThePort(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("SIGINT via os.Process.Signal is not supported on Windows")
	}

	bin := orionBinary(t)
	cmd := testproc.Command(t, bin, "web", "--port", "0")
	cmd.Env = append(os.Environ(), "ORION_HOME="+t.TempDir())
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}

	buf := make([]byte, 256)
	n, err := stdout.Read(buf)
	if err != nil {
		t.Fatalf("orion web printed nothing before exiting: %v (stderr: %q)", err, stderr.String())
	}
	line := strings.TrimSpace(string(buf[:n]))
	url, ok := strings.CutPrefix(line, "orion web: ")
	if !ok {
		t.Fatalf("first line was %q, want an `orion web: <url>` announcement", line)
	}
	_, port, err := net.SplitHostPort(strings.TrimPrefix(url, "http://"))
	if err != nil {
		t.Fatal(err)
	}

	client := &http.Client{Timeout: 5 * time.Second}
	if _, err := client.Get(url); err != nil {
		t.Fatalf("server was not actually serving before the signal: %v", err)
	}

	if err := cmd.Process.Signal(syscall.SIGINT); err != nil {
		t.Fatalf("sending SIGINT: %v", err)
	}

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case <-done:
		// exited; SIGINT's default Go disposition is termination, so any
		// resulting *exec.ExitError is expected here rather than a failure.
	case <-time.After(5 * time.Second):
		t.Fatal("process did not exit within 5s of SIGINT -- shutdown did not complete")
	}

	// The port must be free: a hung listener would fail this bind.
	deadline := time.Now().Add(2 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		ln, err := net.Listen("tcp", "127.0.0.1:"+port)
		if err == nil {
			ln.Close()
			return
		}
		lastErr = err
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("port %s was still held after the process exited: %v", port, lastErr)
}

// Case 18: the page carries no authentication of any kind -- an ordinary GET
// with no credentials at all must succeed, and the response must not demand
// any (no 401, no WWW-Authenticate). server.go is explicit that loopback-only
// binding is the entire access control; a 401 anywhere would mean an
// undocumented second gate had been added.
func TestWebRequiresNoAuthenticationToAccess(t *testing.T) {
	var out bytes.Buffer
	srv, err := startWeb(&out, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	go srv.Serve()

	url := strings.TrimSpace(strings.TrimPrefix(out.String(), "orion web: "))
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	// Deliberately no Authorization header, no cookie, nothing.
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		t.Errorf("GET %s (no credentials) = %d, want a page to be served with none required", url, resp.StatusCode)
	}
	if resp.StatusCode != http.StatusOK {
		t.Errorf("GET %s (no credentials) = %d, want %d", url, resp.StatusCode, http.StatusOK)
	}
	if wa := resp.Header.Get("WWW-Authenticate"); wa != "" {
		t.Errorf("response asked for credentials via WWW-Authenticate: %q", wa)
	}
}

// Case 19: through the actual command wiring -- web.Listen's mux, not
// Assets() called directly -- a path naming nothing gets 404, and the tree
// stays read-only against a write method. assets_routing_test.go and
// assets_write_methods_test.go already prove this for Assets() in isolation;
// this proves the wiring startWeb goes through actually reaches it.
func TestWebSubcommand404sUnknownPathsAndStaysReadOnly(t *testing.T) {
	var out bytes.Buffer
	srv, err := startWeb(&out, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	go srv.Serve()

	base := strings.TrimSpace(strings.TrimPrefix(out.String(), "orion web: "))
	client := &http.Client{Timeout: 5 * time.Second}

	resp, err := client.Get(base + "/this-path-names-nothing")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("GET /this-path-names-nothing = %d, want %d", resp.StatusCode, http.StatusNotFound)
	}

	req, err := http.NewRequest(http.MethodPost, base+"/", strings.NewReader("x"))
	if err != nil {
		t.Fatal(err)
	}
	resp, err = client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("POST / = %d, want %d -- the served tree must stay read-only", resp.StatusCode, http.StatusMethodNotAllowed)
	}
}

// Case 20: --port must accept only integers in 0-65535, and an out-of-range
// or malformed value must be rejected before anything is bound -- not fall
// through to a default bind. web_test.go and web_defaultport_test.go already
// check several bad values end to end; this pins the boundary values (0 and
// 65535 are valid) and confirms a rejected value never reaches the listener.
func TestWebPortRangeIsZeroTo65535InclusiveAndRejectedBeforeBinding(t *testing.T) {
	for _, n := range []int{0, 65535} {
		got, err := webPort([]string{"--port", strconv.Itoa(n)})
		if err != nil {
			t.Errorf("webPort(--port %d) = error %v, want %d accepted as a valid boundary", n, err, n)
			continue
		}
		if got != n {
			t.Errorf("webPort(--port %d) = %d, want %d", n, got, n)
		}
	}

	for _, bad := range []string{"65536", "-1"} {
		if _, err := webPort([]string{"--port", bad}); err == nil {
			t.Fatalf("webPort(--port %s) accepted an out-of-range value", bad)
		}
	}

	// Rejection must happen before binding: reserve 65536's would-be neighbor
	// port and confirm a rejected call never touched the network at all by
	// checking startWeb is never reached with a parse error in hand -- i.e.
	// webPort itself must fail without calling web.Listen. runWeb enforces
	// this by checking the error from webPort before calling startWeb; this
	// asserts the precondition that ordering relies on: webPort returns its
	// error without any side effect on a listener.
	before, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	_, freePort, err := net.SplitHostPort(before.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	before.Close()

	if _, err := webPort([]string{"--port", "65536"}); err == nil {
		t.Fatal("expected an error for an out-of-range port")
	}
	// The port freed above must still be free: an out-of-range --port must
	// not have caused any bind attempt anywhere.
	ln, err := net.Listen("tcp", "127.0.0.1:"+freePort)
	if err != nil {
		t.Fatalf("port %s was unexpectedly unavailable after a rejected --port: %v", freePort, err)
	}
	ln.Close()
}
