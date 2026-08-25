---
description: Provision an isolated workspace for a new idea and capture its intent
---

Start a new piece of work under Orion.

1. Run `orion doctor` first. If it reports a blocking failure, stop and show
   the user what to fix. Do not proceed with a broken toolchain: every later
   stage will fail in a more confusing way.

2. Provision the workspace:

   ```
   orion new "$ARGUMENTS"
   ```

   This creates an isolated directory with its own git repo, its own generated
   sandbox settings, and its own state. Nothing here can reach the user's other
   work.

3. Report the workspace id and path to the user.

4. Invoke the `beej` skill to capture intent, working in the new workspace's
   `repo/` directory.

Do not skip to design or implementation. The intent artifact is what the next
stage reads.
