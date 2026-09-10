package web

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// Case 21 (OR-269): an unknown verb is state-changing until proven otherwise
// (see TestUnknownMethodIsTreatedAsStateChanging in guard_test.go), so a
// custom method with no origin proof is refused before the allowlist is even
// consulted -- even against a route that IS on it.
func TestCustomMethodToAllowlistedRouteWithNoOriginIsRefused(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("FROBNICATE", "/read", nil)
	req.Host = "127.0.0.1:52341"
	// Deliberately no Origin and no Sec-Fetch-Site.
	guardedTestServer().ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("FROBNICATE /read with no origin = %d, want %d", rec.Code, http.StatusForbidden)
	}
}

// Case 22 (OR-269): once a custom method proves where it came from, it clears
// the origin check like any other method and reaches the route rules -- and
// an allowlisted route only ever answers GET/HEAD, so it is refused there
// too, but for a different reason: 405, not 403.
func TestCustomMethodToAllowlistedRouteWithValidOriginIsMethodNotAllowed(t *testing.T) {
	rec := httptest.NewRecorder()
	guardedTestServer().ServeHTTP(rec, sameOriginRequest("FROBNICATE", "/read"))

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("FROBNICATE /read (valid origin) = %d, want %d", rec.Code, http.StatusMethodNotAllowed)
	}
	if allow := rec.Header().Get("Allow"); allow != "GET, HEAD" {
		t.Errorf("Allow = %q, want %q", allow, "GET, HEAD")
	}
}
