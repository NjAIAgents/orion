---
description: Show what stage every Orion workspace is at
---

Report Orion state.

1. `orion ls` for every workspace.
2. If the user named one, `orion status <id>` for the detail.

Surface these explicitly, because they are the reasons a workspace that looks
idle is actually waiting:

- **BREAKER** lines mean a circuit breaker tripped and a human must review
  before work resumes. Give them the `orion reset --session <id>` command.
- **waiting-on-quota** means a provider limit was hit. Give them the resume
  time and the `orion run` command to restart.

Keep it short. A status report that buries the one blocked workspace in a wall
of healthy ones has failed at its job.
