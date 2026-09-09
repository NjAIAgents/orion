# 0023: One tracker project is one `.specify/`; features accrue as `specs/NNN-*/`; the spec is a living document

- Status: Accepted
- Date: 2026-09-07
- Ticket: OR-355 (epic), OR-365 (this record)
- Related: [0009](0009-canonical-slug-one-name.md),
  [0012](0012-one-workspace-per-tracker-project.md),
  [0013](0013-new-creates-the-tracker-project-not-a-workspace.md),
  [0022](0022-per-project-toolkit-install-via-specify-init.md)

## Context

spec-kit's unit of work is a **feature**: a numbered directory
`specs/NNN-<name>/` holding one `spec.md`, `plan.md` and `tasks.md`, with a
branch per feature created by the `git` extension
[0021](0021-spec-kit-inside-stages.md) declines. Its brownfield guidance is
"run `/speckit-specify` again; it creates a new feature directory". Orion's
unit is a **tracker project**: one project, one workspace
([0012](0012-one-workspace-per-tracker-project.md)), created by `orion new`
and provisioned by `orion plan`
([0013](0013-new-creates-the-tracker-project-not-a-workspace.md)). The chain
[0022](0022-per-project-toolkit-install-via-specify-init.md) describes runs
once per project and pins `specs/001-<slug>`. Nothing said what a feature is
in Orion's terms, or what happens to a spec after the stage that wrote it.

spec-kit's own documentation names three ways a spec can change after it is
written, and is careful not to choose:

- **flow-forward** — never edit; every change is a new feature directory,
  the old one kept as history;
- **living spec** — `spec.md` is the contract; edit it in place and
  regenerate `plan.md` and `tasks.md` from it;
- **flow-back** — edit whatever is nearest (the code, the tasks, the plan)
  and reconcile the rest afterwards, using `analyze` to find the gaps.

Orion's gates act on files. The discovery gate reads the open questions in
the spec the next stage will read; the artifact gate checks that the file a
stage owes exists and is committed; the analyze stage compares the spec,
plan and tasks that are there now. Every one of them assumes there is one
current spec to look at.

## Decision

**One tracker project is one workspace is one `.specify/`.** The
constitution, the presets and `feature.json` are project-level and shared
by every feature in that project. There is never a second `.specify/` for a
second feature.

**Features accrue as `specs/NNN-<slug>/` under that project.** The chain's
first feature is `001-<slug>`, the slug of
[0009](0009-canonical-slug-one-name.md). Each feature decomposes into the
tracker under its own identity label, `orion-spec-<slug>`
(`internal/decompose/tasks.go`, `Tree.Label`), so a re-run reconciles
against that feature's tree and no other.

**The spec is a living document.** After the spec stage has written it,
`spec.md` is edited in place — by `orion answer`, by a person, or by
re-running the stage — and everything downstream is regenerated from it with
`orion plan KEY --from spec`. It is never versioned by copying the directory.
Flow-forward is not adopted because it multiplies the file the gates read:
two spec directories for one piece of work is two answers to "what are we
building". Flow-back is not adopted because it lets the code lead the spec,
and the gates cannot see code drift — only a later verify-class report can,
and [0021](0021-spec-kit-inside-stages.md) keeps that a report.

## Consequences

- `orion plan KEY` resumes per key and never creates `specs/002-*`. The
  entry point for a second feature on an existing project — what the
  operator types, how its directory is numbered, whether it gets its own
  epic — is out of scope for this record and is not decided here. Until it
  is, a second feature is a second tracker project.
- `--from <step>` is the re-plan verb. Editing `spec.md` and re-running from
  `spec` regenerates the plan, the tasks and the analysis; `decompose`
  reconciles by identity label, so tickets that still match are linked and
  only new ones are created.
- Because the constitution is project-level, a change to it re-gates every
  feature's next plan and analyze run; that is the point of having one.
- The queue label goes on **stories and epic-level tasks**, never on the epic
  and never on a task under a story (OR-390; `Tree.Queue` in
  `internal/decompose/marker.go`, pinned by
  `TestQueueLabelOnStoriesAndEpicLevelTasksAdmitsEachOnce`). The rule is the
  queue's: `watch.Queued` drops a labelled issue whose parent is also
  labelled, and a claimed parent works its children in one branch. Label the
  epic and one agent works the whole project in one branch while every
  story is dropped as "its parent has it"; label sub-tasks and they are
  dropped as redundant. Stories and epic-level tasks have no labelled parent,
  so each is admitted exactly once.
- The task list's Dependencies section is carried into the epic body as
  prose and **not** turned into `is blocked by` links, so Setup, Foundational
  and story items are all claimable at once. Accepted for now rather than
  done silently: the links would be one more thing to reconcile on a re-run,
  and the queue already serialises within a story. Revisit if parallel
  claims on one feature collide in practice.
