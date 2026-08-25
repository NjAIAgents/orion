---
description: Record a correction so Orion does not repeat it in any project
---

Capture a lesson from a mistake that was just made or corrected.

The rule is the two-strike rule: a mistake seen twice becomes a durable
correction. Recording it once is cheap, so record on the first occurrence and
let recurrence do the promoting.

1. State the lesson in one imperative sentence. "Money is BigDecimal, never
   double." Not "we should probably be careful with floating point."

2. Record it:

   ```
   orion lessons add "<the lesson>" --kind correction
   ```

3. It starts scoped to this project only. It reaches other projects only after
   it actually recurs somewhere else. That restraint is deliberate: a lesson
   about this repo's conventions is wrong advice everywhere else, and a memory
   that leaks bad advice across projects is worse than no memory.

Lessons that have not recurred in 90 days expire, and the block injected into
`CLAUDE.md` is capped and ranked. The agent reads that file in full every
session, so an unbounded list would degrade every session invisibly.
