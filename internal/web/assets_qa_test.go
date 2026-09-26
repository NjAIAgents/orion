package web

import (
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// The implementer's own test proves the build compiles at all, but not that
// it does so WITHOUT front-end toolchain output: static/ could in principle
// hold a build tool's compiled or bundled artifact, hiding a regression
// where the embed pattern silently started depending on one.
//
// NOT "EXACTLY ONE FILE" ANY MORE (OR-67). That was this test's original
// check, correct back when static/ held only the placeholder from OR-64.
// OR-67 added static/js/app.js: hand-written source, committed the same way
// index.html always was, with no build step between editing it and //go:embed
// picking it up -- exactly what OR-66 mandates. A file count cannot tell that
// apart from a bundler's output, so the check is now on the SHAPE of what is
// there: every tracked file must read as source a person wrote (comments,
// real line breaks, no minification), never a single unreadable line a
// toolchain produced.
func TestBuildHasNoFrontEndToolchainOutputInStatic(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	out, err := exec.Command("git", "ls-files", "static").CombinedOutput()
	if err != nil {
		t.Fatalf("git ls-files static: %v: %s", err, strings.TrimSpace(string(out)))
	}
	files := strings.Fields(string(out))
	if len(files) == 0 {
		t.Fatal("static/ tracks nothing; the placeholder from OR-64 must always be present")
	}
	for _, f := range files {
		assertReadsAsHandWrittenSource(t, f)
	}
}

// assertReadsAsHandWrittenSource rejects the shape a bundler's output has --
// one enormous line, or a line so long a person could not have typed it --
// without asserting anything about naming or directory layout, which is not
// this test's business.
func assertReadsAsHandWrittenSource(t *testing.T, gitPath string) {
	t.Helper()
	// git ls-files runs with this test's own working directory
	// (internal/web, go test's default), so the path it prints -- static/...
	// -- already resolves from here with no offset needed.
	b, err := os.ReadFile(gitPath)
	if err != nil {
		t.Fatalf("reading %s: %v", gitPath, err)
	}
	const maxHandWrittenLine = 400
	for _, line := range strings.Split(string(b), "\n") {
		if len(line) > maxHandWrittenLine {
			t.Errorf("%s has a %d-character line; that is the shape of minified or "+
				"bundled output, not source a person wrote", gitPath, len(line))
			return
		}
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
