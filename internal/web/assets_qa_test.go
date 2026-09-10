package web

import (
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The implementer's own test proves the build compiles at all, but not that
// it does so WITHOUT front-end toolchain output: static/ could in principle
// hold both the placeholder and a stale built bundle, hiding a regression
// where the embed pattern silently started depending on that bundle. Listing
// static/'s tracked files and asserting the placeholder is the only one
// closes that gap: a fresh clone with no JS toolchain ever run has nothing
// else to offer //go:embed.
func TestBuildHasNoFrontEndToolchainOutputInStatic(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	out, err := exec.Command("git", "ls-files", "static").CombinedOutput()
	if err != nil {
		t.Fatalf("git ls-files static: %v: %s", err, strings.TrimSpace(string(out)))
	}
	files := strings.Fields(string(out))
	if len(files) != 1 || filepath.Base(files[0]) != "index.html" {
		t.Errorf("static/ tracks %v, want only index.html; a committed build artifact "+
			"would make go build ./... depend on a front-end toolchain having run", files)
	}
}

func TestGetRootReturnsOK(t *testing.T) {
	rec := httptest.NewRecorder()
	Assets().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	if rec.Code != http.StatusOK {
		t.Errorf("GET / = %d, want %d", rec.Code, http.StatusOK)
	}
}

func TestGetRootContentTypeIsHTML(t *testing.T) {
	rec := httptest.NewRecorder()
	Assets().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("GET / Content-Type = %q, want prefix text/html", ct)
	}
}

func TestGetRootBodyContainsPlaceholderTitleMarker(t *testing.T) {
	rec := httptest.NewRecorder()
	Assets().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	if body := rec.Body.String(); !strings.Contains(body, "<title>orion web</title>") {
		t.Errorf("GET / body missing placeholder title marker; body was:\n%s", body)
	}
}
