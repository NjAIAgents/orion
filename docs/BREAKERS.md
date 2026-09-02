# Breakers: what trips, what recovers, and what an agent should do

This exists because two runs stopped to ask the same question and an advisor
correctly refused to invent an answer (OR-143, OR-156). It is the answer.

## The rule

**A tripped breaker ends the run. There is no working around one.**

A breaker fires when a session has demonstrated it is not making progress.
Continuing costs money and produces confident, wrong work. Stopping is the
designed outcome, not a failure to handle.

## Where it applies

**Supervised runs only.** Everything here bounds an unattended agent, and a
trip *commits* so the run's work survives the run — which is the wrong thing
to do to a person at a keyboard, who can already stop typing (OR-263).

The breaker arms when `ORION_WORKSPACE` is set, which the supervisor exports
into every agent run and nothing else does. Outside one it says so and allows
the call:

```
orion: not a supervised run (ORION_WORKSPACE unset); breaker inactive
```

`ORION_BREAKER_FORCE=1` arms it anyway, for testing from a shell.

This scoping is the breaker's alone. `gate` (dangerous shell commands) and
`shield` (an agent editing its own guardrails, or weakening the test that
defines a fix) guard against anyone holding the tool, and stay armed
everywhere.

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

## Waiting for a long command is not looping

A read of a file you are **waiting on** is not counted by the identical-repeat
breaker. Three things make a call a wait rather than a repeat:

- asking a background task for its output;
- reading a file a background command of yours was told to write, even while
  it is still empty;
- a read that returns something **different** from the last identical one.

Everything else still counts. Re-reading a file nothing is writing is the loop
this breaker exists to catch, and the exemption does not touch it.

This is OR-124's precedent applied again: a passing verify was exempted
because the normal working cycle was being counted as a loop. OR-189 and
OR-191 hit the same shape from the other side — the suite took nine minutes,
the only way to wait was to re-read its output file, and from the breaker's
side that is the *same action* as looping. Both runs finished their work, had
it green, and died with every line uncommitted.

**Still prefer the foreground.** One Bash call that waits several minutes is
one tool call, and waiting is free. The exemption exists so that a wait is not
punished, not because polling is the better shape.

## The work you were holding is committed for you

When a breaker with no way out trips, whatever the worktree holds —
modified files and new ones — is committed on the spot, as a `wip:` snapshot,
before you get another turn. `plans/BLOCKED.md` is excluded: it is the account
of the trip, not part of the change.

The commit message says the work is unverified, because it is: the session was
stopped for not making progress. It is preserved for a person to read, not
blessed. Nothing in the allowance is spent on it, and the block message tells
you what actually happened to it — including when the commit failed.

`breaker/unverified-edits` does **not** snapshot. It is the one trip with a
designed way out, and a `wip:` commit in the middle of a run that then
succeeds is noise in somebody's pull request.

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

So when a **run** ends with a breaker tripped and the worktree still holding
uncommitted changes, Orion commits them as the same `wip:` snapshot — and
says so in the run output, **on the ticket**, and to the operator's channel.
Only if that commit fails does it revert, and it says that too, in those
words: a dirty worktree makes the next rebase of the branch refuse, so
leaving the residue is the one option that helps nobody.

Reverting used to be unconditional, on the reasoning that a trip's residue is
unverified. True, and it cost OR-189 and OR-191 258 and 439 lines of finished,
green work between them. Unverified work on a branch can be read, resumed or
dropped by a person; reverted work cannot be anything.

The ticket carries it because both of those runs "looked like ordinary
failures until someone opened the worktree".

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
