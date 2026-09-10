package web

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// safe is checked before sameOrigin: `!safe(method) && !sameOrigin(r)`. That
// short-circuit means a GET or HEAD to an allowlisted route is served no
// matter what Origin or Sec-Fetch-Site claims -- a hostile cross-site Origin
// included. That is deliberate (a read-only allowlisted route is meant to be
// readable by a plain navigation, which carries no proof header at all), but
// it is also the one place a future edit could quietly invert: tightening the
// origin check to run unconditionally would start refusing ordinary reads.
// This pins the current, intended behavior so that regression is caught here
// rather than by a user's browser failing to load the page.
func TestGetToAllowlistedRouteIsServedRegardlessOfOrigin(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/read", nil)
	req.Host = "127.0.0.1:52341"
	req.Header.Set("Origin", "https://attacker.example")
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	guardedTestServer().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /read (hostile Origin/Sec-Fetch-Site) = %d, want %d -- a safe method should never need origin proof", rec.Code, http.StatusOK)
	}
}
