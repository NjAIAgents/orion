---
description: Provision an isolated workspace for a new idea and capture its intent
---

Start a new piece of work under Orion.

1. Run `orion doctor` first. If it reports a blocking failure, stop and show
   the user what to fix. Do not proceed with a broken toolchain: every later
   stage will fail in a more confusing way.

2. Start the idea:

   ```
   orion new "$ARGUMENTS"
   ```

   This is interactive and the user answers it, not you: it elaborates the
   idea, has them finalise the project name, and creates the tracker project
   with that description behind a describe-then-confirm gate. A Jira project
   cannot be deleted without admin rights, so do not answer that prompt on
   the user's behalf.

   It then creates an isolated directory with its own git repo, its own
   generated sandbox settings, and its own state. Nothing here can reach the
   user's other work.

3. Report the workspace id and path, and the tracker project key, to the user.

4. Invoke nj-agents `/capture-intent` to capture intent, working in the new workspace's
   `repo/` directory.

Do not skip to design or implementation. The intent artifact is what the next
stage reads.
