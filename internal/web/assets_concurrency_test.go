package web

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
)

// HEAD gets the same routing as GET, minus the body -- http.FileServer
// handles that itself, but a future hand-rolled handler that special-cases
// GET could silently drop it.
func TestHeadRootSucceeds(t *testing.T) {
	rec := httptest.NewRecorder()
	Assets().ServeHTTP(rec, httptest.NewRequest(http.MethodHead, "/", nil))

	if rec.Code != http.StatusOK {
		t.Errorf("HEAD / = %d, want %d", rec.Code, http.StatusOK)
	}
}

// A query string is not part of the path http.FileServer resolves against
// static/, so "/" with one attached still serves index.html rather than
// 404ing or choking on the unexpected input.
func TestGetRootWithQueryStringStillServesIndex(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/?foo=bar", nil)
	Assets().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("GET /?foo=bar = %d, want %d", rec.Code, http.StatusOK)
	}
}

// The embedded FS is read-only and shared across requests, so concurrent
// "/" requests should never race or return a truncated/corrupted body --
// this is what `go test -race` exists to catch.
func TestConcurrentGetRootRequestsSucceed(t *testing.T) {
	const n = 50
	var wg sync.WaitGroup
	errs := make(chan string, n)

	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rec := httptest.NewRecorder()
			Assets().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
			if rec.Code != http.StatusOK {
				errs <- "unexpected status code"
				return
			}
			if rec.Body.Len() == 0 {
				errs <- "empty body"
			}
		}()
	}
	wg.Wait()
	close(errs)

	for e := range errs {
		t.Error(e)
	}
}

// The whole point of embedding static/ is that the resulting binary carries
// its own front end -- no Go toolchain, no repo checkout, no static/ dir
// alongside it. Building the test binary here and running it from a bare
// temp dir is the closest thing to "a different machine" this package can
// prove without an actual second host.
func TestBinaryServesAssetsWithoutRepoOrToolchainPresent(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not on PATH")
	}

	dir := t.TempDir()
	bin := filepath.Join(dir, "assetscheck.test")

	build := exec.Command("go", "test", "-c", "-o", bin, ".")
	build.Dir = "."
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go test -c: %v: %s", err, out)
	}

	// Run the compiled test binary from an empty directory with no repo, no
	// static/, and no GOPATH/module cache reachable by relative path --
	// only -run this one case, so it doesn't recurse into itself.
	runDir := t.TempDir()
	cmd := exec.Command(bin, "-test.run", "TestServesPlaceholderStandalone")
	cmd.Dir = runDir
	cmd.Env = []string{"PATH=" + os.Getenv("PATH")}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("compiled test binary failed outside the repo checkout: %v: %s", err, out)
	}
}

// Not a real test case in the usual sense -- invoked by name from inside the
// compiled binary above, from a working directory with no repo, no
// static/, and no Go toolchain on PATH. If go:embed had captured a live
// filesystem reference instead of baking static/'s bytes into the binary,
// this would be the call that fails.
func TestServesPlaceholderStandalone(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.URL = &url.URL{Path: "/"}
	Assets().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET / = %d, want %d", rec.Code, http.StatusOK)
	}
	if rec.Body.Len() == 0 {
		t.Fatal("GET / returned empty body outside the repo checkout")
	}
}
