package localauth

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const port = 8765

// A harness that answers the one question every test here asks: did the
// request reach a handler, or did the middleware stop it?
type surface struct {
	guard  *Guard
	served http.Handler
	ran    string // path of the handler that ran, empty if none did
}

func newSurface(t *testing.T, readOnly ...string) *surface {
	t.Helper()
	g, err := New(port, readOnly...)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	s := &surface{guard: g}

	mux := http.NewServeMux()
	record := func(w http.ResponseWriter, r *http.Request) { s.ran = r.URL.Path }
	mux.HandleFunc("/api/runs", record)          // read-only in most tests
	mux.HandleFunc("/api/gates/approve", record) // a write endpoint
	// Registered by a later ticket and nobody thought about auth: this is the
	// regression OR-269's default-deny exists to catch.
	mux.HandleFunc("/api/config", record)

	s.served = g.Middleware(mux)
	return s
}

// do sends a request that is valid in every respect, then applies the
// mutations a test cares about. Starting from valid and breaking one thing is
// the only way a pass proves the thing under test rather than a typo.
func (s *surface) do(method, path string, mutate ...func(*http.Request)) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, nil)
	r.Host = "127.0.0.1:8765"
	r.Header.Set("Origin", "http://127.0.0.1:8765")
	r.Header.Set("Sec-Fetch-Site", "same-origin")
	r.Header.Set(HeaderName, s.guard.Token())
	for _, m := range mutate {
		m(r)
	}
	s.ran = ""
	w := httptest.NewRecorder()
	s.served.ServeHTTP(w, r)
	return w
}

func (s *surface) mustReject(t *testing.T, w *httptest.ResponseRecorder, what string) {
	t.Helper()
	if w.Code != http.StatusForbidden {
		t.Errorf("%s: status = %d, want 403", what, w.Code)
	}
	if s.ran != "" {
		t.Errorf("%s: handler %s ran; the middleware let it through", what, s.ran)
	}
}

func (s *surface) mustAllow(t *testing.T, w *httptest.ResponseRecorder, path string) {
	t.Helper()
	if w.Code != http.StatusOK {
		t.Errorf("%s: status = %d (%s), want 200", path, w.Code, strings.TrimSpace(w.Body.String()))
	}
	if s.ran != path {
		t.Errorf("%s: handler did not run", path)
	}
}

// ---- OR-268: the token ----------------------------------------------------

// >= 128 bits, from crypto/rand, per process. A token derived from anything
// stable -- the port, the pid, a file on disk -- is a token another local
// process can compute rather than steal.
func TestTokenIsPerProcessAndAtLeast128Bits(t *testing.T) {
	a, err := New(port)
	if err != nil {
		t.Fatal(err)
	}
	b, err := New(port)
	if err != nil {
		t.Fatal(err)
	}
	if len(a.Token()) != 2*tokenBytes {
		t.Errorf("token is %d hex chars = %d bits, want %d bits",
			len(a.Token()), 4*len(a.Token()), 8*tokenBytes)
	}
	if 8*tokenBytes < 128 {
		t.Errorf("tokenBytes = %d, below the 128-bit floor OR-268 sets", tokenBytes)
	}
	if a.Token() == b.Token() {
		t.Error("two servers minted the same token; it is not per-process")
	}
}

func TestWriteEndpointRejectsAMissingOrWrongToken(t *testing.T) {
	s := newSurface(t, "/api/runs")

	s.mustAllow(t, s.do("POST", "/api/gates/approve"), "/api/gates/approve")

	s.mustReject(t, s.do("POST", "/api/gates/approve", func(r *http.Request) {
		r.Header.Del(HeaderName)
	}), "no token")

	s.mustReject(t, s.do("POST", "/api/gates/approve", func(r *http.Request) {
		r.Header.Set(HeaderName, "")
	}), "empty token")

	// One byte off. Guards against a prefix or length-only comparison.
	wrong := s.guard.Token()
	wrong = wrong[:len(wrong)-1] + map[bool]string{true: "0", false: "1"}[strings.HasSuffix(wrong, "1")]
	s.mustReject(t, s.do("POST", "/api/gates/approve", func(r *http.Request) {
		r.Header.Set(HeaderName, wrong)
	}), "token wrong in the last byte")

	// A prefix of the real token, in case a comparison ever gets written the
	// convenient way round.
	s.mustReject(t, s.do("POST", "/api/gates/approve", func(r *http.Request) {
		r.Header.Set(HeaderName, s.guard.Token()[:8])
	}), "token prefix")
}

// The two transports OR-268 forbids. A cookie is auto-attached to a cross-site
// POST, which hands evil.com the authority the token exists to withhold; a
// query string leaks into history, Referer and shell history. Presenting the
// token either way must be worth nothing.
func TestTheTokenIsAcceptedInNoTransportButItsOwnHeader(t *testing.T) {
	s := newSurface(t, "/api/runs")

	s.mustReject(t, s.do("POST", "/api/gates/approve", func(r *http.Request) {
		r.Header.Del(HeaderName)
		r.AddCookie(&http.Cookie{Name: "orion_token", Value: s.guard.Token()})
	}), "token in a cookie")

	s.mustReject(t, s.do("POST", "/api/gates/approve?token="+s.guard.Token(), func(r *http.Request) {
		r.Header.Del(HeaderName)
	}), "token in the query string")

	// Belt: even on a path the allowlist exempts, a cookie must not become a
	// second way in.
	s.mustReject(t, s.do("POST", "/api/runs", func(r *http.Request) {
		r.Header.Del(HeaderName)
		r.AddCookie(&http.Cookie{Name: "orion_token", Value: s.guard.Token()})
	}), "cookie on a read-only path")
}

