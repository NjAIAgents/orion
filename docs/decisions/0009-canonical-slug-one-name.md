# 0009: One canonical slug names the Jira project, workspace and git repo

- Status: Accepted
- Date: 2026-08-28
- Ticket: OR-149

## Context

`orion new` derives a workspace slug from the idea
(`internal/workspace.Slugify`) and, separately, a Jira project key from the
same idea (`internal/tracker.DeriveKey`, constrained to Jira's
uppercase, 2-10-character project-key rules). Derived independently, these
risk a workspace named one thing and its Jira project or git repo named a
related-but-different thing for the same piece of work.

## Decision

One canonical slug is derived once from the idea and reused to name the
Jira project, the workspace directory and the git repo.

## Consequences

- A person moving between the tracker, the filesystem and GitHub sees the
  same name everywhere, rather than needing a mapping table between them.
- Anywhere a new identifier is derived for one of these three surfaces, it
  derives from the same canonical slug rather than independently
  re-deriving from the idea text (which, per `internal/tracker`'s own
  design, is not guaranteed to produce the same result twice against
  Jira's stricter key constraints).
