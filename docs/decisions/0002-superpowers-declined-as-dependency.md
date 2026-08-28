# 0002: Superpowers declined as a dependency; three of five ideas adopted natively

- Status: Accepted
- Date: 2026-08-28
- Related: [0001](0001-precedence-rule-orion-owns-orchestration.md)

## Context

The "superpowers" toolkit was evaluated alongside nj-agents for adoption.
It bundles skills covering, among other things, red-before-green testing
discipline, diagnose-before-patch debugging, and plan-conformance checking
— all things Orion's own build/verify stages could use. But superpowers
also ships `/execute-plan`, which wants to drive the sequence of
implementation work itself: deciding what runs next, checking its own plan
format, looping until it judges the work done.

Per [0001](0001-precedence-rule-orion-owns-orchestration.md), Orion already
owns control flow across stages. `/execute-plan` and Orion's own stage
sequencer would each assume they are the one deciding what happens next —
two control-flow owners cannot compose, regardless of how good either one
is in isolation.

## Decision

Superpowers is declined as a dependency. It is not installed, and Orion
does not invoke `/execute-plan` or any other superpowers entry point that
drives its own sequence.

Three of the five evaluated ideas are adopted anyway, but natively —
implemented as Orion-owned gates inside stages Orion already sequences,
not as calls into the superpowers plugin:

- **OR-156** — red-before-green: require a failing test before a fix is
  accepted.
- **OR-157** — diagnose-before-patch: require a stated root cause before
  the CI fix loop patches.
- **OR-158** — plan-conformance: check the implementation against the
  reviewed plan before it ships.

The other two ideas evaluated from the same set were not carried forward.

## Consequences

- No dependency on the superpowers plugin is added; the three adopted
  ideas live as Orion-native checks inside Orion's own loops (e.g. the CI
  fix loop for OR-157), so they compose the same way any other Orion gate
  does.
- This record exists so the rejection doesn't have to be re-argued: if
  superpowers (or an equivalent toolkit) is re-proposed as a dependency,
  the objection is `/execute-plan`'s control-flow ownership specifically,
  not the quality of its ideas — extracting an idea and implementing it as
  an Orion-owned gate remains open, adopting the toolkit wholesale does
  not.
- If more of superpowers' ideas turn out to be worth having, adopt them the
  same way OR-156/157/158 were: implement the idea as an Orion-owned gate,
  and do not adopt any entry point that competes with Orion's sequencer.
