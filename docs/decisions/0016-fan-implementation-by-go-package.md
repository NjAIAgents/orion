# 0016: Implementation fans out by Go package, the implementer proposes and Orion validates, and only the parent verifies

- Status: Accepted (by Navjyot, 2026-08-31; recorded as Accepted before review, see OR-230)
- Date: 2026-08-31
- Tickets: OR-230 (this decision), OR-229 (the read-side fan-out this waited on),
  OR-181 (the `supervisor.Fan` primitive it dispatches through)
- Related: [0001](0001-precedence-rule-orion-owns-orchestration.md) (Orion owns
  sequencing; a delegated step reports a verdict and does not decide what runs
  next), [0008](0008-parallelism-level-ordering.md) (which parallelism ships
  when), [0014](0014-supervised-runs-get-a-curated-config-directory.md) (a run
  gets the capabilities Orion decided it gets)

## Context

The implementer works files serially. Where a ticket touches genuinely
independent code that is wasted wall time, and `Edit` plus `Write` is 177 of
879 tool calls — 20% — on a measured run. The obvious fan-out is per file:
different files, different inodes, whole-file writes, no collision.

**Per-file ownership solves the write race and neither problem that bites.**

- **Builds are not isolated.** The compiler compiles the *package*, not the
  file. A subagent running tests to check its own work builds against its
  peers' half-written files and sees failures that are not its own. Five agents
  each running a suite against a tree nobody owns produces noise and cost, not
  signal.
- **Signature coupling.** File A defines a function, file B calls it. Agent B
  writes the call site against a signature agent A is still changing. This is
  the class of failure that produced the `Idea` redeclaration and the duplicate
  `CreateProject` during a manual merge — moved from merge time to write time,
  where there is no conflict marker to warn anyone.

There is a second, sharper reason to be careful here. The read-side fan-out
(OR-229) landed and was **reverted**: under concurrency its results were paired
with the wrong questions, so the implementer would have been told about files it
never asked about. Wrong rather than merely slow. A write-side fan-out has the
same failure mode with the tree as the casualty rather than a paragraph of
prose, so the design has to be one where a wrong split *fails visibly* instead
of corrupting quietly.

The gating clause on OR-230 — "do not start until the read-side fan-out has
landed and reported its numbers; if it recovers most of the time, this may not
be worth its risk" — is resolved by that revert. OR-229 recovered nothing, so
the read side has not removed the reason to do this.

## Decision

**Three rules, and each of them is what makes the next one safe.**

### The unit is the Go package, not the file

The Go package is the compilation unit and therefore the real isolation
boundary. Two files in one package are coupled by construction; two files in
different packages each build on their own, and the remaining coupling is the
import edge, which is *visible and enumerable* rather than a judgement call.

`go list`'s `.Deps` is the input, not `.Imports`: `a → c → b` is still `a`
compiling against `b`, so the transitive set is the honest one and direct
imports alone would admit a plan coupled one hop further out.

This deliberately means most of Orion's own tickets do **not** fan. A change
that runs down a layer — command, then work, then config — is one bucket and
runs serially, which is not a speedup and is the correct answer. The payoff is
independent packages, mechanical repeated changes, and added test files.

### The implementer proposes, Orion validates, and a failure means serial

The implementer emits a package-to-subagent assignment. `internal/fanout`
either admits it or forces serial:

- no package assigned twice, compared on the **canonical import path**, so two
  spellings of one package are not admitted as two;
- fan width within `limits.max_concurrent_children` — a fan wider than the cap
  is sequential rounds of writers against one tree, which loses the isolation
  and keeps the coordination;
- no import edge, in either direction, between any two assigned packages;
- and every failure to resolve — no toolchain, not a module, a tree that does
  not currently parse — is serial too.

**Any validation failure runs serially. No negotiation, no retry with a better
argument.** This is the shape of `require_plan_before_edit` and it is chosen
for the same reason: it keeps the decision out of LLM judgement, where a wrong
guess corrupts silently instead of failing visibly. An agent asked to judge
whether its own work is separable judges it optimistically.

It is also [0001](0001-precedence-rule-orion-owns-orchestration.md) applied
inside a stage. The agent supplies the proposal; Orion decides whether the work
runs concurrently, and Orion runs it.

### Subagents write, only the parent verifies — enforced by the tool list

Children produce edits and report. The parent runs build and tests **once**,
after joining, against a whole tree. One test run instead of N, and the test
suite was 21% of all Bash calls before any fan-out multiplied it.

The enforcement is `--allowedTools` and `--disallowedTools` on the child, not a
clause in its prompt. **A pattern match on the command was rejected**: it would
have to guess at every spelling of running a suite — `go test`,
`./scripts/test.sh`, `make check`, `npm test`, a script wrapping one of those —
and would be leaky by construction. A child with no `Bash` cannot run any of
them, including the one nobody thought of. `Task` is denied with it, so a child
cannot spawn children and escape the width just admitted.

The prompt still *says* it, and says why. An agent that discovers it cannot run
the suite and is not told the reason concludes the environment is broken and
spends its turns working around it — a restriction meant to save one suite run
costing several.

### Not adopted

- **Per-file ownership.** The whole Context section.
- **A git worktree per child.** It restores build isolation, and it buys back a
  merge — which is the `Idea`-redeclared failure returned to where it started,
  plus N checkouts of wall time and disk.
- **Letting a child run only "its own" package's tests.** `go test ./internal/a`
  still links `./internal/b` as it stands mid-write. The package is the
  compilation unit; it is not the linkage unit.

## Consequences

- **Most tickets will not fan, and that is the design working.** A rejection is
  the common case, not an error path, so the refusal has to name which check
  failed and read as a decision rather than a fault.
- **The fan is only as good as the task text.** A child is given its package
  and its task and nothing else — no ticket, no plan, no conversation. A task
  saying "as described above" produces a subagent that does nothing, which is
  why an empty task is a validation failure rather than a runtime surprise.
- **A failed child is a package nobody wrote.** `orion fan` exits non-zero and
  names it, because a parent reading a partial fan as a complete one would go
  on to build a tree with a hole in it.
- **The parent must actually verify.** Children that cannot test plus a parent
  that does not is a change nobody checked. Every fan's closing line says so,
  and the implementer prompt caps the fixing at two rounds per
  CONVENTIONS-orchestration §C before it stops and reports what is still red.
- **Attribution crosses a process boundary.** The children run as the same
  actor as the run that asked, resolved from `ORION_ACTOR`, so one ticket's
  implementation spend stays one row of its cost report. A frontend ticket's
  subagents are frontend work.
- **Cross-package changes still land on the parent.** A child that finds it
  needs an edit outside its package says so in its report and stops. That list
  is the only thing that survives the child's context, so a child which leaves
  something out of it has lost it.
- **Reversible.** Deleting `internal/fanout` and the `fanOffer` prompt lines
  returns the implementer to serial work; nothing else depends on the fan.
  `limits.max_concurrent_children: 1` turns it off without a code change.
