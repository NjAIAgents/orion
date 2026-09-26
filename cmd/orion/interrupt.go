package main

import (
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/orion-sdlc/orion/internal/supervisor"
)

// interruptGrace is how long a killed stage run gets to be reaped before its
// pid is named as a survivor.
const interruptGrace = 5 * time.Second

// killRunsOnInterrupt makes Ctrl-C (and SIGTERM) end the stage runs this
// process started, not just this process (OR-484).
//
// Every run leads its own process group (setNewProcessGroup) so a timeout can
// kill its whole tree. The same isolation keeps it out of the terminal's
// foreground group, so Ctrl-C reached only orion: Go's default handling exited
// it, and the claude run carried on as an orphan under PID 1 with its MCP
// servers, still editing the sandbox the operator had just stopped. `orion
// watch` already trapped this (OR-195); plan and run did not.
//
// The returned func stops listening; a caller whose own work is over should
// call it so a later Ctrl-C behaves normally.
func killRunsOnInterrupt(w io.Writer) (stop func()) {
	return listenForInterrupt(w, supervisor.KillAll, os.Exit)
}

func listenForInterrupt(w io.Writer, kill func(time.Duration) []int, exit func(int)) (stop func()) {
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	quit := make(chan struct{})
	go func() {
		select {
		case <-sig:
		case <-quit:
			return
		}
		fmt.Fprintln(w, "\ninterrupted: stopping the running agent and everything it started")
		if survivors := kill(interruptGrace); len(survivors) > 0 {
			fmt.Fprintf(w, "still running after %s -- stop these by hand: %v\n", interruptGrace, survivors)
		}
		exit(130)
	}()
	return func() {
		signal.Stop(sig)
		close(quit)
	}
}
