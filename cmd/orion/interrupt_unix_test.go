//go:build !windows

package main

// OR-484: Ctrl-C on `orion plan` / `orion run` must kill the stage runs, not
// only orion. The tree-kill itself (supervisor.KillAll) is covered by its own
// tests; this asserts the wiring: a real SIGINT reaches the handler, the runs
// are killed, and orion exits with the interrupt status.

import (
	"bytes"
	"os"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

type fakeExit struct {
	mu   sync.Mutex
	code int
	hit  chan struct{}
}

func (f *fakeExit) exit(c int) {
	f.mu.Lock()
	f.code = c
	f.mu.Unlock()
	close(f.hit)
}

func TestInterruptKillsTheRunsThenExits130(t *testing.T) {
	var out bytes.Buffer
	killed := make(chan time.Duration, 1)
	fx := &fakeExit{code: -1, hit: make(chan struct{})}
	stop := listenForInterrupt(&out, func(g time.Duration) []int { killed <- g; return []int{4242} }, fx.exit)
	defer stop()

	if err := syscall.Kill(os.Getpid(), syscall.SIGINT); err != nil {
		t.Fatal(err)
	}
	select {
	case <-fx.hit:
	case <-time.After(5 * time.Second):
		t.Fatal("SIGINT did not reach the handler")
	}
	select {
	case g := <-killed:
		if g != interruptGrace {
			t.Errorf("kill grace = %v, want %v", g, interruptGrace)
		}
	default:
		t.Fatal("orion exited without killing the stage runs -- the orphan OR-484 found")
	}
	if fx.code != 130 {
		t.Errorf("exit code = %d, want 130", fx.code)
	}
	if !strings.Contains(out.String(), "4242") {
		t.Errorf("a surviving pid was not named:\n%s", out.String())
	}
}

// Once the command's work is over, stop() hands Ctrl-C back.
func TestStoppedListenerDoesNothing(t *testing.T) {
	fx := &fakeExit{code: -1, hit: make(chan struct{})}
	stop := listenForInterrupt(&bytes.Buffer{}, func(time.Duration) []int { return nil }, fx.exit)
	stop()
	select {
	case <-fx.hit:
		t.Fatal("a stopped listener still exited")
	case <-time.After(200 * time.Millisecond):
	}
}
