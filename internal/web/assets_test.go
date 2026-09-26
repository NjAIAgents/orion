package web

import (
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"testing"
)

// OR-64's done-when, half of it: "/" serves the placeholder. The other half --
// that `go build ./...` needs no front-end toolchain -- is proved by this file
// compiling at all, since //go:embed resolves before any test runs.
func TestRootServesTheEmbeddedPlaceholder(t *testing.T) {
	rec := httptest.NewRecorder()
	Assets().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("GET / = %d, want %d", rec.Code, http.StatusOK)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("GET / Content-Type = %q, want text/html", ct)
	}
	// The marker is the page's title rather than its prose: a rewritten
	// placeholder should not fail this, a "/" that has stopped resolving to
	// index.html should.
	if body := rec.Body.String(); !strings.Contains(body, "<title>orion web</title>") {
		t.Errorf("GET / did not serve static/index.html; body was:\n%s", body)
	}
}

// A request that walks out of the embedded tree gets a 404, not a file. This
// is fs.FS semantics rather than anything this package implements, and the
// test is here so that a later hand-rolled handler -- one that joins the URL
// path onto a directory to serve OR-51's hashed asset names -- cannot quietly
// reintroduce the traversal that the embedded FS ruled out.
func TestAssetsServeNothingOutsideTheEmbeddedTree(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.URL.Path = "/../assets.go"
	Assets().ServeHTTP(rec, req)

	if rec.Code == http.StatusOK && strings.Contains(rec.Body.String(), "package web") {
		t.Errorf("a path escaping static/ served package source:\n%s", rec.Body.String())
	}
}

// The placeholder only does its job if it is IN the repository. A clone that
// lacks it does not fall back to a blank page: the package stops compiling,
// because //go:embed static matching nothing is a build error. That is not a
// theoretical way to lose it -- .gitignore ignores dist/ at every level, so a
// front-end output directory under the usual name would have been untracked
// from the first commit and green on the machine that wrote it.
func TestThePlaceholderIsTrackedByGit(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	// Paths are relative to the working directory, which under `go test` is
	// the package directory.
	cmd := exec.Command("git", "ls-files", "--error-unmatch", "static/index.html")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Errorf("internal/web/static/index.html is not tracked by git, so a fresh "+
			"clone will not compile: %v: %s", err, strings.TrimSpace(string(out)))
	}
}
