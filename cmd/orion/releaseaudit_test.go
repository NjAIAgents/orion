package main

import (
	"os"
	"os/signal"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"
)

// OR-256 and OR-257: the two holes an audit of OR-116 found in the parts of
// `orion release ship` that only matter when something goes wrong -- which is
// exactly where they were least covered.

// The log has to be open before the preflight, or every refusal writes nothing.
// A source check rather than an end-to-end run, because reaching the refusal
// path for real needs a repository with a registry binding, a remote, and a
// deliberately red CI; what breaks the property is one statement moving, and
// that is what this catches.
func TestTheEventLogOpensBeforeThePreflightRefuses(t *testing.T) {
	src := repoFile(t, "cmd", "orion", "releaseship.go")

	openAt := strings.Index(src, "channelID, log, closeLog := shipContext(")
	refuseAt := strings.Index(src, "refusal(s); nothing was promoted")
	if openAt < 0 || refuseAt < 0 {
		t.Fatalf("could not find both landmarks (log opens at %d, refuses at %d)", openAt, refuseAt)
	}
	if openAt > refuseAt {
		t.Error("shipContext opens the event log AFTER the preflight refuses, so a dirty " +
			"tree, red checks or an empty delta leave no event at all. That is the " +
			"outcome this command produces most often and the one somebody comes back " +
			"to the log to ask about.")
	}

	// And the refusals must actually be emitted, not merely emittable.
	refusalBlock := src[strings.Index(src, "for _, r := range refusals"):]
	if end := strings.Index(refusalBlock, "\n\t}"); end > 0 {
		refusalBlock = refusalBlock[:end]
	}
	if !strings.Contains(refusalBlock, "log.Emit(") {
		t.Error("the refusal loop prints to the terminal but emits no event; the terminal " +
			"is gone by the time anyone asks why it did not ship")
	}
	if !strings.Contains(refusalBlock, "events.KindBlocked") {
		t.Error("a refusal should be recorded as blocked, not as a generic note: it is a " +
			"decision Orion made, and the kind is what makes it findable")
	}
}

// A dry run is a question, not a decision not to ship. An event for it would
// read like the latter in a log somebody is scanning for why a release stopped.
func TestADryRunReturnsBeforeAnythingIsRecorded(t *testing.T) {
	src := repoFile(t, "cmd", "orion", "releaseship.go")
	dryAt := strings.Index(src, `ui.Ok(w, "dry run"`)
	startedAt := strings.Index(src, "release ship %s started on the %s channel")
	if dryAt < 0 || startedAt < 0 {
		t.Fatalf("could not find both landmarks (dry run at %d, started at %d)", dryAt, startedAt)
	}
	if dryAt > startedAt {
		t.Error("a dry run is recorded as a started release, so the log cannot tell a " +
			"question apart from an attempt")
	}
}

// OR-257. The signal handler used to be scoped to the two await loops, so an
// interrupt during releaseScript -- the cross-compile and upload, the longest
// step and the one the operator is watching -- killed the process on Go's
// default handling. Promoted, unreleased, and not one word about it.
func TestTheCutIsGuardedByAnInterruptHandler(t *testing.T) {
	src := repoFile(t, "cmd", "orion", "releaseship.go")

	guardAt := strings.Index(src, "stop := onInterrupt(")
	cutAt := strings.Index(src, "releaseScript(root, in.Version)")
	if guardAt < 0 {
		t.Fatal("the cut has no interrupt handler; ctrl-c during the upload leaves the " +
			"release branch promoted, nothing tagged, and no message at all")
	}
	if cutAt < 0 || guardAt > cutAt {
		t.Errorf("the interrupt handler is installed after the cut begins (guard at %d, "+
			"cut at %d)", guardAt, cutAt)
	}

	// The message an interrupt produces must be the one a failure produces:
	// the state they leave behind is identical, so two wordings would be two
	// descriptions of one situation.
	tail := src[guardAt:]
	if end := strings.Index(tail, "\n\tif out, err := gitIn"); end > 0 {
		tail = tail[:end]
	}
	if !strings.Contains(tail, "shipStopped(") {
		t.Error("the interrupt handler does not route through shipStopped, so it will not " +
			"print the resume command or say that the branch is promoted and unreleased")
	}
}

// The handler installs and comes back down without leaking its goroutine into
// the next command, and fn does not fire when nothing was signalled.
func TestOnInterruptFiresOnceAndStopsCleanly(t *testing.T) {
	if runtime.GOOS == "windows" {
		// Windows has no way to deliver SIGTERM to a process, oneself
		// included -- os.Process.Signal supports only Kill there. The
		// handler under test still works (os/signal translates console
		// events), but the trigger cannot be built (OR-344).
		t.Skip("no self-deliverable termination signal on Windows")
	}
	fired := make(chan struct{}, 2)
	stop := onInterrupt(func() { fired <- struct{}{} })

	p, err := os.FindProcess(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case <-fired:
	case <-time.After(2 * time.Second):
		t.Fatal("the handler did not run on SIGTERM")
	}
	stop()

	// After stop, the signal must not reach the handler again. Go's default
	// SIGTERM disposition would kill this test process, so the signal is
	// re-notified into a channel this test owns and drains.
	own := make(chan os.Signal, 1)
	signal.Notify(own, syscall.SIGTERM)
	defer signal.Stop(own)
	if err := p.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case <-own:
	case <-time.After(2 * time.Second):
		t.Fatal("the second signal was not delivered anywhere")
	}
	select {
	case <-fired:
		t.Error("the handler ran after stop(); a later command would inherit an interrupt " +
			"handler that reports a release it has nothing to do with")
	case <-time.After(200 * time.Millisecond):
	}
}
