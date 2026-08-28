# 0008: Parallelism ships level 3, then level 1, then level 2

- Status: Accepted
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
