package web

import (
	"net/http"
	"testing"
)

// Case 23 (OR-269): a route registered with Handle (not HandleReadOnly)
// refuses its very first request. There is no warm-up window where the gate
// hasn't caught up yet -- routes is fixed at the pattern nobody allowlisted
// from the moment Listen builds the mux, so the FIRST request to reach it
// gets the same 403 as the hundredth. Distinct pattern from every other
// file's OR-60/OR-269 stand-in routes so this test doesn't share state with
// theirs.
func TestHandleRegisteredRouteRefusesFirstRequest(t *testing.T) {
	originalLen := len(routes)
	defer func() { routes = routes[:originalLen] }()

	Handle("/or-269-case23-handle-registered", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("reached"))
	}))

	s, err := Listen(0)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	go s.Serve()

	req, err := http.NewRequest(http.MethodGet, "http://"+s.Addr()+"/or-269-case23-handle-registered", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("first GET to a Handle-registered route = %d, want %d -- Handle is not an allowlist exemption", resp.StatusCode, http.StatusForbidden)
	}
}
