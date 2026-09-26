package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The server root with an empty path is exactly what a browser sends for
// "/" -- confirming it resolves to the placeholder here, not just under an
// explicit "/" request, is what pins the go:embed seam to the address a real
// client actually uses.
func TestEmptyPathResolvesToIndex(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://example.com", nil)
	Assets().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET (empty path) = %d, want %d", rec.Code, http.StatusOK)
	}
	if body := rec.Body.String(); !strings.Contains(body, "<title>orion web</title>") {
		t.Errorf("GET (empty path) did not serve static/index.html; body was:\n%s", body)
	}
}

// Assets() is a read-only file server: a write method against "/" must not
// be treated as a way to read (or worse, imply support for writing) the
// embedded tree.
func TestPostRootRejected(t *testing.T) {
	rec := httptest.NewRecorder()
	Assets().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/", nil))

	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST / = %d, want %d", rec.Code, http.StatusMethodNotAllowed)
	}
}

func TestPutRootRejected(t *testing.T) {
	rec := httptest.NewRecorder()
	Assets().ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/", nil))

	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("PUT / = %d, want %d", rec.Code, http.StatusMethodNotAllowed)
	}
}

func TestDeleteRootRejected(t *testing.T) {
	rec := httptest.NewRecorder()
	Assets().ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/", nil))

	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("DELETE / = %d, want %d", rec.Code, http.StatusMethodNotAllowed)
	}
}
