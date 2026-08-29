package budget

import (
	"sync"
	"time"
)

// Admission control, as opposed to a pre-flight check.
//
// The shipped gate read the spend and started the job. Serially that is
// correct: nothing else is spending, so what the ledger says is what is true.
// Working several tickets at once breaks the assumption rather than the code
// -- all of them read the same number, all of them pass, and then all of them
// spend. A 95% checkpoint set to protect somebody on a smaller plan is crossed
// by the runs that were already going when it was read, and the first anyone
// hears about it is the bill.
//
// So a run is admitted only if the budget covers it INCLUDING what is
// currently running: reserve on dispatch, release on completion.
//
// The reservation is in-process, which is where the concurrency is -- one
// watcher, N goroutines. Across processes the ledger itself is lock-protected
// (OR-138) and the claim label stops two watchers working one ticket, so the
// remaining cross-process gap is two watchers dispatching different tickets in
// the same second. That is the pre-existing single-run behaviour, not a
// regression introduced here.

var (
	admitMu  sync.Mutex // held across "read status, decide, reserve"
	reserved Run        // the estimated cost of every run in flight
	inFlight int
)

// Estimate is what one more run is likely to cost: the mean of the runs
// actually recorded in the window.
//
// Measured rather than configured, because a made-up per-run figure is the
// same category of invention this package's doc comment refuses for the
// provider's quota. With no history the estimate is zero -- which is the
// honest answer, and harmless, because a ledger with no runs has spent
// nothing and there is no checkpoint to sail past yet.
func (l *Ledger) Estimate() Run {
	var sum Run
	n := 0
	cutoff := time.Now().Add(-Window)
	for _, r := range l.Runs {
		if r.At.Before(cutoff) {
			continue
		}
		sum.CostUSD += r.CostUSD
		sum.InputTokens += r.InputTokens
		sum.OutputTokens += r.OutputTokens
		n++
	}
	if n == 0 {
		return Run{}
	}
	return Run{
		CostUSD:      sum.CostUSD / float64(n),
		InputTokens:  sum.InputTokens / n,
		OutputTokens: sum.OutputTokens / n,
	}
}

// Admit decides whether one more run may start, and reserves its estimated
// cost if so.
//
// The returned Status is what the caller reports: on a refusal it is the
// checkpoint to ask a human about. The returned release MUST be called when
// the run finishes, whatever the outcome -- a leaked reservation makes the
// budget look permanently fuller than it is, and the queue stops for a
// checkpoint that nothing is actually spending toward.
//
// ok is false only when a threshold is crossed and unacknowledged. Everything
// else -- an unreadable ledger, no limits set -- is the caller's to interpret
// via the returned Status, exactly as before.
func Admit(home string, lim Limits) (Status, func(), bool) {
	admitMu.Lock()
	defer admitMu.Unlock()

	l, err := Load(home)
	if err != nil && l == nil {
		return Status{}, func() {}, false
	}
	est := l.Estimate()
	// The candidate's own estimate on top of everything already in flight.
	// Leaving its own cost out would admit a run that the very next status
	// read shows as over the line.
	want := Run{
		CostUSD:      reserved.CostUSD + est.CostUSD,
		InputTokens:  reserved.InputTokens + est.InputTokens,
		OutputTokens: reserved.OutputTokens + est.OutputTokens,
	}
	st := l.StatusWith(lim, want)
	if st.Crossed != 0 {
		return st, func() {}, false
	}

	reserved, inFlight = want, inFlight+1
	var once sync.Once
	return st, func() {
		once.Do(func() {
			admitMu.Lock()
			defer admitMu.Unlock()
			inFlight--
			if inFlight <= 0 {
				// Back to nothing outstanding. Reset rather than subtract, so
				// float drift over a long-lived watcher cannot accumulate into
				// a phantom reservation that never clears.
				inFlight, reserved = 0, Run{}
				return
			}
			reserved.CostUSD -= est.CostUSD
			reserved.InputTokens -= est.InputTokens
			reserved.OutputTokens -= est.OutputTokens
		})
	}, true
}

// Reserved reports the outstanding reservation and how many runs hold it, for
// tests and for reporting. Not part of the decision.
func Reserved() (Run, int) {
	admitMu.Lock()
	defer admitMu.Unlock()
	return reserved, inFlight
}
