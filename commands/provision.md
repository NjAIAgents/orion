---
description: Create the remote repository, branch model and tracker project
---

Provision the external systems for a workspace. Everything here touches
things outside the sandbox, so each step confirms before acting and each is
idempotent.

1. Confirm the toolchain can actually do it:

   ```
   orion doctor
   ```

   Look specifically at `gh repo scope` and `jira`. A `gh auth status` that
   passes can still lack the `repo` scope, and a Jira account that
   authenticates can still lack permission to create a project. Both fail at
   provisioning time rather than login time, which is why doctor probes them
   separately.

2. Provision:

   ```
   orion provision <id>
   ```

   This creates the private GitHub repository, pushes `main` and `develop`, sets
   `develop` as the repository default so pull requests target it automatically,
   and applies branch protection to both.

   The Jira project is normally already there: `orion new` creates it. This
   command reports the existing binding and creates nothing when one is
   recorded, and only creates or binds a project for a workspace that has
   none.

3. **Read the warnings.** Branch protection fails on free-plan private
   repositories, and that failure is reported rather than swallowed. Orion's
   own gate hook still refuses pushes to `main` and `develop`, but that
   constrains the agent only. A human with a terminal is unconstrained until
   server-side protection is in place.

4. Decompose the plan into tracker issues:

   ```
   orion run <id> --stage decompose
   ```

   This uses `/pm-plan`, which previews the entire Epic, Story and Task tree
   and waits for approval before creating anything. That approval is not
   waived by auto-merge being on. A sandboxed workspace can be deleted; issues
   in a shared tracker are seen by other people and cannot be cleanly
   withdrawn.

## The branch model

`main` is the release branch. `develop` is the integration branch and the base
for every pull request. Every task gets its own branch cut from `develop`, merges
back into `develop`, and `develop` reaches `main` later through its own reviewed pull
request.

Both long-lived branches are push-protected. Protecting only `main` would
make the pull request into `develop` optional, and an optional review gate is
not a gate.
