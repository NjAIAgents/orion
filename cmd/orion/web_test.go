package main

// OR-61's done-when, held in place: `orion web` starts the server and prints
// the URL, `orion help` shows it, and --port overrides the default.

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/orion-sdlc/orion/internal/testproc"
)

func TestWebPortDefaultsAndIsOverriddenByTheFlag(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want int
	}{
		{"no flag", nil, defaultWebPort},
		{"other flags only", []string{"--verbose"}, defaultWebPort},
		{"separate value", []string{"--port", "9123"}, 9123},
		{"equals form", []string{"--port=9123"}, 9123},
		{"zero asks the OS", []string{"--port", "0"}, 0},
	} {
		got, err := webPort(tc.args)
		if err != nil {
			t.Errorf("%s: webPort(%v) = error %v", tc.name, tc.args, err)
			continue
		}
		if got != tc.want {
			t.Errorf("%s: webPort(%v) = %d, want %d", tc.name, tc.args, got, tc.want)
		}
	}
}

// A --port that is not a port must fail loudly. Falling back to the default
// would serve on an address the operator did not ask for and did not type,
// and the printed line would look entirely normal.
func TestWebPortRejectsAValueThatIsNotAPort(t *testing.T) {
	for _, bad := range []string{"808O", "", "-1", "65536", "8080abc", "  "} {
		if got, err := webPort([]string{"--port", bad}); err == nil {
			t.Errorf("webPort(--port %q) = %d, want a usage error", bad, got)
		}
	}
	// A trailing --port with nothing after it is a half-typed command, and
	// argFlag cannot tell it from a flag that was never there.
	if got, err := webPort([]string{"--port"}); err == nil {
		t.Errorf("webPort(--port) = %d, want a usage error", got)
	}
}

