// Package localauth authenticates Orion's local web surface.
//
// The surface is not read-only any more: it approves human gates, edits agent
// config and starts runs by shelling out to the CLI. Binding to 127.0.0.1 is
// not a permission boundary -- every other process running as the operator can
// reach it, and so can any website the operator visits, by CSRF or by DNS
// rebinding.
//
// Three layers, all mandatory, applied as ONE middleware over the whole mux.
// They are not alternatives; each covers a threat the others miss, and the
// table of what each one does not cover is in
// docs/decisions/0024-local-surface-authentication.md (OR-267).
//
//   - A per-process token in a custom request header. The only layer that
//     stops another LOCAL process, because a local process is not a browser
//     and sets its own Origin. A custom header cannot be set by a cross-origin
//     HTML form, so it doubles as the CSRF defence (OR-268).
//   - Origin and Sec-Fetch-Site, with the EMPTY header rejected. A check
//     shaped `if origin != "" && !loopback(origin)` fails open on exactly the
//     request an HTML-form CSRF sends (OR-269).
//   - An exact-string Host allowlist, never a suffix match and never a DNS
//     lookup on the attacker-controlled value (OR-270).
//
// The mux is guarded by default: only paths written into the read-only
// allowlist are exempt, and only for GET and HEAD. A write endpoint added
// later -- or a GET that mutates -- is protected without anyone remembering
// to protect it.
package localauth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
)

// HeaderName carries the token. A custom header is the whole CSRF defence:
// a cross-origin HTML form cannot set one, and JavaScript that tries is held
// behind a CORS preflight this server does not answer.
//
// Deliberately not a cookie (ambient authority -- the browser would attach it
// to evil.com's POST for the attacker) and deliberately not a query parameter
// (browser history, Referer, shell history, access logs).
const HeaderName = "X-Orion-Token"

// tokenBytes is 16 bytes = 128 bits, the floor OR-268 sets. The comparison is
// against a value an attacker on the same machine can guess at over a local
// socket with no round-trip cost, so the margin is not decorative.
const tokenBytes = 16

// Guard holds one server process's token and the exact strings its Host and
// Origin headers must match.
type Guard struct {
	token    string
	hosts    map[string]bool // exact host:port strings, lowercased
	origins  map[string]bool // exact scheme://host:port strings, lowercased
	readOnly map[string]bool // paths exempt from the token, GET/HEAD only
}

// New mints a token for this process and builds the allowlists for port.
//
// readOnly names the paths served without a token, exactly as registered on
// the mux. Everything else -- including a path not registered at all -- is
// denied. Adding a route is therefore not enough to expose it.
func New(port int, readOnly ...string) (*Guard, error) {
	b := make([]byte, tokenBytes)
	if _, err := rand.Read(b); err != nil {
		return nil, fmt.Errorf("minting the local surface token: %w", err)
	}

	g := &Guard{
		token:    hex.EncodeToString(b),
		hosts:    map[string]bool{},
		origins:  map[string]bool{},
		readOnly: map[string]bool{},
	}
	p := strconv.Itoa(port)
	// The bracket form is what a browser puts in Host and Origin for IPv6, and
	// what net.JoinHostPort produces.
	for _, h := range []string{
		net.JoinHostPort("127.0.0.1", p),
		net.JoinHostPort("::1", p),
		net.JoinHostPort("localhost", p),
	} {
		g.hosts[h] = true
		g.origins["http://"+h] = true
	}
	for _, path := range readOnly {
		g.readOnly[path] = true
	}
	return g, nil
}

// Token is the secret the server prints once to the launching terminal (or
// writes 0600 under the runtime dir) and inlines into the page it serves, for
// the page's own JavaScript to send back in HeaderName.
//
// It must never be echoed back by an API response: a credential rendered into
// a page is a credential that has left the store.
func (g *Guard) Token() string { return g.token }

// Middleware wraps the WHOLE mux. Per-handler checks guard the endpoints that
// exist on the day they are written; this guards the ones nobody has written
// yet, which is the regression the layer exists to prevent.
func (g *Guard) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := g.check(r); err != nil {
			http.Error(w, err.Error(), http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// check returns nil when the request may proceed. Every path through it that
// does not end in an explicit allow is a denial.
func (g *Guard) check(r *http.Request) error {
	// The Host allowlist applies to reads as well as writes. A rebound page
	// reading a run's logs is a leak even if it can write nothing.
	if !g.hostAllowed(r.Host) {
		return fmt.Errorf("localauth: Host %q is not an allowed loopback name", r.Host)
	}

	// The exemption is per path AND per method. A POST to a read-only path is
	// still a write, so it still needs the token.
	if (r.Method == http.MethodGet || r.Method == http.MethodHead) && g.readOnly[r.URL.Path] {
		return nil
	}

	if err := g.originAllowed(r); err != nil {
		return err
	}
	if subtle.ConstantTimeCompare([]byte(r.Header.Get(HeaderName)), []byte(g.token)) != 1 {
		return fmt.Errorf("localauth: missing or invalid %s header", HeaderName)
	}
	return nil
}

// hostAllowed matches the Host header against the allowlist by exact string,
// after splitting host from port.
//
// Never a suffix or substring test: `strings.HasSuffix(host, "localhost")`
// admits localhost.evil.com, and `strings.Contains(host, "127.0.0.1")` admits
// 127.0.0.1.evil.com -- both names an attacker can register and point at
// loopback.
//
// And never a DNS lookup on the value. Resolving attacker-controlled input and
// then trusting the answer is not a defence against DNS rebinding, it is a
// description of it: the attacker publishes an A record for 127.0.0.1.
func (g *Guard) hostAllowed(host string) bool {
	h, port, err := net.SplitHostPort(host)
	if err != nil {
		// No port means it cannot equal a bound-port entry anyway. The server
		// always listens on an explicit port.
		return false
	}
	// Lowercased because DNS names are case-insensitive, so a browser may send
	// "LocalHost" for a URL the operator typed that way. This widens nothing:
	// the comparison is still against the three exact names.
	return g.hosts[net.JoinHostPort(strings.ToLower(h), port)]
}

// originAllowed enforces the browser-set origin signals on a state-changing
// request, DEFAULT-DENY: an absent header is a rejection.
//
// That is the whole point of the layer. A cross-origin HTML form POST is one
// of the few browser requests sent with no Origin at all, and a non-browser
// client sends nothing unless it chooses to -- so a check that lets the empty
// case through lets both attackers through while looking like it stops them.
func (g *Guard) originAllowed(r *http.Request) error {
	origin := strings.ToLower(r.Header.Get("Origin"))
	if origin == "" {
		return fmt.Errorf("localauth: state-changing request with no Origin header")
	}
	if !g.origins[origin] {
		return fmt.Errorf("localauth: Origin %q is not a loopback origin for this server", origin)
	}
	// Fetch metadata says where the request came from in the browser's own
	// words, and the page's own fetch is the only same-origin caller there is.
	// Requiring it sets a browser floor (Chrome 76, Firefox 90, Safari 16.4),
	// which is accepted for a developer tool launched from a terminal.
	if site := r.Header.Get("Sec-Fetch-Site"); site != "same-origin" {
		return fmt.Errorf("localauth: Sec-Fetch-Site is %q, want same-origin", site)
	}
	return nil
}
