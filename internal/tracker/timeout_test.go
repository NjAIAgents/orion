package tracker

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// The repro OR-128 asks for: a Jira that accepts the connection and then
// never answers.
//
// This is the shape that produced the original report -- threads parked in
// pthread_cond_wait, nothing on the console, nothing in the event log --
// because a stalled response is not a connection error. Nothing fails, so
// nothing is reported; the process simply waits, and waiting looks exactly
// like a healthy idle watcher.
func unresponsiveJira(t *testing.T) *Jira {
	t.Helper()
	// Released on cleanup so the handler goroutine does not outlive the test.
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release
	}))
	t.Cleanup(func() {
		close(release)
		srv.Close()
	})
	return &Jira{
		BaseURL: srv.URL, Email: "e", Token: "t",
		client: &http.Client{Timeout: 200 * time.Millisecond},
	}
}

func TestAnUnresponsiveJiraTimesOutRatherThanHanging(t *testing.T) {
	j := unresponsiveJira(t)

	start := time.Now()
	_, err := j.Search(JQLEq("labels", QueueLabelDefault), 25)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("a Jira that never answers must produce an error, not a nil result: " +
			"silence here is what made orion watch look alive while doing nothing (OR-128)")
	}
	if elapsed > 5*time.Second {
		t.Fatalf("the queue search waited %s on an unresponsive server; "+
			"the client timeout is not being enforced", elapsed)
	}
}

// A timeout that surfaces as "context deadline exceeded" is barely better
// than silence: it names neither the server nor how long Orion waited, and
// it is the ONLY thing that will appear on the console when a watch tick
// stops on a stalled Jira. So the message is part of the fix, not decoration.
func TestATimedOutRequestSaysWhatStalledAndForHowLong(t *testing.T) {
	j := unresponsiveJira(t)

	_, err := j.Search(JQLEq("labels", QueueLabelDefault), 25)
	if err == nil {
		t.Fatal("expected a timeout error")
	}
	for _, want := range []string{"Jira did not respond", "200ms", j.BaseURL} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the timeout error does not mention %q, so a person reading the\n"+
				"console cannot tell which call stalled:\n%v", want, err)
		}
	}
}

// Probe is the auth handshake -- the very first network call `orion doctor`
// and the provisioning path make. The issue names it explicitly: an
// indefinite hang there is indistinguishable from healthy silence.
func TestTheAuthProbeAlsoTimesOut(t *testing.T) {
	j := unresponsiveJira(t)

	start := time.Now()
	cap, err := j.Probe()
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("an unresponsive Jira must fail the probe rather than hang")
	}
	if cap.Reachable {
		t.Error("a server that never answered is not reachable")
	}
	if elapsed > 5*time.Second {
		t.Fatalf("Probe waited %s; the client timeout is not being enforced", elapsed)
	}
}

// Every Jira must carry a timeout, however it was constructed. A zero-value
// Jira previously had a nil client, and the obvious fallback --
// http.DefaultClient -- has no timeout at all, which would reintroduce the
// exact indefinite hang on any future construction path that skipped
// NewJiraFromEnv.
func TestAJiraBuiltWithoutTheConstructorStillHasATimeout(t *testing.T) {
	j := &Jira{BaseURL: "https://example.invalid", Email: "e", Token: "t"}

	if got := j.httpClient().Timeout; got != JiraTimeout {
		t.Fatalf("a Jira with no client falls back to a %s timeout, want %s "+
			"(zero means wait forever)", got, JiraTimeout)
	}
}
