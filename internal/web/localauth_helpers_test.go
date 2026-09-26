package web

// setLocalAuthHeaders is not itself a _test.go file so every test file in
// this package can share it without an import cycle -- but it exists only
// for tests (internal/localauth.HeaderName expects real browser signals
// this package's own production code never sends to itself).
//
// OR-266 put a default-deny guard in front of Listen's mux (internal/localauth):
// every request needs a valid token, a same-origin Origin, and
// Sec-Fetch-Site: same-origin unless its path was registered with
// HandleReadOnly. A test written before OR-266 that calls the mux directly
// over the real listener -- proving the OR-60 route-registration seam works,
// nothing about auth -- needs these three headers set to reach the handler
// it is actually testing, or every such test would need OR-266's own
// reasoning re-explained inline instead of pointing here.

import (
	"net/http"

	"github.com/orion-sdlc/orion/internal/localauth"
)

// setLocalAuthHeaders sets the three headers a request needs to pass s's
// guard: a valid token, a loopback Origin matching s's own address, and
// Sec-Fetch-Site: same-origin. s.Addr() is read after Listen, so the Origin
// this builds always matches the port s actually bound.
func setLocalAuthHeaders(r *http.Request, s *Server) {
	r.Header.Set(localauth.HeaderName, s.Token())
	r.Header.Set("Origin", "http://"+s.Addr())
	r.Header.Set("Sec-Fetch-Site", "same-origin")
}
