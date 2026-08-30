# 0008: Parallelism ships level 3, then level 1, then level 2

- Status: Accepted; amended 2026-08-30 (OR-206) — the gate fired, negatively
- Date: 2026-08-28
- Tickets: OR-143, OR-144, OR-145

## Context

Orion can run more of the pipeline in parallel across worktrees and
tickets. Three levels of parallelism were identified (OR-143, OR-144,
OR-145). Level 2 specifically depends on Orion's auto-rebase behavior
(`collect.auto_rebase`, OR-114) holding up correctly under concurrent
branches — behavior that had not yet been observed in practice at the time
of this decision.

## Decision

Level 3 ships before level 1, and level 1 before level 2. Level 2 is
gated on first observing auto-rebase behave correctly under real load,
not just on level 1 and level 3 being done.

## Consequences

- Level 2 parallelism work should not start until there is direct evidence
  auto-rebase holds up under the concurrency level 1 already exercises.
- This ordering is a dependency, not a statement that level 2 is riskier
  or less valuable than the others — skipping the gate builds level 2 on
  an unverified assumption about auto-rebase.

## Amendment, 2026-08-30 (OR-206)

The gate fired, and the evidence is negative. At `max_concurrent_tickets = 2`
— the lowest parallel setting there is — two of the in-flight branches
exhausted `maxAutoRebases` and were handed to a person, and they were the two
that had been open longest. Auto-rebase does not hold up under the concurrency
level 1 already exercises, and it was never going to: with
`require_up_to_date` on, one merge invalidates every other open pull request,
so rebasing them all grows with the square of the queue depth.

This does not mean level 2 was mis-ordered. It means the thing level 2 was
gated on was the wrong mechanism, and
[0011](0011-orion-owns-the-landing-queue.md) supplies the right one: a landing
queue Orion owns, where one branch takes a turn per pass and the rest hold.

So the gate is re-stated rather than lifted. **Level 2 parallelism is now
gated on the landing queue holding up under the concurrency level 1
exercises** — the same shape of evidence, about the mechanism that actually
bears the load. `max_concurrent_tickets` should not be raised before that,
for the reason the original gate existed.
