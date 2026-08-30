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
breaker a deadlock rather than a brake (OR-119, OR-194):

1. Writing `plans/BLOCKED.md`. That is the breaker's own protocol.
2. The **cleanup allowance** below.

The block message names the recovery that this specific trip has. It used to
mention the verify exemption on every kind, which two agents read as "Bash is
open" on a loop trip and then found refused. If the message does not offer a
recovery, there is not one.

## The cleanup allowance

*"Stop looping" and "stop acting" are not the same instruction.* On OR-192 an
agent tripped mid-edit, obeyed the policy exactly, and then could not revert
the risky test it had just written, because Edit and Bash were both refused
by then. The next reader would have found a modified file and no explanation.

So a tripped session may still run **six** commands, and only these:

```
git status    git diff    git checkout -- <path>    git restore <path>
git add <path>    git commit
```

`git commit --amend` is **refused**. It is the same command doing the
opposite thing: it replaces the tip commit rather than adding one, and what
the run already committed is the only durable record a tripped run leaves.

Three things this is not:

- **Not Bash for cleanup.** Nothing distinguishes a cleanup edit from another
  attempt at the task except intent, and intent is not observable. The
  command is. `git checkout -- x` can only revert; `git commit` cannot change
  a file's contents.
- **Not a reset.** Spending it never clears the trip, and it does not refill.
  When it is gone, everything is refused.
- **Not a shell.** `git status; anything` is refused, along with every other
  compound command.

## The stop-note is written for you

`plans/BLOCKED.md` is now appended **by the breaker, at the moment it trips**.
On OR-192 the note survived only because the agent happened to write it
before the breaker closed; one tool call later there would have been none.
Add to it — what you were attempting, what is done, what remains — rather
than assuming you have a turn in which to create it.

## What an agent should do when it trips

1. Stop. Do not retry the call. Do not find another route to the same action.
2. Add to `plans/BLOCKED.md`: what was being attempted, what is done, what
   remains, and the exact next step.
3. **Revert what should not survive, then commit what compiles.** A branch
   with real commits can be resumed. A plan file describing uncommitted work
   cannot, and OR-143 stranded a half-written function that way. An
   uncommitted change also blocks the next rebase of the branch.
4. Summarise and stop.

## What Orion does when the agent could not

An allowance only helps while the agent is still there to spend it. A run
killed on the turn ceiling, or one whose last act was the call that tripped,
has no such moment — and OR-192 was rescued only because a later stage
happened to exist and happened to read the note.

So when a **run** ends with a breaker tripped and the worktree holding
uncommitted **tracked** changes, Orion reverts them and says so, in the run
output and to the operator's channel. Commits are untouched; untracked files
are left alone, matching what `orion collect` tolerates before a rebase.
Silently leaving the residue is the one option that is not available: the
next thing to touch that branch is a rebase that refuses on a dirty tree.

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

The cleanup allowance is not a counter-example. It buys no information and
makes no attempt at the task — it exists so the *tree* can be handed over,
not so the *run* can continue.

If a limit is wrong for a repository, change the limit in `orion.json`
(`limits.max_repeat_identical` and friends). Do not weaken the trip.
