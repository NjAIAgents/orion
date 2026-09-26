package web

import (
	"fmt"
	"io"
	"net/http"
	"sync"
	"testing"
	"time"
)

// A second route, registered from its own init the way a page registers
// itself (OR-53, OR-54), distinct from the one in server_test.go so these
// cases don't share state with it.
func init() {
	Handle("/or-60-routes-reach", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, "reached")
	}))
}

// A registered route's response must actually reach the client over the real
// listener -- not just get matched by the mux internally.
func TestRouteResponseReachesClient(t *testing.T) {
	s, err := Listen(0)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	go s.Serve()

	client := &http.Client{Timeout: 5 * time.Second}
	req, err := http.NewRequest(http.MethodGet, "http://"+s.Addr()+"/or-60-routes-reach", nil)
	if err != nil {
		t.Fatal(err)
	}
	setLocalAuthHeaders(req, s)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(body); got != "reached" {
		t.Errorf("body = %q, want %q", got, "reached")
	}
}

// A pattern nobody registered must 404, not panic or fall through to some
// other handler -- this is the ServeMux default, but it is the seam's job to
// preserve it rather than swallow it.
func TestUnregisteredPatternReturns404(t *testing.T) {
	s, err := Listen(0)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	go s.Serve()

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get("http://" + s.Addr() + "/no-such-route-was-ever-registered")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusNotFound)
	}
}

// Many requests to a registered route at once must all be answered -- a
// handler shared across goroutines by net/http is the baseline this checks,
// not a special property of this package, but a regression (e.g. a handler
// that stashed per-request state in a package var) would show up here.
func TestConcurrentRequestsAllHandled(t *testing.T) {
	s, err := Listen(0)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	go s.Serve()

	const n = 50
	client := &http.Client{Timeout: 5 * time.Second}
	var wg sync.WaitGroup
	errs := make(chan error, n)

	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			resp, err := client.Get("http://" + s.Addr() + "/or-60-routes-reach")
			if err != nil {
				errs <- err
				return
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				errs <- fmt.Errorf("status = %d, want %d", resp.StatusCode, http.StatusOK)
				return
			}
			body, err := io.ReadAll(resp.Body)
			if err != nil {
				errs <- err
				return
			}
			if got := string(body); got != "reached" {
				errs <- fmt.Errorf("body = %q, want %q", got, "reached")
			}
		}()
	}
	wg.Wait()
	close(errs)

	for err := range errs {
		t.Error(err)
	}
}

// Listen assembles the mux once, from whatever is in routes at that moment.
// A Handle call after Listen has already built the mux for a given Server
// must not retroactively appear on it -- otherwise two Listen calls (as in a
// test binary, where every test in this package calls Listen) could observe
// different route sets depending on init order between files, rather than
// each Server serving a fixed snapshot.
func TestRouteRegisteredAfterListenIsNotServedOnThatServer(t *testing.T) {
	s, err := Listen(0)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	go s.Serve()

	Handle("/or-60-registered-too-late", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, "too late")
	}))

	client := &http.Client{Timeout: 5 * time.Second}
	req, err := http.NewRequest(http.MethodGet, "http://"+s.Addr()+"/or-60-registered-too-late", nil)
	if err != nil {
		t.Fatal(err)
	}
	// A valid token, so this request reaches the mux -- otherwise the guard's
	// own denial of an unrecognised path would be indistinguishable from the
	// mux's 404, and this test would stop proving what it says it proves.
	setLocalAuthHeaders(req, s)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want %d -- a route registered after Listen was called reached this server's mux", resp.StatusCode, http.StatusNotFound)
	}
}
