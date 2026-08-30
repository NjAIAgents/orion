---
description: Start a new piece of work: elaborate the idea, name it, create its tracker project
---

Start a new piece of work under Orion.

1. Run `orion doctor` first. If it reports a blocking failure, stop and show
   the user what to fix. Do not proceed with a broken toolchain: every later
   stage will fail in a more confusing way.

2. Hand the user this command to run **in their own terminal**:

   ```
   orion new "$ARGUMENTS"
   ```

   Do not run it yourself. It interviews the user about the idea — who it is
   for, what is wrong today, what success looks like, what is out of scope,
   what constrains it — and then has them finalise the project name. It needs a
   terminal, and the answers are theirs to give.

   It creates the tracker project carrying that elaborated description, behind
   a confirmation that says a Jira project cannot be deleted without admin
   rights. It creates no workspace and writes nothing to disk.

3. Ask the user for the project key it printed, then:

   ```
   orion plan <KEY>
   ```

   That provisions the workspace and announces the design chain and its cost
   before anything spends.

Do not skip to design or implementation. The project description is what the
next stage reads.
