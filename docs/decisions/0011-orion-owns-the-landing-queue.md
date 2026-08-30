# 0011: Orion owns the landing queue; GitHub's merge queue is not adopted

- Status: Accepted
- Date: 2026-08-30
- Tickets: OR-206 (and OR-114, the auto-rebase this replaces the speculative
  half of)

## Context

Orion parallelises the work and left the landing serial but *unqueued*. With
`vcs.require_up_to_date` on the work branch, one merge moves the base and
invalidates every other open pull request at the same instant. Each
invalidated branch then rebased, force-pushed and re-ran its checks — all of
them, on the same pass — and because CI is slow (OR-202) another branch merged
before most of them finished, so they paid again.

The cost was written down before it was observed.
`internal/collect/rebase.go` said outright that "the count grows with the
square of the queue", and `cmd/orion/reposettings.go` that strict "makes every
merge invalidate every other open pull request" with a cost that "scales with
queue depth". Both were written when the depth was 1 and the cost was
theoretical.

On 2026-08-29, at `max_concurrent_tickets = 2` — the lowest parallel setting
there is — OR-194 and OR-199 exhausted `maxAutoRebases` and were handed to a
person, while OR-196, OR-197 and OR-200 merged in the same window. The two
that starved were the two that had been open longest, because nothing ordered
the queue and losing a race earned a branch nothing. Starvation is
deterministic, not unlucky: it follows whenever merge rate × CI duration ≥ 1,
and depth 2 sits on that boundary.

[0008](0008-parallelism-level-ordering.md) gated level-2 parallelism on
"direct evidence auto-rebase holds up under the concurrency level 1 already
exercises". That evidence has now arrived and is negative. It is not a defect
in `rebase.go`, which behaves exactly as documented — it is the absence of the
mechanism `rebase.go` was standing in for.

Raising `maxAutoRebases` does not fix it. It moves the loss further out
without changing the asymptotics, and `rebase.go` already gives the reason: a
ticket rebased twice and behind again "is not in a situation more rebasing
resolves". The queue is the problem.

## Decision

**Orion owns a landing queue.** When several branches are behind their base at
once, exactly one takes a turn per pass and the rest hold: no force-push, no
CI re-run, no rebase allowance spent. The turn goes to whichever branch has
been behind longest, and a branch gives up its place the moment it stops
waiting for one — it landed, it is no longer behind, it conflicts, or it has
been handed to a person. State lives beside the existing rebase counter in
`.orion/merge-requests.json`.

**GitHub's native merge queue is not adopted**, though it solves the same
problem, for two reasons that are already recorded in this repository:

- It is a forge feature Orion cannot rely on being there.
  `internal/collect/staleness.go` documents that even branch *protection* is
  unavailable for private repositories on the free plan, which is every
  repository Orion currently runs on; the merge queue sits above that. The
  staleness gate was built as a local git command for exactly this reason, and
  the queue that answers it should not have a harder dependency than the gate
  that raises it.
- Sequencing across stages is Orion's, per
  [0001](0001-precedence-rule-orion-owns-orchestration.md). Deciding what
  lands next is orchestration, not methodology inside a stage.

**`require_up_to_date` stays the operator's call.** Turning it off for the
work branch would remove the cause outright, at the price of the guarantee
that checks ran against the exact merge result. `reposettings.go` already
states this is not a fact Orion gets to assume, and that does not change here.

## Consequences

- The rebasing cost is linear in the queue depth rather than quadratic, and
  the oldest branch behind its base is the first one helped rather than the
  most likely to starve.
- `maxAutoRebases` is unchanged and still bounds a runaway loop. The queue
  decides *whose turn it is*, not whether there is a limit; a branch past the
  cap is still handed to a person, and being the leader is not permission to
  exceed the bound.
- Merging is deliberately **not** queued. A branch whose checks are green and
  whose base has not moved still lands immediately, and the single-ticket path
  costs no extra passes. This bounds the change to the speculative rebasing
  that produced the quadratic; serialising merges as well would hold pull
  requests that are ready, which is a larger decision than this evidence
  supports.
- [0008](0008-parallelism-level-ordering.md)'s gate has fired and is amended
  there rather than silently satisfied: level 2 is now gated on this queue,
  not on the auto-rebase it replaces.
- `max_concurrent_tickets` should still not be raised on the strength of this
  alone. The rebase count grew with the *square* of the depth, so 2 → 4 was
  never a doubling of the pressure; the queue removes that term, but OR-202
  (CI duration) is still the multiplier on every cycle.
