# 0007: Auto effort is a standing preference, not per-ticket

- Status: Accepted
- Date: 2026-08-28
- Ticket: OR-134 (comments)
- Related: [0005](0005-agent-roster-is-global.md)

## Context

Each actor in the roster can run at a chosen `claude --effort` level (low,
medium, high, xhigh, max), configurable the same way its model is (OR-131,
OR-133). Anthropic's own guidance favors leaving effort on auto rather than
hand-tuning it per task. Separately, model and effort are prompt-cache
keys: changing either mid-run invalidates the prompt cache for that run.

## Decision

Auto effort is a standing preference, set once in the roster
(`~/.orion/agents.json`, per [0005](0005-agent-roster-is-global.md)), not
chosen per ticket. Escalating effort for a specific run must happen at a
fresh run — never mid-run — because model and effort are prompt-cache keys.

## Consequences

- There is no per-ticket effort override; changing effort means changing
  the roster entry before starting the *next* run, not asking a run
  already in progress to escalate.
- Any tooling, prompt, or documentation suggesting "bump the effort for
  just this ticket" mid-run is inconsistent with this decision and should
  be corrected rather than accommodated.
