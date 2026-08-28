# 0003: Ponytail kept, scoped to development only

- Status: Accepted
- Date: 2026-08-28
- Ticket: OR-160
- Related: [0001](0001-precedence-rule-orion-owns-orchestration.md)

## Context

"Ponytail" is a lazy/minimal-code-first development style: before writing
new code, work down a ladder — does this need to exist at all, is it
already in the codebase, does the standard library or a native platform
feature cover it, can it be one line — and only then write the minimum
code that works. It was evaluated as something Orion's implementer stage
could apply.

## Decision

Ponytail is kept, but scoped strictly to the development/build stage — how
much code the implementer writes for a given task. It does not touch who
decides what work happens, what gets committed, or any gating or review
decision.

## Consequences

- Because it only shapes code-writing style within a stage Orion already
  owns (per [0001](0001-precedence-rule-orion-owns-orchestration.md)), it
  composes cleanly: there is no control-flow conflict, unlike superpowers'
  `/execute-plan` (see [0002](0002-superpowers-declined-as-dependency.md)).
- Applying ponytail-style preferences outside development — e.g. to review
  verdicts, PM decomposition, or gating logic — is out of scope for this
  decision and would need its own evaluation before it's assumed to apply
  there too.