// The printed line is the whole point of the command: it must carry the
// address that was actually bound, not the one that was asked for. With
// --port 0 those differ, which is what makes this a real check.
func TestWebPrintsTheURLItActuallyBound(t *testing.T) {
	var out bytes.Buffer
	srv, err := startWeb(&out, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()

	want := "orion web: http://" + srv.Addr() + "\n"
	if got := out.String(); got != want {
		t.Errorf("printed %q, want %q", got, want)
	}
	if strings.HasSuffix(srv.Addr(), ":0") {
		t.Errorf("addr = %q: port 0 should have been resolved to a real port", srv.Addr())
	}
}

// End to end: the URL that was printed serves the page, so `orion web` is a
// command an operator can follow rather than a line of text.
func TestWebServesThePageAtThePrintedURL(t *testing.T) {
	var out bytes.Buffer
	srv, err := startWeb(&out, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	go srv.Serve()

	url := strings.TrimSpace(strings.TrimPrefix(out.String(), "orion web: "))
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s = %d, want 200", url, resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "<title>orion web</title>") {
		t.Errorf("GET %s did not serve the front end, got: %q", url, truncateStr(string(body), 200))
	}
}

// --port has to reach the listener, not merely parse. Probing for a free
// port and then binding it is racy in principle; on a loopback port the
// operating system just handed back, losing that race means something else
// grabbed it in between, which is a skip rather than a failure.
func TestWebBindsThePortItWasGiven(t *testing.T) {
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	_, port, err := net.SplitHostPort(probe.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	probe.Close()

	n, err := webPort([]string{"--port", port})
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	srv, err := startWeb(&out, n)
	if err != nil {
		t.Skipf("port %s was taken between the probe and the bind: %v", port, err)
	}
	defer srv.Close()

	if got := srv.Addr(); got != "127.0.0.1:"+port {
		t.Errorf("addr = %q, want 127.0.0.1:%s -- --port did not reach the listener", got, port)
	}
}

// The switch in main.go is the wiring this ticket is about, and no in-process
// test reaches it: `orion web` has to be TYPED at a real binary. Port 0 keeps
// it off any fixed number, so a machine already running one is not a failure.
func TestTheWebSubcommandStartsAServerAndPrintsItsURL(t *testing.T) {
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

	line, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil {
		t.Fatalf("orion web printed nothing before exiting: %v (stderr: %q)", err, stderr.String())
	}
	url, ok := strings.CutPrefix(strings.TrimSpace(line), "orion web: ")
	if !ok {
		t.Fatalf("first line was %q, want an `orion web: <url>` announcement", line)
	}

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		t.Fatalf("the printed URL %q was not serving: %v", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("GET %s = %d, want 200", url, resp.StatusCode)
	}
}

// An unparseable --port must stop the command rather than quietly serving on
// the default: exit 64, the usage code every other command here uses.
func TestTheWebSubcommandRejectsABadPort(t *testing.T) {
	bin := orionBinary(t)
	cmd := testproc.Command(t, bin, "web", "--port", "808O")
	cmd.Env = append(os.Environ(), "ORION_HOME="+t.TempDir())
	var stderr strings.Builder
	cmd.Stderr = &stderr
	err := cmd.Run()

	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 64 {
		t.Fatalf("`orion web --port 808O` = %v, want exit 64 (stderr: %q)", err, stderr.String())
	}
	if !strings.Contains(stderr.String(), "--port") {
		t.Errorf("stderr should name the flag it rejected, got: %q", stderr.String())
	}
}

// The equals form is a different code path through argFlag than a separate
// value, and it has to reach the listener the same way `--port 9123` does.
func TestWebPortEqualsFormBindsOnTheGivenAddress(t *testing.T) {
	port, err := webPort([]string{"--port=8080"})
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	srv, err := startWeb(&out, port)
	if err != nil {
		t.Skipf("port 8080 was unavailable: %v", err)
	}
	defer srv.Close()

	if got := srv.Addr(); got != "127.0.0.1:8080" {
		t.Errorf("addr = %q, want 127.0.0.1:8080", got)
	}
}

// The full pipeline: parse `--port 0`, then bind. The printed address must
// carry the port the OS actually handed back, not the 0 that was asked for.
func TestWebPortZeroThroughTheFullPipelineReportsTheRealPort(t *testing.T) {
	port, err := webPort([]string{"--port", "0"})
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	srv, err := startWeb(&out, port)
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()

	if strings.HasSuffix(srv.Addr(), ":0") {
		t.Errorf("addr = %q: --port 0 should have resolved to a real port", srv.Addr())
	}
	want := "orion web: http://" + srv.Addr() + "\n"
	if got := out.String(); got != want {
		t.Errorf("printed %q, want %q", got, want)
	}
}

// Parsing is not enough: the OS-assigned port from `--port 0` has to be one a
// client can actually reach at the address that was printed.
func TestWebPortZeroServesAtTheAssignedPort(t *testing.T) {
	port, err := webPort([]string{"--port", "0"})
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	srv, err := startWeb(&out, port)
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	go srv.Serve()

	url := strings.TrimSpace(strings.TrimPrefix(out.String(), "orion web: "))
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		t.Fatalf("the assigned-port URL %q was not serving: %v", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("GET %s = %d, want 200", url, resp.StatusCode)
	}
}

// A negative port is not a port. The whole command must stop at exit 64, the
// way every other bad flag here does, and the message has to name both the
// flag and the value so the operator knows what was rejected.
func TestTheWebSubcommandRejectsANegativePort(t *testing.T) {
	bin := orionBinary(t)
	cmd := testproc.Command(t, bin, "web", "--port", "-1")
	cmd.Env = append(os.Environ(), "ORION_HOME="+t.TempDir())
	var stderr strings.Builder
	cmd.Stderr = &stderr
	err := cmd.Run()

	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 64 {
		t.Fatalf("`orion web --port -1` = %v, want exit 64 (stderr: %q)", err, stderr.String())
	}
	if !strings.Contains(stderr.String(), "--port") {
		t.Errorf("stderr should name the flag it rejected, got: %q", stderr.String())
	}
	if !strings.Contains(stderr.String(), "-1") {
		t.Errorf("stderr should name the value it rejected, got: %q", stderr.String())
	}
}

// A command missing from the usage text is a command nobody finds.
func TestHelpShowsTheWebCommand(t *testing.T) {
	if !strings.Contains(usage, "orion web") {
		t.Error("usage does not mention `orion web`")
	}
	if !strings.Contains(usage, fmt.Sprint(defaultWebPort)) {
		t.Errorf("usage does not state the default port %d", defaultWebPort)
	}
}
