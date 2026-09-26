package web

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// A path that names nothing in static/ gets a 404, not a fallback to
// index.html -- Assets() is a plain file server, not an SPA router.
func TestNonexistentPathReturns404(t *testing.T) {
	rec := httptest.NewRecorder()
	Assets().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/nonexistent", nil))

	if rec.Code != http.StatusNotFound {
		t.Errorf("GET /nonexistent = %d, want %d", rec.Code, http.StatusNotFound)
	}
}

// fs.Sub strips the static/ prefix before serving, so the embedded path
// itself is not part of the URL space: a request for /static/index.html
// looks for a nested static/ directory that does not exist, not for the
// file this package actually serves at "/".
func TestStaticPrefixIsNotExposed(t *testing.T) {
	rec := httptest.NewRecorder()
	Assets().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/static/index.html", nil))

	if rec.Code != http.StatusNotFound {
		t.Errorf("GET /static/index.html = %d, want %d (fs.Sub already removed the prefix)", rec.Code, http.StatusNotFound)
	}
}

// A traversal path does not escape static/ to serve a file from the
// repository root or elsewhere -- fs.FS semantics reject ".." rather than
// resolving it against the real filesystem.
func TestPathTraversalDoesNotEscapeStatic(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/../anything", nil)
	Assets().ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("GET /../anything = %d, want %d", rec.Code, http.StatusNotFound)
	}
}
