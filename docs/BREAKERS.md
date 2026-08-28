# Breakers: what trips, what recovers, and what an agent should do

This exists because two runs stopped to ask the same question and an advisor
correctly refused to invent an answer (OR-143, OR-156). It is the answer.

## The rule

**A tripped breaker ends the run. There is no working around one.**

A breaker fires when a session has demonstrated it is not making progress.
Continuing costs money and produces confident, wrong work. Stopping is the
designed outcome, not a failure to handle.

## What each trip allows

| Trip | Meaning | Self-service recovery |
|---|---|---|
| `breaker/unverified-edits` | edits piled up with no verify | **Yes.** Run the tests or the build. A PASSING verify clears the trip and the run continues. |
| `breaker/loop` | the identical call, same arguments, `max_repeat_identical` times | **None.** |
| `breaker/command-failures` | one command failed `max_same_command_failures` times | **None.** |
| `breaker/consecutive-failures` | `max_consecutive_failures` calls failed in a row | **None.** |
| session time, tool budget | the run exceeded its allowance | **None.** |

Two things stay open on **every** trip, because sealing them makes the
breaker a deadlock rather than a brake (OR-119):

1. Writing `plans/BLOCKED.md`. That is the breaker's own protocol.
2. Nothing else.

The block message names the recovery that this specific trip has. It used to
mention the verify exemption on every kind, which two agents read as "Bash is
open" on a loop trip and then found refused. If the message does not offer a
recovery, there is not one.

## What an agent should do when it trips

1. Stop. Do not retry the call. Do not find another route to the same action.
2. Write `plans/BLOCKED.md`: what was being attempted, what is done, what
   remains, and the exact next step.
3. **Commit what compiles.** A branch with real commits can be resumed. A
   plan file describing uncommitted work cannot, and OR-143 stranded a
   half-written function that way.
4. Summarise and stop.

## What a human should do

```
orion reset --session <id>     # clears the trip after review
```

Then requeue the ticket by removing `orion-failed` and adding `ORION`.

Reset after **looking**, not reflexively. A breaker that is cleared without
anyone reading why it fired is a breaker that has been switched off.

## Why loop trips have no way out

An unverified-edits trip means "you have not checked your work", and running
the check is both the remedy and the proof. Nothing analogous exists for the
others. A loop trip means the agent is repeating itself; allowing it to run a
build does not tell it anything it did not already have, and every extra call
is spend against a session that has already shown it is stuck.

If a limit is wrong for a repository, change the limit in `orion.json`
(`limits.max_repeat_identical` and friends). Do not weaken the trip.
