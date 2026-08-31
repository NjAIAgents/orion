package collect

import (
	"bytes"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/orion-sdlc/orion/internal/config"
	"github.com/orion-sdlc/orion/internal/slack"
)

// awaitDecision is the manual recovery path, and the reason it exists is
// worth stating plainly: `orion collect` is run by a person, at a terminal,
// precisely BECAUSE no watcher is running -- usually after a run failed
// midway. The old behaviour asked for approval in Slack and exited. The
// person then approved, promptly and correctly, and nothing read it, because
// nothing was looking. Every line of output said success.
//
// So: waiting is right here, and wrong in `orion watch`, where the next tick
// is the second pass. Both directions are tested.

// turningSlack answers "no reaction yet" for the first n reads, then approves.
// The shape of a real approval: the person is not at their desk the instant
// the request is posted.
type turningSlack struct {
	mu     sync.Mutex
	reads  int
	after  int
	postTS string
}

func (s *turningSlack) Reactions(string, string) ([]slack.Reaction, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reads++
	if s.reads <= s.after {
		return nil, nil
	}
	return []slack.Reaction{{Name: "white_check_mark", Users: []string{"UNAV"}}}, nil
}
func (s *turningSlack) Replies(string, string) ([]slack.Message, error) { return nil, nil }
func (s *turningSlack) UserName(id string) string {
	if id == "UNAV" {
		return "navjyot"
	}
	return id
}
func (s *turningSlack) PostTS(string, string) (string, error) { return s.postTS, nil }
func (s *turningSlack) React(string, string, string)          {}
func (s *turningSlack) BotID() string                         { return bot }
func (s *turningSlack) MemberID(who string) (string, error) {
	if who == "navjyot" {
		return "UNAV", nil
	}
	return "", fmt.Errorf("no Slack user is named %q", who)
}

func (s *turningSlack) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.reads
}

func waitOpts(d time.Duration) (Options, *bytes.Buffer) {
	var buf bytes.Buffer
	return Options{Out: &buf, AwaitApproval: d, Poll: time.Millisecond}, &buf
}

func cfgWith() config.Config {
	var c config.Config
	c.Slack.MergeApprovers = allow
	return c
}

func TestWaitingSeesAnApprovalThatArrivesLate(t *testing.T) {
	s := &turningSlack{after: 3}
	opts, _ := waitOpts(2 * time.Second)
	deps := Deps{Slack: s, Now: time.Now}

	d, err := awaitDecision(Request{Channel: "C1", TS: "1.1"}, "FCIA-7", cfgWith(), opts, deps, opts.Out)
	if err != nil {
		t.Fatalf("awaitDecision: %v", err)
	}
	if !d.Approved {
		t.Fatalf("the approval arrived on read %d and was not seen", s.count())
	}
	if s.count() < 2 {
		t.Errorf("read %d time(s); it returned without waiting", s.count())
	}
}

// The old behaviour, which watch still relies on: one read, no waiting.
func TestAWatcherNeverWaits(t *testing.T) {
	s := &turningSlack{after: 3}
	var buf bytes.Buffer
	opts := Options{Out: &buf} // AwaitApproval unset
	deps := Deps{Slack: s, Now: time.Now}

	d, err := awaitDecision(Request{Channel: "C1", TS: "1.1"}, "FCIA-7", cfgWith(), opts, deps, &buf)
	if err != nil {
		t.Fatalf("awaitDecision: %v", err)
	}
	if d.Approved {
		t.Error("nothing had approved yet")
	}
	if s.count() != 1 {
		t.Errorf("read %d times; a watcher must read once and move on", s.count())
	}
}

// Nobody answers. This must not merge, must not hang forever, and must say
// the request survives -- otherwise the reasonable conclusion is that giving
// up cancelled it.
func TestGivingUpLeavesTheRequestStanding(t *testing.T) {
	s := &turningSlack{after: 1 << 30} // never approves
	opts, buf := waitOpts(20 * time.Millisecond)
	deps := Deps{Slack: s, Now: time.Now}

	d, err := awaitDecision(Request{Channel: "C1", TS: "1.1"}, "FCIA-7", cfgWith(), opts, deps, buf)
	if err != nil {
		t.Fatalf("awaitDecision: %v", err)
	}
	if d.Approved || d.Rejected {
		t.Fatal("nobody answered, so there is no decision to report")
	}
	out := buf.String()
	if !strings.Contains(out, "Nothing was merged") {
		t.Errorf("the outcome must be unambiguous:\n%s", out)
	}
	if !strings.Contains(out, "orion collect FCIA-7") {
		t.Errorf("the way back must be named, with the key:\n%s", out)
	}
	if !strings.Contains(out, "Nothing was cancelled") {
		t.Errorf("a person who walked away must be told the request still stands:\n%s", out)
	}
}

// A rejection ends the wait as decisively as an approval. Sitting through
// the full timeout after somebody has said no would be absurd.
func TestARejectionEndsTheWaitImmediately(t *testing.T) {
	f := &rejectingSlack{}
	opts, _ := waitOpts(time.Minute) // would hang if the no were ignored
	deps := Deps{Slack: f, Now: time.Now}

	done := make(chan Decision, 1)
	go func() {
		d, _ := awaitDecision(Request{Channel: "C1", TS: "1.1"}, "FCIA-7", cfgWith(), opts, deps, opts.Out)
		done <- d
	}()
	select {
	case d := <-done:
		if !d.Rejected {
			t.Fatal("the rejection was not reported")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a rejection did not end the wait")
	}
}

type rejectingSlack struct{ turningSlack }

func (s *rejectingSlack) Reactions(string, string) ([]slack.Reaction, error) {
	return []slack.Reaction{{Name: "x", Users: []string{"UNAV"}}}, nil
}
