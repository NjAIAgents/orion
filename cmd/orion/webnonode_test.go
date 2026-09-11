package main

// OR-71: on a machine with no Node installed, `go build ./...` produces a
// working binary serving the real UI.
//
// "No Node installed" is checked by SCRUBBING PATH before the build runs,
// not by hoping the CI runner happens to lack one. CI's three legs already
// have no setup-node step, so a UI that needed one would fail there too --
// but only if something actually imports it. This is the honest version of
// that check the ticket itself asks for: whatever tool cache this machine
// or a laptop happens to carry, the subprocess doing the build cannot see
// node even if it exists a directory away.
//
// The go toolchain is put on the scrubbed PATH explicitly, by directory
// rather than by name, so this does not accidentally also hide go and turn
// a real pass into a false one.

import (
	"context"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestBuildAndServeWithNoNodeOnPath(t *testing.T) {
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go toolchain not on PATH")
	}
	goDir := filepath.Dir(goBin)

	repoRoot := repoRootForTest(t)

	binDir := t.TempDir()
	binPath := filepath.Join(binDir, "orion-webtest")

	// THE SCRUBBED PATH: only the go toolchain's own directory, plus the
	// bare minimum a build subprocess needs to run at all (a shell, git for
	// -ldflags version info). No path a Node install would plausibly sit on
	// -- /usr/local/bin, /opt/homebrew/bin unless that happens to be go's
	// own directory, any nvm/volta shim directory -- survives this.
	//
	// OS-SPECIFIC, because a Unix-shaped PATH ("/usr/bin:/bin") on Windows
	// is not merely wrong, it is invisible: Windows has no such directories,
	// so those two entries contribute nothing and the scrub was actually
	// stricter there than intended -- which is exactly backwards from a test
	// meant to prove a WORKING build, not the tightest possible one.
	scrubbedPath := scrubbedPathFor(goDir)

	// GOCACHE, RESOLVED, NOT ASSUMED SET. os.Getenv("GOCACHE") is empty on
	// any machine that never exported it explicitly -- which is the common
	// case, because `go env GOCACHE` falls back to an OS cache directory
	// (%LocalAppData% on Windows, ~/Library/Caches on macOS) when the env
	// var is absent. Passing that empty string through as GOCACHE= does not
	// leave the variable unset -- it OVERRIDES go's own fallback with an
	// explicit empty value, which is worse than not setting it at all: this
	// is the exact cause of the Windows failure ("GOCACHE is not defined and
	// %LocalAppData% is not defined") this comment now documents. `go env`
	// is asked instead, so the resolved path is what actually gets passed.
	gocache := goEnvOrFatal(t, goBin, "GOCACHE")

	buildCtx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	build := exec.CommandContext(buildCtx, goBin, "build", "-o", binPath, "./cmd/orion")
	build.Dir = repoRoot
	build.Env = append([]string{
		"PATH=" + scrubbedPath,
		"HOME=" + os.Getenv("HOME"),
		"GOCACHE=" + gocache,
		"GOPATH=" + os.Getenv("GOPATH"),
	}, windowsCacheEnv()...)
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build with no node on PATH failed -- OR-66's decision has been "+
			"undone somewhere: %v\n%s", err, out)
	}

	// SERVING THE REAL UI, not merely that the binary exists. A build that
	// succeeds but silently embedded an empty or stale tree would pass every
	// check above and still be the regression this ticket exists to catch.
	home := t.TempDir()
	srv := exec.Command(binPath, "web", "--port", "0")
	srv.Env = append([]string{
		"ORION_HOME=" + home,
		"PATH=" + scrubbedPath,
	}, windowsCacheEnv()...)
	stdout, err := srv.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.Start(); err != nil {
		t.Fatalf("starting the built binary: %v", err)
	}
	defer func() {
		_ = srv.Process.Kill()
		_ = srv.Wait()
	}()

	addr := readServerAddr(t, stdout)
	client := &http.Client{Timeout: 5 * time.Second}

	for _, check := range []struct {
		path string
		want string
	}{
		{"/", "orion web"},                    // the real page's <title>, not the OR-64 placeholder's
		{"/js/app.js", "listPanels"},          // OR-70's shell, not empty
		{"/js/panel-run.js", "registerPanel"}, // OR-70's registered panel
		{"/vendor/preact.module.js", ""},      // OR-66's committed runtime, served
		{"/api/snapshot", `"Cards"`},          // OR-62's real endpoint
	} {
		resp, err := client.Get("http://" + addr + check.path)
		if err != nil {
			t.Fatalf("GET %s: %v", check.path, err)
		}
		// THE WHOLE BODY, not one Read() call. A single Read on an
		// http.Response.Body is not guaranteed to fill the buffer even when
		// more data remains -- and panel-run.js is 16KB with its
		// registerPanel() call on the very last line, so a partial read
		// missed the one thing this check exists to find. io.ReadAll is what
		// this test actually means by "the response body".
		body, readErr := io.ReadAll(resp.Body)
		resp.Body.Close()
		if readErr != nil {
			t.Errorf("GET %s: reading body: %v", check.path, readErr)
			continue
		}
		if resp.StatusCode != http.StatusOK {
			t.Errorf("GET %s = %d, want 200", check.path, resp.StatusCode)
			continue
		}
		if check.want != "" && !strings.Contains(string(body), check.want) {
			t.Errorf("GET %s did not contain %q; got %d bytes", check.path, check.want, len(body))
		}
	}
}

