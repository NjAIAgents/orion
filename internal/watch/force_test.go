package watch

import (
	"bytes"
	"os"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// noKill stands in for a force that had nothing left to kill.
func noKill(time.Duration) []int { return nil }

// safeBuf is a buffer both the handler goroutine and the test can touch.
// The handler prints AFTER it sets the drain flag, so a test that waits on
// the flag and then reads the output is racing the write.
type safeBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *safeBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *safeBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// waitFor polls until want appears in the buffer, and returns everything
// written by then.
func waitFor(t *testing.T, out *safeBuf, want string) string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if got := out.String(); strings.Contains(got, want) {
			return got
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("never printed %q; got:\n%s", want, out.String())
	return ""
}

// quiet resets the package state the signal handler mutates, so one test's
// stopped watcher is not another's starting condition.
func quiet(t *testing.T) {
	t.Helper()
	t.Cleanup(func() {
		stopping.Store(false)
		running.Store(nil)
	})
}

// TestTheFirstSignalStillOnlyDrains pins the behaviour OR-195 must not
// change while fixing the second signal: one ctrl-c sets the drain flag,
// says so, and kills nothing. An agent mid-run is left to finish.
func TestTheFirstSignalStillOnlyDrains(t *testing.T) {
	quiet(t)
	out := &safeBuf{}
	sig := make(chan os.Signal, 2)
	exited := make(chan int, 1)
	go handle(out, sig, func(code int) { exited <- code })

	sig <- os.Interrupt
	got := waitFor(t, out, "stopping after the current step")
	if !stopping.Load() {
		t.Fatal("the first signal did not ask the loop to drain")
	}
	select {
	case code := <-exited:
		t.Fatalf("the first signal exited with %d; it must only drain", code)
	case <-time.After(200 * time.Millisecond):
	}
	// The old text promised "a ticket claimed with nothing running", which
	// was the opposite of the truth and read like idle state to tidy up
	// later rather than an agent still spending.
	if strings.Contains(got, "nothing running") {
		t.Fatalf("the warning still claims forcing leaves nothing running: %q", got)
	}
}

// TestTheSecondSignalForcesRatherThanDelegatingToTheDefault is the defect
// itself. The second signal used to call signal.Stop, handing SIGINT back to
// a default disposition that kills the watcher and nothing else -- leaving
// the agents running with their parent gone.
func TestTheSecondSignalForcesRatherThanDelegatingToTheDefault(t *testing.T) {
	quiet(t)
	out := &safeBuf{}
	sig := make(chan os.Signal, 2)
	exited := make(chan int, 1)
	go handle(out, sig, func(code int) { exited <- code })

	sig <- os.Interrupt
	sig <- os.Interrupt
	select {
	case code := <-exited:
		if code == 0 {
			t.Fatal("a forced quit exited 0; it must be distinguishable from a drained queue")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the second signal did not force; the watcher would have been " +
			"killed by the default disposition with its agents still running")
	}
}

// TestForceQuitKillsAndSaysWhatItLeftBehind covers the two things the force
// path owes a person who has to clean up after it: any pid that would not
// die, and the tickets still holding the claim label.
func TestForceQuitKillsAndSaysWhatItLeftBehind(t *testing.T) {
	quiet(t)
	p := newPool(2)
	p.live["OR-193"] = true
	p.live["OR-42"] = true
	running.Store(p)

	killed := 0
	var out bytes.Buffer
	code := forceQuit(&out, func(grace time.Duration) []int {
		killed++
		if grace <= 0 {
			t.Fatalf("the force path waited %s for a kill to land", grace)
		}
		return []int{2487}
	})

	if killed != 1 {
		t.Fatalf("forceQuit killed %d times; it must kill exactly once", killed)
	}
	if code == 0 {
		t.Fatal("a forced quit must exit non-zero")
	}
	got := out.String()
	for _, want := range []string{"2487", "OR-42", "OR-193", "orion-working"} {
		if !strings.Contains(got, want) {
			t.Fatalf("the force report never mentions %q:\n%s", want, got)
		}
	}
	// A survivor named without saying it is still running reads as an
	// afterthought; the point is that it is still spending now.
	if !strings.Contains(got, "did not die") {
		t.Fatalf("the surviving pid is named but not explained:\n%s", got)
	}
}

// TestForceQuitSaysSoWhenNothingWasClaimed keeps the honest case honest: a
// watcher forced while idle must not print a ticket list it invented.
func TestForceQuitSaysSoWhenNothingWasClaimed(t *testing.T) {
	quiet(t)
	var out bytes.Buffer
	forceQuit(&out, noKill)
	if !strings.Contains(out.String(), "nothing is left claimed") {
		t.Fatalf("an idle forced quit did not say the tracker is clean:\n%s", out.String())
	}
}

// TestForceQuitNamesEverySurvivingPid extends the single-survivor case: at
// max_concurrent_tickets above 1, more than one child can outlive the grace,
// and a report that only ever demonstrated one pid could still silently drop
// the rest.
func TestForceQuitNamesEverySurvivingPid(t *testing.T) {
	quiet(t)
	var out bytes.Buffer
	code := forceQuit(&out, func(time.Duration) []int { return []int{111, 222, 333} })

	if code == 0 {
		t.Fatal("a forced quit with survivors must exit non-zero")
	}
	got := out.String()
	for _, pid := range []string{"111", "222", "333"} {
		if !strings.Contains(got, pid) {
			t.Fatalf("forceQuit dropped surviving pid %s from its report:\n%s", pid, got)
		}
	}
}

// TestMixedSignalTypesStillForce covers the ticket's explicit claim that
// SIGTERM counts as EITHER signal in the protocol, not just as a matched
// pair: `kill <pid>` from another shell and a terminal ctrl-c can arrive in
// either order and must still add up to a force, since a watcher stopped by
// one operator tool and finished off by another is the realistic case, not
// two identical signals.
func TestMixedSignalTypesStillForce(t *testing.T) {
	quiet(t)
	out := &safeBuf{}
	sig := make(chan os.Signal, 2)
	exited := make(chan int, 1)
	go handle(out, sig, func(code int) { exited <- code })

	sig <- os.Interrupt
	waitFor(t, out, "stopping after the current step")
	sig <- syscall.SIGTERM
	select {
	case code := <-exited:
		if code == 0 {
			t.Fatal("a forced quit exited 0; SIGINT-then-SIGTERM must still force")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("SIGINT followed by SIGTERM did not force")
	}
}

// TestFirstSignalWarningStatesTheRiskCorrectly is the positive half of
// TestTheFirstSignalStillOnlyDrains's negative check: it is not enough that
// the old, backwards "nothing running" text is gone, the replacement must
// actually say an agent keeps running and spending, and that the ticket
// stays claimed -- the two things a person forcing a second time needs to
// know before they do it.
func TestFirstSignalWarningStatesTheRiskCorrectly(t *testing.T) {
	quiet(t)
	out := &safeBuf{}
	sig := make(chan os.Signal, 2)
	exited := make(chan int, 1)
	go handle(out, sig, func(code int) { exited <- code })

	sig <- os.Interrupt
	got := waitFor(t, out, "stopping after the current step")
	for _, want := range []string{"kills the running agents now", "leaves their tickets claimed"} {
		if !strings.Contains(got, want) {
			t.Fatalf("the drain warning does not say %q:\n%s", want, got)
		}
	}
}
