# 0016: The integration state machine is the sophisticated part; the queue is channels and JSON

- Status: Accepted (by Navjyot, 2026-08-31)
- Date: 2026-08-31
- Tickets: OR-253 (the two-queue design this binds), OR-236 (batch integration), OR-95 (dependency awareness, its prerequisite)
- Related: [0004](0004-no-sqlite-file-based-storage.md) (no SQLite; files plus
  `procsafe`), [0011](0011-orion-owns-the-landing-queue.md) (Orion owns the
  landing queue), [0015](0015-ci-authority-under-a-merge-ref.md) (Orion is the
  gate under a merge ref)

## Context

OR-253 splits work into two queues: a **coding queue** that is fully parallel,
and an **integration queue** that processes one operation at a time. Agents
scale horizontally; integration does not. That asymmetry is the design.

The question raised was whether to build this on a job-queue library — Asynq,
River, Machinery — with SQLite or Postgres behind it for tickets, batches, git
SHAs and CI state, rather than on Go channels and files.

It is a fair question. Those libraries give retries, scheduling, persistence
and visibility for free, and the integration pipeline genuinely is a durable
workflow rather than a loop.

## Decision

**No job-queue library and no database.** Concurrency is Go channels.
Durable state is JSON under `ORION_HOME` through `internal/procsafe`, the same
way `merge-requests.json`, `branches.json` and `ci-fixes.json` already work.

**The sophistication goes into the integration state machine**, which records a
git SHA at every transition and refuses to merge a result that was validated
against a different base.

### Why not the libraries

The dependency argument from [0004](0004-no-sqlite-file-based-storage.md)
applies with more force, not less. That ADR rejected SQLite because
`mattn/go-sqlite3` needs cgo, which breaks the `CGO_ENABLED=0` six-target
cross-compile the release Makefile depends on: Orion ships as one static
binary, no cgo, empty `go.sum`, builds offline.

Asynq needs Redis. River needs Postgres. Machinery needs a broker. Each turns
`orion watch` from something a person runs on a laptop into something that
needs a service running first. That is a larger violation than the one 0004
already refused, for a queue whose depth is measured in tens.

Channels cost nothing and already express the shape: a `chan Ticket` fanning
out to N coding workers, a `chan Batch` consumed by exactly one integration
worker. The single-consumer constraint that makes integration correct is a
property of the channel, not something to enforce with a lock.

### Why state still has to be persisted

Channels die with the process. A batch mid-validation must survive a restart,
a crash, or a `ctrl-c` that the watcher's own banner promises is safe. So the
integration state lives in a file, and 0004's consequence clause governs it:
any new shared mutable state under `ORION_HOME` goes through `procsafe` rather
than growing a second locking scheme.

### The state machine, and the SHA at every transition

Each batch records where it came from and what was proven, not merely what
happened:

```
Batch 17
  base_dev_sha:           a82f91   the dev the ref was cut from
  integration_sha:        b73cd2   the assembled ref
  ci_status:              PASSED
  validated_sha:          b73cd2   what CI actually proved
  dev_sha_at_validation:  a82f91   what dev was when it proved it
  status:                 READY_TO_MERGE
```

Before merging, one comparison:

```
current dev SHA == dev_sha_at_validation ?
  yes -> merge
  no  -> dev moved: rebuild the ref, re-run CI
```

With a serial integration worker, dev can only move between validation and
merge if a person pushed directly. That makes the check cheap and almost always
a no-op — which is precisely when a guard is worth having, because the case it
catches is the one nobody is watching for.

`base_dev_sha` and `dev_sha_at_validation` are the same value in the ordinary
case. Both are recorded anyway: when they differ, the divergence is the bug
report.

## Consequences

- Orion keeps its "one static binary, no services" property. A future feature
  that wants a broker argues against this ADR, not around it.
- Batch state is greppable and diffable, like the rest of Orion's state, and
  can be inspected after a failure without a client.
- The merge step gains a precondition it has never had: no result is merged
  into a base other than the one it was validated against.
- OR-254's dashboard reads these records rather than deriving integration
  timing a second way.
- If integration throughput ever becomes the binding constraint at 50-100
  agents, the answer is more integration lanes partitioned by dependency
  (OR-95), not a bigger queue. The queue was never the bottleneck.
