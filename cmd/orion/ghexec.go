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

// pushTimeout bounds `git push`, the other network call `orion watch` makes
// on its own hot path.
//
// Separate from, and far longer than, ghTimeout because a push is not a REST
// read: it transfers objects, and a first push of a large branch over a slow
// link legitimately takes minutes. The bound exists for the case that is not
// slowness at all -- a credential helper waiting on a prompt nobody will
// answer, or a connection that has silently gone away -- where without it the
// watcher waits forever with nothing on the console (OR-128).
//
// A var, not a const, so a test can shrink it.
var pushTimeout = 10 * time.Minute

// gitCommand builds a `git` invocation bounded by pushTimeout, for the
// network-touching git calls inside `orion watch`'s loop. Local git commands
// do not need this: they cannot hang on a remote.
func gitCommand(dir string, args ...string) (*exec.Cmd, context.CancelFunc) {
	ctx, cancel := context.WithTimeout(context.Background(), pushTimeout)
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	return cmd, cancel
}
