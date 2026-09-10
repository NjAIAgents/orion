package web

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// Case 16 (OR-269): the Origin's host matches this server's host but names a
// different port. http://127.0.0.1:9999 is loopback, but a different port is
// some other program on this machine -- proving the request came from THIS
// page means matching the port too, not just the address family.
func TestPostToAllowlistedRouteWithOriginSameHostDifferentPortIsRefused(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/read", nil)
	req.Host = "127.0.0.1:52341"
	req.Header.Set("Origin", "http://127.0.0.1:9999")
	guardedTestServer().ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("POST /read (Origin same host, different port) = %d, want %d", rec.Code, http.StatusForbidden)
	}
}

// Case 17 (OR-269): Origin: null is what a sandboxed iframe, a file:// page,
// or a redirected cross-origin request sends -- it names no origin at all,
// so it cannot prove one.
func TestPostToAllowlistedRouteWithOriginNullIsRefused(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/read", nil)
	req.Host = "127.0.0.1:52341"
	req.Header.Set("Origin", "null")
	guardedTestServer().ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("POST /read (Origin: null) = %d, want %d", rec.Code, http.StatusForbidden)
	}
}

// Case 18 (OR-269): a malformed Origin URL must fail closed. url.Parse
// erroring is handled the same as a value that parses but names the wrong
// host -- refused, not treated as "couldn't check, so allow it".
func TestPostToAllowlistedRouteWithMalformedOriginIsRefused(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/read", nil)
	req.Host = "127.0.0.1:52341"
	req.Header.Set("Origin", "http://%zz")
	guardedTestServer().ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("POST /read (malformed Origin) = %d, want %d", rec.Code, http.StatusForbidden)
	}
}

// Case 19 (OR-269): PUT with valid origin proof reaches the route rules, not
// the origin check -- and an allowlisted route is read-only, so it answers
// 405 with Allow: GET, HEAD rather than accepting the write.
func TestPutToAllowlistedRouteWithValidOriginIsMethodNotAllowed(t *testing.T) {
	rec := httptest.NewRecorder()
	guardedTestServer().ServeHTTP(rec, sameOriginRequest(http.MethodPut, "/read"))

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("PUT /read (valid origin) = %d, want %d", rec.Code, http.StatusMethodNotAllowed)
	}
	if allow := rec.Header().Get("Allow"); allow != "GET, HEAD" {
		t.Errorf("Allow = %q, want %q", allow, "GET, HEAD")
	}
}

// Case 20 (OR-269): same as above for DELETE -- the read-only allowlist
// refuses every write verb identically, proof of origin or not.
func TestDeleteToAllowlistedRouteWithValidOriginIsMethodNotAllowed(t *testing.T) {
	rec := httptest.NewRecorder()
	guardedTestServer().ServeHTTP(rec, sameOriginRequest(http.MethodDelete, "/read"))

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("DELETE /read (valid origin) = %d, want %d", rec.Code, http.StatusMethodNotAllowed)
	}
	if allow := rec.Header().Get("Allow"); allow != "GET, HEAD" {
		t.Errorf("Allow = %q, want %q", allow, "GET, HEAD")
	}
}
