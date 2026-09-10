package web

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// Cases 6-8 (OR-269): PUT, DELETE, and PATCH are state-changing methods like
// POST, and a route that never went on the read-only allowlist refuses all of
// them the same way -- the gate keys off the allowlist, not a hardcoded list
// of "the write methods we thought of".
func TestNonAllowlistedRouteRefusesPUTDELETEPATCH(t *testing.T) {
	for _, method := range []string{http.MethodPut, http.MethodDelete, http.MethodPatch} {
		t.Run(method, func(t *testing.T) {
			rec := httptest.NewRecorder()
			guardedTestServer().ServeHTTP(rec, sameOriginRequest(method, "/write"))

			if rec.Code != http.StatusForbidden {
				t.Fatalf("%s /write = %d, want %d -- a route nobody allowlisted accepted a write", method, rec.Code, http.StatusForbidden)
			}
		})
	}
}

// Case 9 (OR-269): an allowlisted route still refuses a state-changing
// request that names neither Sec-Fetch-Site nor Origin -- an absent header is
// not proof of same-origin, it is the shape of a cross-site form post.
func TestPostToAllowlistedRouteWithNoOriginHeadersIsRefused(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/read", nil)
	req.Host = "127.0.0.1:52341"
	// Deliberately no Origin and no Sec-Fetch-Site.
	guardedTestServer().ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("POST /read with no Origin/Sec-Fetch-Site = %d, want %d", rec.Code, http.StatusForbidden)
	}
}

// Case 10 (OR-269): once Sec-Fetch-Site proves same-origin, the request
// reaches the route rules rather than the origin check -- and an allowlisted
// route answers 405 with Allow: GET, HEAD rather than serving the write.
func TestPostToAllowlistedRouteWithSecFetchSiteSameOriginIsMethodNotAllowed(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/read", nil)
	req.Host = "127.0.0.1:52341"
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	guardedTestServer().ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST /read (Sec-Fetch-Site: same-origin) = %d, want %d", rec.Code, http.StatusMethodNotAllowed)
	}
	if allow := rec.Header().Get("Allow"); allow != "GET, HEAD" {
		t.Errorf("Allow = %q, want %q", allow, "GET, HEAD")
	}
}
