# 0001: Orion owns orchestration; a toolkit supplies methodology inside a stage, never control flow across stages

- Status: Accepted
- Date: 2026-08-28
- Load-bearing: yes — also recorded in `CLAUDE.md` because it binds future
  integration work, not just this record.

## Context

Orion delegates real work — review, security scanning, testing, PR
authoring, PM decomposition — to nj-agents skills rather than
reimplementing them (see `docs/nj-agents-integration.md`). As more of the
pipeline gets delegated, and as more external toolkits get evaluated for
integration (see [0002](0002-superpowers-declined-as-dependency.md)), the
question of who is actually in charge at any given moment stops being
implicit. A skill invoked mid-stage could, in principle, try to decide what
runs next, write its own state about what happened, or act as a second
sequencer alongside Orion's own stage loop (intent -> spec -> plan ->
scaffold -> decompose -> build -> verify -> review -> pr).

Without a stated rule, every new integration re-litigates this from
scratch, and the failure mode is silent: two things that both think they
own sequencing don't collide loudly, they each proceed on a private
assumption about what already happened.

## Decision

Orion owns orchestration, gating, artifacts and the tracker contract. An
external toolkit supplies methodology **inside** a stage; it never owns
control flow **across** stages.

Concretely:

- Orion decides which stage runs next, whether a gate passes, what gets
  written to the artifact chain (`docs/intent/`, `specs/`, `plans/`) and
  what gets committed to the tracker.
- A toolkit skill is invoked as one step inside a stage — e.g.
  `/pre-push-review` inside the review stage — and reports a verdict back
  (PASS/WARN/BLOCK, an exit code). It does not decide whether the next
  stage runs, does not merge, does not maintain its own competing record of
  what happened.
- `docs/nj-agents-integration.md`'s division-of-labour and stage-delegation
  tables are the concrete expression of this rule, not a separate decision.

## Consequences

- Every future toolkit integration is evaluated against this rule first.
  Anything that wants to drive sequencing across stages itself — as
  superpowers' `/execute-plan` does — cannot be adopted as a whole
  dependency; see [0002](0002-superpowers-declined-as-dependency.md) for the
  worked example.
- A toolkit skill that bypasses Orion's gates (auto-merging on its own,
  blocking without reporting through the exit-code contract) is a bug in
  the integration, not an acceptable variant of it.
- Because this binds how integration work is designed rather than merely
  recording a past choice, the rule itself is also stated in `CLAUDE.md` so
  it reaches an agent working in this repo without it first reading this
  ADR.