// scrubbedPathFor builds the PATH the build and serve subprocesses run
// under: only the go toolchain's own directory, plus the OS's own minimal
// shell/system directories. No Node install's directory can plausibly be
// among these, on either OS.
func scrubbedPathFor(goDir string) string {
	sep := string(os.PathListSeparator)
	if runtime.GOOS == "windows" {
		// System32 and the Windows directory: what a Go-built binary and
		// `go build` itself need for basic process and network calls on
		// Windows. Neither is a plausible home for a Node install.
		sysroot := os.Getenv("SystemRoot")
		if sysroot == "" {
			sysroot = `C:\Windows`
		}
		return strings.Join([]string{goDir, sysroot + `\System32`, sysroot}, sep)
	}
	return strings.Join([]string{goDir, "/usr/bin", "/bin"}, sep)
}

// windowsCacheEnv passes through the one Windows-specific variable go and a
// built Go binary both need and this test does not otherwise set:
// %LocalAppData%, which go env GOCACHE falls back to computing from when
// GOCACHE itself is not exported. Empty on every other OS, where HOME
// already covers the equivalent role.
func windowsCacheEnv() []string {
	if runtime.GOOS != "windows" {
		return nil
	}
	if v := os.Getenv("LocalAppData"); v != "" {
		return []string{"LocalAppData=" + v}
	}
	return nil
}

// goEnvOrFatal resolves a `go env` variable through the go toolchain itself
// rather than through this process's own environment -- os.Getenv would
// return empty on any machine that never exported the variable explicitly,
// which is the common case, and passing that empty string through as an
// override is worse than leaving the variable unset: it was the exact cause
// of a real CI failure ("GOCACHE is not defined and %LocalAppData% is not
// defined") that this function exists to stop repeating.
func goEnvOrFatal(t *testing.T, goBin, name string) string {
	t.Helper()
	out, err := exec.Command(goBin, "env", name).Output()
	if err != nil {
		t.Fatalf("go env %s: %v", name, err)
	}
	return strings.TrimSpace(string(out))
}

// repoRootForTest finds the module root by walking up from this test file's
// own directory to the first go.mod -- t.Helper()'s caller runs from
// whatever GOPATH/module cache go test uses, not necessarily the checkout
// root, so this cannot assume "." is the repo.
func repoRootForTest(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("could not determine this test file's own path")
	}
	dir := filepath.Dir(file)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no go.mod found walking up from " + file)
		}
		dir = parent
	}
}

// readServerAddr reads the one line `orion web` prints on startup --
// "orion web: http://127.0.0.1:PORT" -- and returns the host:port, waiting
// briefly rather than racing the subprocess's first write.
func readServerAddr(t *testing.T, stdout interface{ Read([]byte) (int, error) }) string {
	t.Helper()
	buf := make([]byte, 256)
	deadline := time.Now().Add(5 * time.Second)
	var collected string
	for time.Now().Before(deadline) {
		n, err := stdout.Read(buf)
		if n > 0 {
			collected += string(buf[:n])
			if i := strings.Index(collected, "http://"); i >= 0 {
				rest := collected[i+len("http://"):]
				if j := strings.IndexAny(rest, "\n\r"); j >= 0 {
					return strings.TrimSpace(rest[:j])
				}
			}
		}
		if err != nil {
			break
		}
	}
	t.Fatalf("did not see the server's own address on stdout within the deadline; got: %q", collected)
	return ""
}
