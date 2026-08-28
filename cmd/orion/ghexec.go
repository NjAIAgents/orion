package main

import (
	"context"
	"os/exec"
	"time"
)

// ghTimeout bounds every `gh` invocation on the watch/collect hot path.
//
// None of these calls carried a timeout before OR-128: a `gh` that hangs --
// stuck behind a proxy, waiting on its own update check, or simply slow --
// blocked the entire watch process indefinitely, with nothing on the
// console or in the event log to say why. A watcher that looks alive while
// doing nothing is worse than one that fails loudly.
//
// 45s is generous for a single REST call; `gh` itself times out its HTTP
// client well under a minute, so this exists to catch the case gh's own
// timeout does not: a hang before the request is even sent.
//
// A var, not a const, so a test can shrink it rather than waiting out the
// real budget to prove the timeout actually fires.
var ghTimeout = 45 * time.Second

// ghCommand builds a `gh` invocation bounded by ghTimeout, for every call
// site that runs inside `orion watch`'s own loop (collect's PR status,
// merge, and prune, and the fix loop's CI-log fetch). The returned cancel
// must be deferred by the caller so the timer is released once the command
// finishes, not just when it fires.
func ghCommand(dir string, args ...string) (*exec.Cmd, context.CancelFunc) {
	ctx, cancel := context.WithTimeout(context.Background(), ghTimeout)
	cmd := exec.CommandContext(ctx, "gh", args...)
	cmd.Dir = dir
	return cmd, cancel
}