// ---- OR-269: default-deny over the whole mux ------------------------------

// The regression this layer exists to prevent: a handler added later, by
// someone who never read this package, is protected without their help.
func TestARouteNotOnTheReadOnlyAllowlistRejectsAnUnauthenticatedPost(t *testing.T) {
	s := newSurface(t, "/api/runs") // /api/config is deliberately not listed

	for _, path := range []string{
		"/api/config",         // registered later, never allowlisted
		"/api/not/registered", // not on the mux at all
		"/api/gates/approve",  // a write endpoint
	} {
		s.mustReject(t, s.do("POST", path, func(r *http.Request) {
			r.Header.Del(HeaderName)
			r.Header.Del("Origin")
			r.Header.Del("Sec-Fetch-Site")
		}), "unauthenticated POST to "+path)
	}
}

// The allowlist exempts a PATH for GET and HEAD. It does not exempt the path
// from writes -- a GET-only exemption that leaked to POST would be the same
// hole with an extra step.
func TestTheAllowlistExemptsReadsOnly(t *testing.T) {
	s := newSurface(t, "/api/runs")

	noToken := func(r *http.Request) { r.Header.Del(HeaderName) }
	s.mustAllow(t, s.do("GET", "/api/runs", noToken), "/api/runs")
	s.mustAllow(t, s.do("HEAD", "/api/runs", noToken), "/api/runs")

	s.mustReject(t, s.do("POST", "/api/runs", noToken), "POST to an allowlisted path")
	s.mustReject(t, s.do("DELETE", "/api/runs", noToken), "DELETE on an allowlisted path")
	// A GET to an unlisted path is a write for all this middleware knows --
	// OR-269 names "a GET that mutates" as the case default-deny has to cover.
	s.mustReject(t, s.do("GET", "/api/config", noToken), "GET to an unlisted path")
}

// Default-deny on the EMPTY header, which is the case the intuitive
// `if origin != "" && !loopback(origin)` shape lets through -- and it is
// exactly what a cross-origin HTML form sends.
func TestAStateChangingRequestWithNoOriginIsRejected(t *testing.T) {
	s := newSurface(t, "/api/runs")

	s.mustReject(t, s.do("POST", "/api/gates/approve", func(r *http.Request) {
		r.Header.Del("Origin")
	}), "no Origin, valid token")

	s.mustReject(t, s.do("POST", "/api/gates/approve", func(r *http.Request) {
		r.Header.Set("Origin", "")
	}), "empty Origin, valid token")

	s.mustReject(t, s.do("POST", "/api/gates/approve", func(r *http.Request) {
		r.Header.Del("Sec-Fetch-Site")
	}), "no Sec-Fetch-Site, valid token")

	for _, bad := range []string{
		"http://evil.com",
		"http://localhost.evil.com:8765",
		"http://127.0.0.1.evil.com:8765",
		"http://127.0.0.1:9999",  // loopback, wrong port
		"https://127.0.0.1:8765", // not the scheme this server is reachable on
		"null",                   // a sandboxed iframe
	} {
		s.mustReject(t, s.do("POST", "/api/gates/approve", func(r *http.Request) {
			r.Header.Set("Origin", bad)
		}), "Origin "+bad)
	}

	for _, bad := range []string{"cross-site", "same-site", "none", ""} {
		s.mustReject(t, s.do("POST", "/api/gates/approve", func(r *http.Request) {
			r.Header.Set("Sec-Fetch-Site", bad)
		}), "Sec-Fetch-Site "+bad)
	}
}

// ---- OR-270: the Host allowlist -------------------------------------------

// Every name here resolves to loopback, or can be made to. That is the point:
// the allowlist must not care what a name resolves to, only whether it is one
// of the three exact strings.
func TestHostIsMatchedExactlyAndNeverResolved(t *testing.T) {
	s := newSurface(t, "/api/runs")

	rejected := []struct{ host, why string }{
		{"localhost.evil.com:8765", "a suffix test on \"localhost\" would admit this"},
		{"127.0.0.1.evil.com:8765", "a substring test on \"127.0.0.1\" would admit this"},
		{"evil.localhost:8765", "a suffix test the other way round would admit this"},
		{"localtest.me:8765", "a public name whose A record IS 127.0.0.1 -- resolving the Host would admit it"},
		{"7f000001.nip.io:8765", "the same trick, encoded; DNS rebinding is exactly this"},
		{"127.0.0.1:9999", "loopback on a port this server is not bound to"},
		{"[::1]:9999", "the same, IPv6"},
		{"127.0.0.2:8765", "loopback range, but not the bound address"},
		{"0.0.0.0:8765", "reachable from off-machine on some stacks"},
		{"127.0.0.1", "no port at all"},
		{"", "no Host header"},
	}
	for _, c := range rejected {
		// Reads are covered too: a rebound page reading a run's logs is a leak
		// even when it can write nothing.
		s.mustReject(t, s.do("GET", "/api/runs", func(r *http.Request) {
			r.Host = c.host
			r.Header.Del(HeaderName)
		}), "Host "+c.host+" -- "+c.why)

		s.mustReject(t, s.do("POST", "/api/gates/approve", func(r *http.Request) {
			r.Host = c.host
		}), "authenticated POST with Host "+c.host)
	}

	for _, good := range []string{"127.0.0.1:8765", "[::1]:8765", "localhost:8765", "LocalHost:8765"} {
		s.mustAllow(t, s.do("GET", "/api/runs", func(r *http.Request) {
			r.Host = good
			r.Header.Del(HeaderName)
		}), "/api/runs")
	}
}
