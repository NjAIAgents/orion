package budget

import (
	"sync"
	"testing"
	"time"
)

// resetReservations clears the process-wide reservation between tests. Package
// state, so a test that left one outstanding would make the next one look
// poorer than it is.
func resetReservations() {
	admitMu.Lock()
	defer admitMu.Unlock()
	reserved, inFlight = Run{}, 0
}

// spentLedger writes n runs of the given cost, so the mean per run -- what
// Admit reserves -- is exactly that cost.
func spentLedger(t *testing.T, home string, n int, usd float64) {
	t.Helper()
	if err := Update(home, func(l *Ledger) {
		for i := 0; i < n; i++ {
			l.Record(Run{At: time.Now().UTC(), CostUSD: usd})
		}
		// Every checkpoint up to 90% already answered, so the test is about
		// the 95% one and not about a threshold crossed long ago.
		l.AckAll(90)
	}); err != nil {
		t.Fatal(err)
	}
}

// The property the whole change exists for: a budget that can afford one more
// run but not two admits exactly one.
//
// Serially the old pre-flight check was right, because nothing else was
// spending. With two tickets started at once both read $92 of $100, both see
// no crossed checkpoint, and both spend -- so the 95% stop is passed by runs
// that were already going when it was read.
func TestABudgetThatAffordsOneMoreRunAdmitsExactlyOne(t *testing.T) {
	resetReservations()
	defer resetReservations()

	home := t.TempDir()
	lim := Limits{WeeklyUSD: 100}
	spentLedger(t, home, 46, 2) // $92 spent, $2 a run

	// Precondition: with nothing in flight there is no checkpoint owing. That
	// is what made the old check say yes to both.
	l, err := Load(home)
	if err != nil {
		t.Fatal(err)
	}
	if st := l.Status(lim); st.Crossed != 0 {
		t.Fatalf("the ledger alone already stops at %d%%; this test proves nothing", st.Crossed)
	}

	_, release, ok := Admit(home, lim)
	if !ok {
		t.Fatal("the first run was refused, but $92 + $2 of $100 is under every unacknowledged checkpoint")
	}
	defer release()

	st, _, ok := Admit(home, lim)
	if ok {
		t.Fatal("a second run was admitted: $92 spent + $2 reserved + $2 is 96% of the budget, " +
			"past the 95% checkpoint nobody has acknowledged")
	}
	if st.Crossed != 95 {
		t.Errorf("refused at %d%%, want the 95%% checkpoint named so it can be acknowledged", st.Crossed)
	}
}

// The same, raced. Two watcher goroutines dispatching at the same instant is
// the actual shape of the bug; a sequential test would pass against a check
// that merely happened to be slow.
func TestConcurrentAdmissionsCannotBothPassOneCheckpoint(t *testing.T) {
	resetReservations()
	defer resetReservations()

	home := t.TempDir()
	lim := Limits{WeeklyUSD: 100}
	spentLedger(t, home, 46, 2)

	const racers = 8
	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		admitted int
		releases []func()
	)
	start := make(chan struct{})
	for i := 0; i < racers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, release, ok := Admit(home, lim)
			mu.Lock()
			defer mu.Unlock()
			if ok {
				admitted++
				releases = append(releases, release)
			}
		}()
	}
	close(start)
	wg.Wait()
	for _, r := range releases {
		defer r()
	}

	if admitted != 1 {
		t.Fatalf("%d of %d concurrent runs were admitted; the budget affords exactly 1", admitted, racers)
	}
}

// Release must give the slot back, or the queue stops for a checkpoint nothing
// is spending toward -- a budget that looks permanently full.
func TestReleasingAReservationFreesTheBudgetAgain(t *testing.T) {
	resetReservations()
	defer resetReservations()

	home := t.TempDir()
	lim := Limits{WeeklyUSD: 100}
	spentLedger(t, home, 46, 2)

	_, release, ok := Admit(home, lim)
	if !ok {
		t.Fatal("the first run was refused")
	}
	if _, _, ok := Admit(home, lim); ok {
		t.Fatal("the second run should not have been admitted while the first was reserved")
	}
	release()

	if r, n := Reserved(); n != 0 || r.CostUSD != 0 {
		t.Fatalf("after release, %d run(s) still reserved (%v)", n, r)
	}
	if _, _, ok := Admit(home, lim); !ok {
		t.Fatal("with the first run finished the budget affords another; it was still refused")
	}
	resetReservations()
}

// Releasing twice must not credit the budget twice. A double release would let
// a later run through on spend that was never freed.
func TestReleasingTwiceIsHarmless(t *testing.T) {
	resetReservations()
	defer resetReservations()

	home := t.TempDir()
	lim := Limits{WeeklyUSD: 100}
	spentLedger(t, home, 10, 1)

	_, releaseA, _ := Admit(home, lim)
	_, releaseB, _ := Admit(home, lim)
	releaseA()
	releaseA()

	if _, n := Reserved(); n != 1 {
		t.Fatalf("%d reservations outstanding after a double release, want 1", n)
	}
	releaseB()
	if _, n := Reserved(); n != 0 {
		t.Fatalf("%d reservations outstanding at the end, want 0", n)
	}
}

// Estimate is measured, never invented. With no history there is no estimate
// -- which is the honest answer, and harmless, because a ledger with no runs
// has spent nothing.
func TestEstimateIsZeroWithNoHistory(t *testing.T) {
	l := &Ledger{}
	if est := l.Estimate(); est.CostUSD != 0 || est.InputTokens != 0 {
		t.Fatalf("Estimate = %v with no runs; a per-run figure must never be invented", est)
	}
}

func TestEstimateIsTheMeanOfRecentRuns(t *testing.T) {
	now := time.Now().UTC()
	l := &Ledger{Runs: []Run{
		{At: now, CostUSD: 1, InputTokens: 100, OutputTokens: 10},
		{At: now, CostUSD: 3, InputTokens: 300, OutputTokens: 30},
		// Outside the window: counted by neither the spend nor the estimate.
		{At: now.Add(-2 * Window), CostUSD: 99, InputTokens: 99999},
	}}
	est := l.Estimate()
	if est.CostUSD != 2 || est.InputTokens != 200 || est.OutputTokens != 20 {
		t.Fatalf("Estimate = %+v, want the mean of the two runs inside the window", est)
	}
}

// An unconfigured budget has never stopped a run, and admission must not start
// now: reserving against a limit nobody set would invent one.
func TestNoBudgetMeansNoCheckpoint(t *testing.T) {
	resetReservations()
	defer resetReservations()

	home := t.TempDir()
	spentLedger(t, home, 46, 2)

	_, release, ok := Admit(home, Limits{})
	if !ok {
		t.Fatal("a run was refused against a budget nobody configured")
	}
	release()
}
