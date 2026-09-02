# 0018: The fan-out unit is per stage — the Go package for implementation, the case group for test authoring

- Status: Accepted
- Date: 2026-09-02
- Tickets: OR-305 (fanned test authoring), OR-306 (Orion runs the suite as a process)
- Related: [0016](0016-fan-implementation-by-go-package.md) (which this extends
  rather than overturns), [0001](0001-precedence-rule-orion-owns-orchestration.md)
  (Orion owns sequencing and verdicts; a fanned child reports and does not decide)

## Context

[0016](0016-fan-implementation-by-go-package.md) fixed the fan-out unit at the
Go package and rejected per-file splitting, for two hazards it named
precisely:

- **Builds are not isolated.** The compiler compiles the package. A subagent
  running tests to check its own work builds against its peers' half-written
  files and sees failures that are not its own.
- **Signature coupling.** File A defines a function, file B calls it. Agent B
  writes the call site against a signature agent A is still changing.

Its title says *implementation*, and that was deliberate. But `fanout.Validate`
is the only gate in the codebase, so a second stage wanting to fan had no way
to do it except under a rule derived from a different stage's coupling.

Test authoring is that second stage, and the numbers are what forced the
question. QA derives its cases through one subagent (OR-182) and then hands
the entire list to a single session that writes every file. Fifty cases is
fifty files written one after another, in one conversation. Nothing about that
is parallel, and the wait is agent turns rather than compute — so neither
`t.Parallel` (OR-264) nor CI capacity (OR-292) touches it.

**Neither of 0016's hazards is present when the stage is writing tests.** Test
files do not define APIs that other test files import, so there is no signature
to couple to. And QA writes against an implementation that is already finished
and committed — `runQA` captures the pre-QA commit before its first turn
precisely because that is true.

The build hazard is the one that needs care, because it is real whenever
anything compiles mid-fan. It is avoided by construction rather than by luck:
no authoring child runs anything at all. That is why this decision and OR-306
had to land together. Once Orion owns execution, "nobody compiles until every
writer has stopped" is a property of the code rather than an instruction an
agent might disregard.

## Decision

**The fan-out unit is a property of the stage, not of the codebase.**

- **Implementation** fans by Go package, exactly as 0016 says. Nothing in that
  decision is weakened, and `fanout.Validate` continues to enforce it.
- **Test authoring** fans by case group. Cases are independent by construction:
  being separately checkable is what makes something a case rather than a
  detail of another one.

Two constraints make the second safe, and both are load-bearing:

1. **An authoring child writes and does not run.** Not its own tests, not a
   package, not the suite. The prompt says so, and a test asserts the prompt
   says so.
2. **An authoring child does not judge.** The verdict stays with the QA session
   that follows, which sees the whole diff and the whole case list. A child
   returning a verdict on a fifth of the cases would be an opinion on a
   question it cannot see all of — which is [0001](0001-precedence-rule-orion-owns-orchestration.md)'s
   rule applied inside a stage instead of across stages.

A fan that cannot run takes the serial path with the same coverage. The fan is
an optimisation; the tests are the point. Any arrangement where a refused split
produces fewer tests than the serial path is a defect, not a trade-off.

## Consequences

The wait this removes is agent turns, which no amount of test-suite
parallelism addresses. It is a different bottleneck from OR-264 and OR-292, and
conflating the two sent one analysis down the wrong path before this was
written down.

Two numbers now bound concurrency where there was one, and they bound different
resources: `qa.author_agents` bounds agents, which contend for a rate limit,
and `qa.exec_procs` bounds processes, which contend for CPU and disk. Reusing
one value for both would be wrong for whichever it was not chosen for.
`limits.max_concurrent_children` remains the hard ceiling for anything fanned,
because `supervisor.Fan` reads it directly and a per-stage setting must not be
able to lift a limit that exists to prevent a stampede (OR-162).

The cost shape changes and is stated before it is spent, per nj-agents
`CONVENTIONS-orchestration` §C: five authors is five sessions, each of which
reads the ticket before writing. On a small ticket that is worse than one
session, which is why a list too short to divide takes the serial path.

A future stage wanting to fan must argue its own unit against 0016's two
hazards rather than inheriting this one. The precedent set here is the
argument, not the answer.
