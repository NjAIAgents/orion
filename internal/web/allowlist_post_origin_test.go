package web

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// Sec-Fetch-Site is the browser's own answer and decides when present: only
// "same-origin" passes, and a POST to an allowlisted route carrying any other
// value is refused as cross-origin -- the route being on the allowlist for
// reads is not proof of where a write came from.

func TestPostToAllowlistedRouteWithSecFetchSiteCrossSiteIsRefused(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/read", nil)
	req.Host = "127.0.0.1:52341"
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	guardedTestServer().ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("POST /read (Sec-Fetch-Site: cross-site) = %d, want %d", rec.Code, http.StatusForbidden)
	}
}

func TestPostToAllowlistedRouteWithSecFetchSiteSameSiteIsRefused(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/read", nil)
	req.Host = "127.0.0.1:52341"
	req.Header.Set("Sec-Fetch-Site", "same-site")
	guardedTestServer().ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("POST /read (Sec-Fetch-Site: same-site) = %d, want %d", rec.Code, http.StatusForbidden)
	}
}

func TestPostToAllowlistedRouteWithSecFetchSiteNoneIsRefused(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/read", nil)
	req.Host = "127.0.0.1:52341"
	req.Header.Set("Sec-Fetch-Site", "none")
	guardedTestServer().ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("POST /read (Sec-Fetch-Site: none) = %d, want %d", rec.Code, http.StatusForbidden)
	}
}

// Once Sec-Fetch-Site is absent, Origin is the fallback proof, and it must
// name this exact host AND port. A POST to an allowlisted route with a
// matching Origin clears the origin check, so what refuses it is the route's
// own read-only rule -- reported as 405 with an Allow header, not 403, because
// the gate has by then confirmed where the request came from and is only
// saying what methods this route accepts.
func TestPostToAllowlistedRouteWithMatchingOriginIsMethodNotAllowed(t *testing.T) {
	rec := httptest.NewRecorder()
	guardedTestServer().ServeHTTP(rec, sameOriginRequest(http.MethodPost, "/read"))

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST /read (Origin matches host:port) = %d, want %d", rec.Code, http.StatusMethodNotAllowed)
	}
	if allow := rec.Header().Get("Allow"); allow != "GET, HEAD" {
		t.Errorf("Allow = %q, want %q", allow, "GET, HEAD")
	}
}

// An Origin naming a different host fails the same host:port test that
// http://127.0.0.1:9999 fails against this server -- it is proof of some
// other origin, not proof of none, but the gate does not treat "some other
// origin" as an exemption from proving this one.
func TestPostToAllowlistedRouteWithDifferentHostOriginIsRefused(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/read", nil)
	req.Host = "127.0.0.1:52341"
	req.Header.Set("Origin", "https://attacker.example")
	guardedTestServer().ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("POST /read (Origin: different host) = %d, want %d", rec.Code, http.StatusForbidden)
	}
}
