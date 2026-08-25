---
name: beej
description: >
  Captures a raw idea as a committed intent.md, the first artifact in the SDLC chain.
  Use when someone describes something they want built, a problem they cannot solve today,
  a feature request, or a pain point, and the next step is to write it down properly.
  Triggers on "capture this idea", "write this up as intent", "I want to build X",
  "start a new project for X", "turn this into a spec-ready brief", or any point where
  a half-formed idea needs to become a version-controlled artifact before design starts.
  Do not use for designing a solution (that is the blueprint stage) or writing code.
---

# Intent capture

Turn what someone wants into `intent/<slug>.intent.md`: a proto-spec in the
originator's own words, committed to git so the next stage can read it.

## Why this exists

Traditionally an idea passes through backlog entries, user stories, story
points and refinement meetings before anyone can act. Ownership transfers at
each handoff, so what reaches engineering is several steps removed from what
the originator meant. Capturing once, in their words, removes every one of
those handoffs.

## The bar

The originator reads it back and says "yes, that is what I meant." Not
"close enough."

## Steps

1. **Listen first.** Have them describe the problem in their own words. What
   can they not do today, who is affected, what does better look like, what is
   explicitly out of scope. No formal language required.

2. **Interrogate.** Ask what an analyst would ask, one thread at a time:
   - Who exactly is affected, and how many of them
   - What happens today instead (the workaround is often the real requirement)
   - What is deliberately out of scope
   - What constraints are non-negotiable (data handling, auth, latency, budget)
   - How would we know this worked

3. **Do not invent answers.** Where the idea is genuinely ambiguous, the
   question goes in Open questions. A flagged unknown is worth more than a
   confident guess, because the guess will be discovered as wrong three
   stages later when it is expensive.

4. **Write the artifact** to `intent/<slug>.intent.md` using the template in
   `templates/intent.md`.

5. **Read it back.** The originator corrects anything misunderstood before it
   is committed.

6. **Commit.** Author and timestamp join the record. The product owner picks
   it up from there.

## Hard rules

- No solution design. If you find yourself naming a database or a framework,
  you have left this stage.
- No code.
- Every unknown becomes an Open question rather than an assumption.

## Exit

A committed `intent/<slug>.intent.md`. The accept decision, recorded as the
merge or closing review, is what starts the blueprint stage.
