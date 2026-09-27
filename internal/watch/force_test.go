package watch

import (
	"bytes"
	"errors"
	"os"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/orion-sdlc/orion/internal/ui"
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

// TestFirstSignalNamesWhatItWaitsFor is OR-547: "after the current step"
// with no step named read as a hang for hours. The first signal names each
// running ticket with its stage, and says what a second one does now: kill
// the agents and put their tickets back in the queue.
func TestFirstSignalNamesWhatItWaitsFor(t *testing.T) {
	quiet(t)
	p := newPool(2)
	p.live["OR-193"] = true
	running.Store(p)
	ui.LiveStart("OR-193")
	t.Cleanup(func() { ui.LiveDone("OR-193", "done") })

	out := &safeBuf{}
	sig := make(chan os.Signal, 2)
	go handle(out, sig, func(int) {})
	sig <- os.Interrupt
	got := waitFor(t, out, "stopping after the current step")
	for _, want := range []string{"waiting for OR-193 (starting", "kills the agents", "back in the queue"} {
		if !strings.Contains(got, want) {
			t.Fatalf("the drain message does not say %q:\n%s", want, got)
		}
	}
}

// fakeUnclaim publishes release as the force path's unclaim for one test.
func fakeUnclaim(t *testing.T, release func(string) error) {
	t.Helper()
	unclaim.Store(&release)
	t.Cleanup(func() { unclaim.Store(nil) })
}

// TestForceQuitPutsKilledTicketsBackInTheQueue is the other half of OR-547:
// a forced stop used to leave every ticket claimed and ask a person to clear
// the label. Now it releases them itself and says so per ticket.
func TestForceQuitPutsKilledTicketsBackInTheQueue(t *testing.T) {
	quiet(t)
	p := newPool(2)
	p.live["OR-193"] = true
	p.live["OR-42"] = true
	running.Store(p)
	var released []string
	fakeUnclaim(t, func(k string) error { released = append(released, k); return nil })

	var out bytes.Buffer
	if code := forceQuit(&out, noKill); code == 0 {
		t.Fatal("a forced quit must exit non-zero")
	}
	if strings.Join(released, ",") != "OR-193,OR-42" {
		t.Fatalf("released %v, want both tickets", released)
	}
	got := out.String()
	if strings.Contains(got, "NOT released") {
		t.Fatalf("every ticket was released but the report says otherwise:\n%s", got)
	}
	if strings.Count(got, "back in the queue") != 2 {
		t.Fatalf("the report does not name each released ticket:\n%s", got)
	}
}

// TestForceQuitNamesTicketsItCouldNotRelease: a refused write is named with
// its error, and the ticket is listed as still claimed for a person.
func TestForceQuitNamesTicketsItCouldNotRelease(t *testing.T) {
	quiet(t)
	p := newPool(2)
	p.live["OR-193"] = true
	p.live["OR-42"] = true
	running.Store(p)
	fakeUnclaim(t, func(k string) error {
		if k == "OR-42" {
			return errors.New("403 from jira")
		}
		return nil
	})

	var out bytes.Buffer
	forceQuit(&out, noKill)
	got := out.String()
	for _, want := range []string{"403 from jira", "NOT released: OR-42\n"} {
		if !strings.Contains(got, want) {
			t.Fatalf("the report never says %q:\n%s", want, got)
		}
	}
}

// TestReleaseAllGivesUpOnAHungTracker: the release is a network write inside
// a signal handler. A tracker that never answers costs the limit, not the
// force, and everything unanswered is reported as still claimed.
func TestReleaseAllGivesUpOnAHungTracker(t *testing.T) {
	hang := make(chan struct{})
	defer close(hang)
	var out bytes.Buffer
	start := time.Now()
	stuck := releaseAll(&out, []string{"OR-1", "OR-2"}, func(k string) error {
		if k == "OR-2" {
			<-hang
		}
		return nil
	}, 50*time.Millisecond)
	if time.Since(start) > 2*time.Second {
		t.Fatal("releaseAll waited on a hung tracker past its limit")
	}
	if strings.Join(stuck, ",") != "OR-2" {
		t.Fatalf("stuck = %v, want only the unanswered OR-2", stuck)
	}
	if !strings.Contains(out.String(), "did not answer") {
		t.Fatalf("the timeout was not reported:\n%s", out.String())
	}
}
