---
description: Advance the current work to its next stage
---

Advance one stage. Determine the current stage by reading which artifacts
exist, not by asking:

| Present | Next stage | Action |
|---|---|---|
| nothing | intent | invoke `beej` |
| `intent/*.intent.md` | spec | invoke `kalp`, part one |
| `+ specs/*.spec.md` | plan | invoke `kalp`, part two |
| `+ plans/*.plan.md` | build | invoke `forge` |
| `+ diff` | verify | run nj-agents `/review-tests-build` |
| verified | review | run nj-agents `/pre-push-review` |
| reviewed clean | PR | run nj-agents `/pr-describe` |

Rules:

- One stage per invocation. Do not chain through several stages in one pass:
  the artifact commit between stages is the review point, and skipping it
  removes the only place a human can intervene cheaply.
- If the previous stage's artifact is missing or incomplete, fix that rather
  than proceeding.
- If `/pre-push-review` returns BLOCK, do not open a PR. Report the findings
  and stop.
