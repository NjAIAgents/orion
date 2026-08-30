# 0012: A tracker project gets one workspace; a second `orion plan` refuses

- Status: Accepted
- Date: 2026-08-30
- Ticket: OR-149

## Context

`workspace.New` was deliberately not idempotent, and said so in its own
comment: two identical ideas got two workspaces, "because conflating them
would let one task's failed state contaminate another's fresh attempt."

[0009](0009-canonical-slug-one-name.md) then made one canonical slug name the
Jira project, the workspace and the git repo. `orion plan <KEY>` derives that
slug from the tracker project's name and provisions the workspace under it,
which changes what a workspace is identified BY — and so changes the contract
above. It left one question open: what does a second `orion plan` on the same
key do — reuse the workspace, refuse, or suffix it?

## Decision

A tracker project gets exactly one workspace. A second `orion plan` on the
same key **refuses**, naming the existing workspace and the two ways forward:
continue in it, or `orion rm <id>` and start over.

Reuse and suffix were both rejected:

- **Suffix** re-creates the problem 0009 exists to solve. The second workspace
  would be `orion-payments-2` while the tracker still says `ORPAY` and the
  repo still says `orion-payments`, so the one name is one name only until the
  command is run twice.
- **Reuse** is the original contamination hazard under a new name. Running the
  plan chain again into a workspace holding a half-finished spec is exactly
  one attempt's failed state contaminating the next.
- **Refuse** keeps both properties and costs one command. Starting over stays
  possible; it just becomes a decision somebody makes rather than something
  that happens.

The original rationale is not being overturned, because it does not reach this
case. An idea is free text, and two typings of it are honestly two attempts.
A tracker project is a globally unique identifier that, in Jira, a non-admin
cannot delete — so two workspaces claiming one project would leave every later
stage asking which of them owns it and getting two answers.

## Consequences

- `workspace.NewOptions.Slug` makes the workspace id the canonical slug
  verbatim, with no random suffix, so a given project always resolves to the
  same workspace. The `New` doc comment states both contracts and which one
  applies when, rather than leaving the superseded rationale in place.
- Without a `Slug` — `orion new "<idea>"` — nothing changes: the id keeps its
  random suffix and two identical ideas still get two workspaces.
- Re-running `orion plan` is not a way to retry a failed stage. Retrying a
  stage is `orion run <id> --stage <stage>`, in the workspace that already
  exists.
- Two DIFFERENT projects whose names slugify alike collide on the second one.
  The refusal reads the existing workspace's recorded tracker binding and says
  which project actually owns it, so this reads as a name clash rather than as
  a repeated command.
