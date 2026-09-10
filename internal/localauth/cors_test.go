package localauth

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// ---- OR-268: the CORS-preflight half of the token's CSRF defence ---------

// The ADR's claim is specific: a custom header "cannot be set by a
// cross-origin HTML form ... and any header-setting request from JavaScript
// is subject to CORS preflight, which the surface does not answer." This
// test is the second half of that sentence -- it does not (and cannot,
// against net/http/httptest) simulate a browser refusing to send the real
// request; it proves the one fact that makes a browser refuse: the server
// never emits an Access-Control-Allow-* header, on any origin, for any
// method. No answer is what turns a preflight into a block.
func TestCrossOriginPreflightGetsNoCORSHeaders(t *testing.T) {
	s := newSurface(t, "/api/runs")

	for _, origin := range []string{"http://evil.com", "https://attacker.example", "null"} {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodOptions, "/api/gates/approve", nil)
		r.Host = "127.0.0.1:8765"
		r.Header.Set("Origin", origin)
		r.Header.Set("Access-Control-Request-Method", "POST")
		r.Header.Set("Access-Control-Request-Headers", HeaderName)
		s.served.ServeHTTP(w, r)

		for _, h := range []string{
			"Access-Control-Allow-Origin",
			"Access-Control-Allow-Headers",
			"Access-Control-Allow-Methods",
			"Access-Control-Allow-Credentials",
		} {
			if v := w.Header().Get(h); v != "" {
				t.Errorf("preflight from Origin %q: response set %s = %q; a real "+
					"answer here is what would let the browser send the actual "+
					"cross-origin request carrying %s", origin, h, v, HeaderName)
			}
		}
	}
}

// ---- the case the preflight defence exists to leave open ------------------

// The surface's own page, calling itself, is the one caller a CORS
// preflight never blocks in the first place -- same-origin requests are not
// subject to it. This is the positive case: a fetch from the page itself,
// setting the header and holding the valid token, reaches the handler.
func TestSameOriginFetchWithCustomHeaderAndValidTokenIsAllowed(t *testing.T) {
	s := newSurface(t, "/api/runs")

	w := s.do("POST", "/api/gates/approve", func(r *http.Request) {
		r.Header.Set(HeaderName, s.guard.Token())
	})
	s.mustAllow(t, w, "/api/gates/approve")
}
