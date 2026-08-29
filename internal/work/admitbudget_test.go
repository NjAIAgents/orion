package work

import (
	"sync"
	"testing"
	"time"

	"github.com/orion-sdlc/orion/internal/budget"
	"github.com/orion-sdlc/orion/internal/config"
)

// admitBudget is the glue budgetGate actually calls before a run is allowed
// to spend. The admission primitive itself (reserve on dispatch, release on
// completion) is covered exhaustively in package budget; what is not covered
// anywhere else is that THIS wiring reserves and releases through it, rather
// than through budgetStatus's old read-then-start check -- which would look
// identical to a caller for a single run and only differ once two are
// concurrent. That is exactly the regression OR-184 exists to prevent, so it
// has to be caught here, at the call site, not only in the primitive.

// The gate an unconfigured budget must never invent.
func TestAdmitBudgetSkipsReservationWithNoBudgetConfigured(t *testing.T) {
	home := t.TempDir()
	_, release, set, admitted := admitBudget(home, config.Config{})
	defer release()

	if set {
		t.Fatal("set=true with no budget configured; a limit nobody set must not be invented")
	}
	if !admitted {
		t.Fatal("an unconfigured budget must never refuse a run")
	}
	if r, n := budget.Reserved(); n != 0 || r.CostUSD != 0 {
		t.Fatalf("a run with no budget configured reserved %v (%d in flight)", r, n)
	}
}

// The property OR-184 exists for, exercised at the call site work.go actually
// uses. A pre-flight read (the old budgetStatus) would admit BOTH of these:
// serially neither has spent yet, so both read $92 of $100 and both pass. It
// is only because the first call's reservation is added to the ledger's own
// spend that the second is refused.
func TestAdmitBudgetAdmitsExactlyOneWhenTheSecondWouldCrossACheckpoint(t *testing.T) {
	home := t.TempDir()
	var cfg config.Config
	cfg.Budget.WeeklyUSD = 100

	if err := budget.Update(home, func(l *budget.Ledger) {
		for i := 0; i < 46; i++ {
			l.Record(budget.Run{At: time.Now().UTC(), CostUSD: 2})
		}
		l.AckAll(90) // every checkpoint up to 90% already answered
	}); err != nil {
		t.Fatal(err)
	}

	_, releaseA, setA, admittedA := admitBudget(home, cfg)
	defer releaseA()
	if !setA || !admittedA {
		t.Fatalf("the first run was refused (set=%v admitted=%v); $92 + $2 of $100 is under every unacknowledged checkpoint", setA, admittedA)
	}

	_, releaseB, setB, admittedB := admitBudget(home, cfg)
	defer releaseB()
	if !setB {
		t.Fatal("set=false with a budget configured in cfg.Budget.WeeklyUSD")
	}
	if admittedB {
		t.Fatal("a second run was admitted: $92 spent + $2 reserved + $2 more is 96% of the budget, " +
			"past the 95% checkpoint nobody has acknowledged")
	}
}

// The same, raced -- two watcher goroutines dispatching through work.Run at
// the same instant is the actual shape of the bug, not two sequential calls
// that happened to run fast.
func TestAdmitBudgetConcurrentCallsCannotBothPassOneCheckpoint(t *testing.T) {
	home := t.TempDir()
	var cfg config.Config
	cfg.Budget.WeeklyUSD = 100
	if err := budget.Update(home, func(l *budget.Ledger) {
		for i := 0; i < 46; i++ {
			l.Record(budget.Run{At: time.Now().UTC(), CostUSD: 2})
		}
		l.AckAll(90)
	}); err != nil {
		t.Fatal(err)
	}

	const racers = 6
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
			_, release, _, ok := admitBudget(home, cfg)
			mu.Lock()
			defer mu.Unlock()
			if ok {
				admitted++
				releases = append(releases, release)
			} else {
				release()
			}
		}()
	}
	close(start)
	wg.Wait()
	// Held until every racer has had its chance to (wrongly) get through --
	// releasing inside the goroutine would let the next racer in behind an
	// already-finished "run" and prove nothing about the actual race.
	for _, r := range releases {
		defer r()
	}

	if admitted != 1 {
		t.Fatalf("%d of %d concurrent admitBudget calls were admitted; the budget affords exactly 1", admitted, racers)
	}
}

// Release must actually free the reservation, or a ticket finishing (however
// it ends) leaves the next one refused for spend that no longer exists.
func TestAdmitBudgetReleaseFreesTheReservationForTheNextRun(t *testing.T) {
	home := t.TempDir()
	var cfg config.Config
	cfg.Budget.WeeklyUSD = 100
	if err := budget.Update(home, func(l *budget.Ledger) {
		for i := 0; i < 46; i++ {
			l.Record(budget.Run{At: time.Now().UTC(), CostUSD: 2})
		}
		l.AckAll(90)
	}); err != nil {
		t.Fatal(err)
	}

	_, releaseA, _, admittedA := admitBudget(home, cfg)
	if !admittedA {
		t.Fatal("the first run was refused")
	}
	if _, _, _, ok := admitBudget(home, cfg); ok {
		t.Fatal("a second run was admitted while the first's reservation was still outstanding")
	}
	releaseA()

	if _, releaseB, _, ok := admitBudget(home, cfg); !ok {
		t.Fatal("with the first run released, the budget affords another; it was still refused")
	} else {
		releaseB()
	}
}
