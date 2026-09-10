package web

// The server behind `orion web` (OR-60): where the process listens, and how a
// page gets itself onto a URL.
//
// LOOPBACK IS NOT A DEFAULT, IT IS THE ONLY OPTION. Listen takes a port, not
// an address, so 0.0.0.0 cannot be reached by a flag, a config key or a
// typo -- there is nowhere to put one. This surface shows a run's tickets,
// its agents, its costs and its gates with no authentication in front of it,
// which is defensible exactly as long as the only machine that can open it is
// the one it runs on. A bind address that CAN be widened eventually is.
//
// A ROUTE REGISTERS ITSELF. Handle appends to a list that Listen assembles
// into a mux, so a new page is a new FILE with an init in it, never an edit
// to a switch in this one. That is the seam the epic needs: the ticket detail
// page (OR-53) and the agent roster (OR-54) can be built at the same time
// without touching a common line, so neither waits on the other and neither
// conflicts with it.

import (
	"fmt"
	"net"
	"net/http"
	"strconv"
	"time"
)

// route is one registered page: the pattern it answers on and what answers.
type route struct {
	pattern string
	handler http.Handler
}

// routes is every route registered so far, in registration order. Package
// state rather than a value threaded through, because the alternative is a
// list of every page in one file -- which is the shared switch this exists to
// avoid.
var routes []route

// Handle registers handler for pattern, in the same pattern language
// http.ServeMux uses. Call it from an init in the file that owns the handler:
//
//	func init() { Handle("/ticket/", http.HandlerFunc(ticketPage)) }
//
// Registering the same pattern twice panics when Listen builds the mux --
// at startup, where it is one obvious failure, rather than on whichever
// request happens to find the collision.
func Handle(pattern string, handler http.Handler) {
	routes = append(routes, route{pattern: pattern, handler: handler})
}

// Server is a bound loopback listener and the routes registered by the time
// it was bound.
type Server struct {
	ln  net.Listener
	srv *http.Server
}

// Listen binds 127.0.0.1:port and serves the registered routes. It does not
// start serving; Serve does, so a caller can print the address first.
//
// Port 0 asks the operating system for a free port and Addr reports which one
// it got, so a test never hardcodes a number and never loses a race with
// whatever else on the machine wanted it.
func Listen(port int) (*Server, error) {
	ln, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		return nil, fmt.Errorf("orion web: listen on 127.0.0.1:%d: %w", port, err)
	}

	mux := http.NewServeMux()
	for _, r := range routes {
		mux.Handle(r.pattern, r.handler)
	}

	return &Server{ln: ln, srv: &http.Server{
		Handler: mux,
		// A request whose headers never finish arriving would otherwise hold
		// its connection open indefinitely.
		ReadHeaderTimeout: 10 * time.Second,
	}}, nil
}

// Addr is the address actually bound, host and port -- "127.0.0.1:52341".
// This is the string to print for the operator and to dial in a test.
func (s *Server) Addr() string { return s.ln.Addr().String() }

// Serve serves until Close, then returns http.ErrServerClosed.
func (s *Server) Serve() error { return s.srv.Serve(s.ln) }

// Close stops serving and releases the port.
func (s *Server) Close() error { return s.srv.Close() }
