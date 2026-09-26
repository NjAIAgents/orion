package main

// `orion web` (OR-61): start the loopback server and say where it is.
//
// The server (internal/web) binds before it serves precisely so the address
// can be printed first, and this is the command that prints it. Port 0 is
// allowed and useful: the operating system picks a free port and the printed
// line reports which one it got, so a second `orion web` on a machine that
// already has one running does not have to guess a number.
//
// A BAD --port IS A USAGE ERROR, NOT A SHRUG. intFlag (main.go) returns the
// default whenever the value will not parse, which is right for a tuning knob
// and wrong here: `orion web --port 808O` would silently serve on the default
// port, print that address, and leave the operator staring at a browser tab
// pointed somewhere else. It exits 64 instead.

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/orion-sdlc/orion/internal/web"
)

// defaultWebPort is where `orion web` listens when nothing says otherwise.
//
// Deliberately not one of the numbers a project under development is likely
// to be using itself (3000, 5000, 8000, 8080, 9000): Orion runs beside the
// repository it is working, and a default that collides with the app under
// test would fail on the machines where it matters most.
const defaultWebPort = 7061

func runWeb(args []string) {
	port, err := webPort(args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "orion: %v\n", err)
		os.Exit(64)
	}
	srv, err := startWeb(os.Stdout, port)
	exitOn(err)

	// Ctrl-C ends the process, which releases the port; there is no state to
	// flush on the way out, so there is nothing for a signal handler to do.
	if err := srv.Serve(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		exitOn(err)
	}
}

// webPort reads --port, defaulting when it is absent and erroring when it is
// present but not a port.
//
// Presence is tested separately from the value, because argFlag reports a
// flag with no value after it exactly as it reports one that was never
// typed. A trailing `orion web --port` is a half-finished command, not a
// request for the default.
func webPort(args []string) (int, error) {
	given := false
	for _, a := range args {
		if a == "--port" || strings.HasPrefix(a, "--port=") {
			given = true
		}
	}
	if !given {
		return defaultWebPort, nil
	}

	s := argFlag(args, "--port", "")
	n, err := strconv.Atoi(s)
	if err != nil || n < 0 || n > 65535 {
		return 0, fmt.Errorf("usage: orion web [--port N]: %q is not a port between 0 and 65535", s)
	}
	return n, nil
}

// startWeb binds and announces, without serving -- so a test can dial the
// address that was printed and a caller keeps the handle it needs to close.
func startWeb(out io.Writer, port int) (*web.Server, error) {
	srv, err := web.Listen(port)
	if err != nil {
		return nil, err
	}
	fmt.Fprintf(out, "orion web: http://%s\n", srv.Addr())
	return srv, nil
}
