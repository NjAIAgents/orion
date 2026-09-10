package web

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// GET and HEAD are safe methods, so the gate never asks them to prove where
// they came from -- an allowlisted route must answer even to a request
// carrying neither Origin nor Sec-Fetch-Site, which is what a plain browser
// navigation or curl actually sends.

func TestGetToAllowlistedRouteWithNoOriginHeadersIsServed(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/read", nil)
	req.Host = "127.0.0.1:52341"
	// Deliberately no Origin and no Sec-Fetch-Site.
	guardedTestServer().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /read with no origin headers = %d, want %d", rec.Code, http.StatusOK)
	}
}

func TestHeadToAllowlistedRouteWithNoOriginHeadersIsServed(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodHead, "/read", nil)
	req.Host = "127.0.0.1:52341"
	// Deliberately no Origin and no Sec-Fetch-Site.
	guardedTestServer().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("HEAD /read with no origin headers = %d, want %d", rec.Code, http.StatusOK)
	}
}

// A route nobody put on the allowlist is refused for every method, not just
// writes -- default-deny means deny, and a GET or HEAD reaching an
// unallowlisted route would be exactly the silent gap OR-269 exists to close.

func TestGetToNonAllowlistedRouteIsRefused(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/write", nil)
	req.Host = "127.0.0.1:52341"
	guardedTestServer().ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("GET /write = %d, want %d", rec.Code, http.StatusForbidden)
	}
}

func TestHeadToNonAllowlistedRouteIsRefused(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodHead, "/write", nil)
	req.Host = "127.0.0.1:52341"
	guardedTestServer().ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("HEAD /write = %d, want %d", rec.Code, http.StatusForbidden)
	}
}

func TestPostToNonAllowlistedRouteIsRefused(t *testing.T) {
	rec := httptest.NewRecorder()
	guardedTestServer().ServeHTTP(rec, sameOriginRequest(http.MethodPost, "/write"))

	if rec.Code != http.StatusForbidden {
		t.Fatalf("POST /write = %d, want %d", rec.Code, http.StatusForbidden)
	}
}
