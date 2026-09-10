package web

// The gate every request passes through before any handler sees it (OR-269).
//
// DEFAULT-DENY, NOT PER-HANDLER CHECKS. A check written into each handler
// guards the handlers that exist on the day it is written. The next one --
// a write endpoint, or a GET that mutates something -- is unguarded, and
// nothing fails to tell anyone: it works, which is the problem. So the check
// wraps the WHOLE mux and refuses by default, and a route is served only
// because someone put it on the read-only allowlist by registering it with
// HandleReadOnly. Adding a page with Handle and forgetting the gate now
// produces a 403 on the first request, which is noticed, rather than an open
// endpoint, which is not.
//
// THE ABSENT ORIGIN IS A REJECTION, NOT A PASS. The tempting shape is
//
//	if origin != "" && !isLoopback(origin) { reject }
//
// which refuses a cross-origin XHR and waves through a request carrying no
// Origin at all -- and no Origin at all is exactly what an HTML form posted
// from an attacker's page has historically sent. A state-changing request
// must PROVE it came from this page; failing to say where it came from is a
// failure to prove it.

import (
	"net"
	"net/http"
	"net/url"
)

// safe reports whether method is one that only reads. Everything else is
// treated as state-changing, including methods this server has never heard
// of -- an unknown verb is not a safe one.
func safe(method string) bool {
	return method == http.MethodGet || method == http.MethodHead
}

// sameOrigin reports whether a request carries proof that it was issued by a
// page this server itself served.
//
// Sec-Fetch-Site is the browser's own answer and is not forgeable by page
// script, so it decides when present: "same-origin" passes and every other
// value ("cross-site", "same-site", "none") does not. A request without it --
// an older browser, curl, a Go client -- falls back to Origin, which must name
// this exact host and port. Neither header present means neither test passed.
func sameOrigin(r *http.Request) bool {
	if site := r.Header.Get("Sec-Fetch-Site"); site != "" {
		return site == "same-origin"
	}
	return loopbackOrigin(r.Header.Get("Origin"), r.Host)
}

// loopbackOrigin reports whether origin is this server's own origin: a
// loopback host, and the same host:port the request was addressed to.
//
// The port is half the check. http://127.0.0.1:9999 is loopback and is some
// other program on this machine; treating it as ours would let anything the
// operator happens to be running locally drive this surface.
func loopbackOrigin(origin, host string) bool {
	if origin == "" || host == "" {
		return false
	}
	u, err := url.Parse(origin)
	if err != nil || u.Host != host {
		return false
	}
	if u.Hostname() == "localhost" {
		return true
	}
	ip := net.ParseIP(u.Hostname())
	return ip != nil && ip.IsLoopback()
}

// guard wraps mux so that a request is served only if it is a read of a
// route on the read-only allowlist. readOnly holds the allowlisted patterns
// as ServeMux registered them; mux.Handler does the matching, so the gate and
// the router can never disagree about which route a URL names.
func guard(mux *http.ServeMux, readOnly map[string]bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !safe(r.Method) && !sameOrigin(r) {
			http.Error(w, "orion web: refused: a state-changing request must prove it came from this page", http.StatusForbidden)
			return
		}

		_, pattern := mux.Handler(r)
		if !readOnly[pattern] {
			http.Error(w, "orion web: refused: route is not on the read-only allowlist", http.StatusForbidden)
			return
		}

		if !safe(r.Method) {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "orion web: refused: this route is read-only", http.StatusMethodNotAllowed)
			return
		}

		mux.ServeHTTP(w, r)
	})
}
