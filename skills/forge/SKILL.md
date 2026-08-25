---
name: forge
description: >
  Implements an approved plan, writing the code and the tests that prove it.
  Use when a plan.md has been committed and the next step is writing the actual change.
  Triggers on "implement the plan", "build this", "write the code for",
  "make it work", or picking up an approved plan to execute it.
  Do not use before a plan exists; a hook will refuse the edit anyway.
---

# Build

Implement `plans/<slug>.plan.md`. Nothing more.

## Rules

1. **Follow the plan.** With a solid plan, implementation is usually one pass.
   If it is taking many passes, the plan was wrong: stop and fix the plan.

2. **When implementation must depart from the plan, update the plan in the
   same commit.** Not afterwards, not in a follow-up. A plan that has drifted
   from the diff is worse than no plan, because the reviewer trusts it.

3. **Write the tests the plan names.** Run them. Do not report done until they
   pass and you have shown the output.

4. **Do not widen scope.** Anything you notice but were not asked to fix goes
   in a note at the end, not into this diff. An unrelated fix riding along in
   a diff is invisible to the reviewer, who is checking the change against the
   plan.

5. **For a bug fix, write the failing test first.** Confirm it fails for the
   right reason. Commit it. Then make it pass without editing it. Run
   `orion fix start` first: it makes the test file read-only to you, which is
   the point. The failing test defines what "fixed" means, and moving it is
   moving the goalposts.

## Verification is part of done

Run the build, the tests and the linter before reporting complete, and paste
the output. If a test fails, fix the code, not the test.

`/review-tests-build` from nj-agents auto-detects this repo's own commands.
Prefer it over guessing at them.

## When you get stuck

The circuit breaker will stop you after repeated identical calls or repeated
failures of the same command. When it does, it is right. Do not work around
it. Write what you learned to `plans/BLOCKED.md` and hand back.

## Exit

The diff and its tests, committed. The merged PR triggers the pipeline.
