package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A mux with one route on the read-only allowlist ("/read") and one that
// registered itself without asking for anything ("/write") -- the shape of
// the regression OR-269 exists to prevent, where a handler added later is
// reachable because nobody remembered to guard it. Built locally rather than
// through Handle/Listen so these cases do not depend on, or disturb, the
// package-level route list every other test in this package shares.
func guardedTestServer() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/read", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("read"))
	}))
	mux.Handle("/write", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("written"))
	}))
	return guard(mux, map[string]bool{"/read": true})
}

// sameOriginRequest is what a browser on this page actually sends: the
// header set the gate is entitled to trust.
func sameOriginRequest(method, target string) *http.Request {
	r := httptest.NewRequest(method, target, strings.NewReader(""))
	r.Host = "127.0.0.1:52341"
	r.Header.Set("Origin", "http://127.0.0.1:52341")
	return r
}

// OR-269's done-when, first half. The POST is same-origin and otherwise
// unremarkable -- the ONLY thing wrong with it is that /write never went on
// the read-only allowlist. Per-handler checks would serve this, because
// /write's handler contains no check; default-deny refuses it.
func TestPostToRouteNotOnAllowlistIsRefused(t *testing.T) {
	rec := httptest.NewRecorder()
	guardedTestServer().ServeHTTP(rec, sameOriginRequest(http.MethodPost, "/write"))

	if rec.Code != http.StatusForbidden {
		t.Fatalf("POST /write = %d, want %d -- a route nobody allowlisted accepted a write", rec.Code, http.StatusForbidden)
	}
	if body := rec.Body.String(); !strings.Contains(body, "read-only allowlist") {
		t.Errorf("POST /write was refused for the wrong reason: %q", body)
	}
	if strings.Contains(rec.Body.String(), "written") {
		t.Error("POST /write reached the handler")
	}
}

// The same route, read rather than written: default-deny means DENY, not
// "deny writes". A page that is not on the allowlist is not served at all,
// which is what makes forgetting to allowlist one visible immediately.
func TestGetToRouteNotOnAllowlistIsRefused(t *testing.T) {
	rec := httptest.NewRecorder()
	guardedTestServer().ServeHTTP(rec, sameOriginRequest(http.MethodGet, "/write"))

	if rec.Code != http.StatusForbidden {
		t.Errorf("GET /write = %d, want %d", rec.Code, http.StatusForbidden)
	}
}

// OR-269's done-when, second half. No Origin, no Sec-Fetch-Site: the request
// says nothing about where it came from, which is precisely what an HTML form
// posted from an attacker's page sends. It must be refused as unproven, not
// waved through for lack of evidence against it.
func TestStateChangingRequestWithNoOriginIsRefused(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/read", strings.NewReader(""))
	req.Host = "127.0.0.1:52341"
	// Deliberately no Origin and no Sec-Fetch-Site.
	guardedTestServer().ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("POST /read with no Origin = %d, want %d -- the empty header case fell open", rec.Code, http.StatusForbidden)
	}
	// Refused for being unproven, not merely for being a write to a read-only
	// route: if the origin check were dropped, the route rules alone would
	// still refuse this and the case would prove nothing.
	if body := rec.Body.String(); !strings.Contains(body, "came from this page") {
		t.Errorf("POST /read with no Origin was refused for the wrong reason: %q", body)
	}
}

// A read of an allowlisted route is the ordinary case and must still work,
// with no credentials of any kind -- loopback binding plus this gate is the
// access control, and a gate that refuses the page it exists to protect has
// simply broken `orion web`.
func TestGetToAllowlistedRouteIsServed(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/read", nil)
	req.Host = "127.0.0.1:52341"
	guardedTestServer().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /read = %d, want %d", rec.Code, http.StatusOK)
	}
	if got := rec.Body.String(); got != "read" {
		t.Errorf("GET /read body = %q, want %q", got, "read")
	}
}

// Read-only means read-only: an allowlisted route refuses a write even when
// the request proves it came from this page. Otherwise a route allowlisted
// today because it only reads would silently start accepting writes the day
// its handler grew a POST branch.
func TestWriteToAllowlistedRouteIsRefused(t *testing.T) {
	rec := httptest.NewRecorder()
	guardedTestServer().ServeHTTP(rec, sameOriginRequest(http.MethodPost, "/read"))

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST /read (same-origin) = %d, want %d", rec.Code, http.StatusMethodNotAllowed)
	}
	if allow := rec.Header().Get("Allow"); allow != "GET, HEAD" {
		t.Errorf("Allow = %q, want %q", allow, "GET, HEAD")
	}
}

// What the gate accepts as proof of origin, one header set per case. Every
// row that expects false is a request that would reach a handler if the
// check were written the fail-open way.
func TestSameOriginAcceptsOnlyProof(t *testing.T) {
	const host = "127.0.0.1:52341"

	cases := []struct {
		name      string
		origin    string
		fetchSite string
		want      bool
	}{
		{"nothing at all", "", "", false},
		{"origin is this server", "http://" + host, "", true},
		{"origin is localhost by name on this port", "http://localhost:52341", "", false},
		{"origin is another local program", "http://127.0.0.1:9999", "", false},
		{"origin is a remote site", "https://attacker.example", "", false},
		{"origin is the literal null", "null", "", false},
		{"origin is loopback with no port", "http://127.0.0.1", "", false},
		{"browser says same-origin", "", "same-origin", true},
		{"browser says cross-site", "http://" + host, "cross-site", false},
		{"browser says same-site", "http://" + host, "same-site", false},
		{"browser says none", "", "none", false},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "/read", nil)
			r.Host = host
			if c.origin != "" {
				r.Header.Set("Origin", c.origin)
			}
			if c.fetchSite != "" {
				r.Header.Set("Sec-Fetch-Site", c.fetchSite)
			}
			if got := sameOrigin(r); got != c.want {
				t.Errorf("sameOrigin(Origin=%q, Sec-Fetch-Site=%q) = %v, want %v", c.origin, c.fetchSite, got, c.want)
			}
		})
	}
}

// A method nobody here has heard of is state-changing until proven otherwise
// -- the gate keys off a closed list of safe methods, not an open list of
// unsafe ones.
func TestUnknownMethodIsTreatedAsStateChanging(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("FROBNICATE", "/read", nil)
	req.Host = "127.0.0.1:52341"
	guardedTestServer().ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Errorf("FROBNICATE /read with no Origin = %d, want %d", rec.Code, http.StatusForbidden)
	}
}

// Through Listen, not the guard in isolation: the gate is on the server's
// handler chain, so nothing registered can be reached beside it. Registering
// with Handle (rather than HandleReadOnly) is how a page added later arrives
// by default, and it must not be served.
func TestListenRefusesARouteRegisteredWithoutTheAllowlist(t *testing.T) {
	originalLen := len(routes)
	defer func() { routes = routes[:originalLen] }()

	Handle("/or-269-not-allowlisted", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("reached"))
	}))

	s, err := Listen(0)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	go s.Serve()

	for _, method := range []string{http.MethodGet, http.MethodPost} {
		req, err := http.NewRequest(method, "http://"+s.Addr()+"/or-269-not-allowlisted", nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Origin", "http://"+s.Addr())
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("%s: %v", method, err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("%s /or-269-not-allowlisted = %d, want %d -- a route registered with Handle was served without being allowlisted", method, resp.StatusCode, http.StatusForbidden)
		}
	}
}
