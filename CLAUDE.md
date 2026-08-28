# Orion — project instructions

## Precedence rule (binding)

Orion owns orchestration, gating, artifacts and the tracker contract. An
external toolkit (nj-agents, or any future one) supplies methodology
**inside** a stage; it never owns control flow **across** stages.

In practice: a delegated skill runs as one step inside a stage Orion is
already sequencing and reports a verdict back (PASS/WARN/BLOCK, an exit
code). It does not decide whether the next stage runs, does not merge, and
does not keep its own competing record of what happened. When evaluating
whether to integrate a new toolkit or accept a new skill invocation, check
it against this rule first — anything that wants to drive sequencing itself
cannot be adopted wholesale, even if its underlying ideas are worth having
natively.

Full rationale and the worked example (superpowers' `/execute-plan`
declined for exactly this reason): `docs/decisions/0001-precedence-rule-orion-owns-orchestration.md` and `docs/decisions/0002-superpowers-declined-as-dependency.md`.

See `docs/decisions/` for the rest of this repo's architecture decisions.
