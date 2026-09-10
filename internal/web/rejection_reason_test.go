package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Case 24 (OR-269): the gate can refuse a request for three different
// reasons -- the route isn't on the allowlist at all, a write didn't prove
// where it came from, or the route only reads -- and each answers with its
// own message. Collapsing them to one generic "forbidden" would leave
// whoever hits it guessing which of the three is true; these three bodies
// must stay distinct.
func TestRejectionMessagesDistinguishReasons(t *testing.T) {
	srv := guardedTestServer()

	allowlist := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/write", nil)
	req.Host = "127.0.0.1:52341"
	srv.ServeHTTP(allowlist, req)

	origin := httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/read", nil)
	req.Host = "127.0.0.1:52341"
	// Deliberately no Origin and no Sec-Fetch-Site.
	srv.ServeHTTP(origin, req)

	readOnly := httptest.NewRecorder()
	srv.ServeHTTP(readOnly, sameOriginRequest(http.MethodPost, "/read"))

	if allowlist.Code != http.StatusForbidden {
		t.Fatalf("GET /write = %d, want %d", allowlist.Code, http.StatusForbidden)
	}
	if origin.Code != http.StatusForbidden {
		t.Fatalf("POST /read with no origin proof = %d, want %d", origin.Code, http.StatusForbidden)
	}
	if readOnly.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST /read with origin proof = %d, want %d", readOnly.Code, http.StatusMethodNotAllowed)
	}

	allowlistBody := allowlist.Body.String()
	originBody := origin.Body.String()
	readOnlyBody := readOnly.Body.String()

	if !strings.Contains(allowlistBody, "read-only allowlist") {
		t.Errorf("allowlist-violation body = %q, want it to name the allowlist", allowlistBody)
	}
	if strings.Contains(allowlistBody, "came from this page") || strings.Contains(allowlistBody, "this route is read-only") {
		t.Errorf("allowlist-violation body = %q, bleeds into another reason", allowlistBody)
	}

	if !strings.Contains(originBody, "came from this page") {
		t.Errorf("missing-origin-proof body = %q, want it to name the missing proof", originBody)
	}
	if strings.Contains(originBody, "read-only allowlist") || strings.Contains(originBody, "this route is read-only") {
		t.Errorf("missing-origin-proof body = %q, bleeds into another reason", originBody)
	}

	if !strings.Contains(readOnlyBody, "this route is read-only") {
		t.Errorf("read-only-enforcement body = %q, want it to name the read-only rule", readOnlyBody)
	}
	if strings.Contains(readOnlyBody, "read-only allowlist") || strings.Contains(readOnlyBody, "came from this page") {
		t.Errorf("read-only-enforcement body = %q, bleeds into another reason", readOnlyBody)
	}

	if allowlistBody == originBody || allowlistBody == readOnlyBody || originBody == readOnlyBody {
		t.Error("two of the three rejection reasons produced the same message")
	}
}
