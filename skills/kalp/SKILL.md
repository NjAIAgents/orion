---
name: kalp
description: >
  Turns a committed intent.md into a requirements-and-design spec and then an
  implementation plan, the second and third artifacts in the SDLC chain.
  Use when an intent has been accepted and the next step is designing the solution,
  or when an approved spec needs an implementation plan before any code is written.
  Triggers on "write the spec", "design this", "how should we build this",
  "produce an implementation plan", "plan this change", "what files change",
  or any request to work out an approach before implementing it.
  Do not use for capturing the original idea (that is the intent stage) or for
  writing the implementation itself.
---

# Spec and plan

Two artifacts, produced in one working session, each committed before the
next begins.

## Part one: the spec

Read `docs/intent/<slug>.md`. Produce
`specs/<slug>.spec.md`: requirements and design together, constrained by
whatever policy skills are available in this repo.

Requirements and design were historically separate phases run by separate
teams. The separation existed for accountability, and it was slow and lossy.
Collapsing them works only if policy is applied while the spec is written
rather than discovered in review weeks later, which is what the skills are
for.

**Demand flagged concerns explicitly.** Especially where two policies
contradict and you cannot satisfy both. Those are the points an analyst would
have escalated, and each one gets resolved with its policy owner before
engineering sees the spec. A spec with no flagged concerns on a non-trivial
change usually means they were not looked for.

Answer or carry forward every Open question from the intent. None may be
silently dropped.

## Part two: the plan

Read the intent and the spec. Produce `plans/<slug>.plan.md` naming:

- **Files that change**, by path
- **Order of work**, as steps
- **Risks**: what could this break, which step is riskiest
- **Rejected alternatives**: what you considered and why you did not pick it
- **Proof**: the tests that demonstrate it works

Then interrogate your own plan before offering it. What breaks if step two
fails halfway. What did you assume that the spec does not state.

**The bar:** an engineer who has never seen this conversation could implement
the change from the plan alone. If they would need to ask you a question, the
plan is not finished.

## Hard rules

- No implementation in either part.
- The plan gate is enforced by a hook. Nothing gets implemented until a
  `*.plan.md` exists, so producing this artifact is not optional ceremony.
- If implementation later departs from the plan, the plan is updated in the
  same commit. Two sources of truth is worse than one wrong one.

## Exit

`specs/<slug>.spec.md` and `plans/<slug>.plan.md`, both committed. Accepting
the plan starts the build stage.
