package work

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"github.com/orion-sdlc/orion/internal/budget"
	"github.com/orion-sdlc/orion/internal/config"
)

// The observed failure, in full: `orion watch fcia` claimed FCIA-8, hit the
// 72% checkpoint, printed a wall of text, refused to start, and sent no
// Slack message at all. The only way forward was to kill the watcher, run
// `orion budget ack 50`, and start again -- and until somebody did, the loop
// re-asked the same unanswerable question every two minutes.
//
// These cover the two halves of the fix: consent, once given, must actually
// let the run through; and the absence of consent must never be inferred
// from nobody being there to give it.

func withBudget(t *testing.T, used, limit int) (home string, cfg config.Config) {
	t.Helper()
	home = t.TempDir()
	cfg.Budget.WeeklyTokens = limit

	l, err := budget.Load(home)
	if err != nil && l == nil {
		t.Fatalf("budget.Load: %v", err)
	}
	l.Record(budget.Run{Workspace: "ws", Stage: "ticket", InputTokens: used})
	if err := l.Save(home); err != nil {
		t.Fatalf("budget.Save: %v", err)
	}
	return home, cfg
}

func TestAnAcknowledgedCheckpointLetsTheRunProceed(t *testing.T) {
	// 72% of the limit, as in the real run.
	home, cfg := withBudget(t, 14_600_000, 20_000_000)

	st, ok := budgetStatus(home, cfg)
	if !ok || st.Crossed == 0 {
		t.Fatalf("expected a crossed checkpoint, got %+v", st)
	}

	if err := ackBudget(home, st.Crossed); err != nil {
		t.Fatalf("ackBudget: %v", err)
	}

	// The point of the whole feature: after consent, the SAME check clears.
	after, _ := budgetStatus(home, cfg)
	if after.Crossed != 0 {
		t.Fatalf("still blocked at %d%% after acknowledgement; consent did nothing",
			after.Crossed)
	}
}

// Consent is for one checkpoint, not the rest of the week. Acknowledging
// 50% must not silently authorise 75% -- that would turn a series of
// deliberate decisions into one blanket approval nobody remembers giving.
func TestAcknowledgementDoesNotCoverTheNextCheckpoint(t *testing.T) {
	home, cfg := withBudget(t, 11_000_000, 20_000_000) // ~55%
	st, _ := budgetStatus(home, cfg)
	if st.Crossed == 0 {
		t.Fatal("expected a checkpoint to be crossed")
	}
	if err := ackBudget(home, st.Crossed); err != nil {
		t.Fatalf("ackBudget: %v", err)
	}
	if s, _ := budgetStatus(home, cfg); s.Crossed != 0 {
		t.Fatalf("the acknowledged checkpoint still blocks: %d%%", s.Crossed)
	}

	// Spend more, crossing the next threshold.
	l, _ := budget.Load(home)
	l.Record(budget.Run{Workspace: "ws", Stage: "ticket", InputTokens: 5_000_000})
	if err := l.Save(home); err != nil {
		t.Fatalf("save: %v", err)
	}

	s, _ := budgetStatus(home, cfg)
	if s.Crossed == 0 {
		t.Error("a later checkpoint was silently pre-approved by the earlier one")
	}
}

// Nobody at the terminal is not a yes.
//
// Under `orion watch`, on a timer, in CI: stdin is not a character device
// and there is no one to ask. Reading it would hang the run rather than
// refuse it, and treating silence as consent would let a watcher spend past
// every checkpoint precisely when no human is watching.
func TestSilenceIsNotConsentToSpend(t *testing.T) {
	r, wr, err := os.Pipe() // a pipe: not a terminal
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer wr.Close()

	realStdin := os.Stdin
	os.Stdin = r
	defer func() { os.Stdin = realStdin }()

	var buf bytes.Buffer
	if askedInline(&buf, 72) {
		t.Fatal("a non-interactive stdin answered YES to spending")
	}
	if strings.Contains(buf.String(), "Continue past") {
		t.Error("prompted a terminal that is not there")
	}
}
