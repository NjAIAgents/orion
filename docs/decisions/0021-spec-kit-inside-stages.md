# 0021: spec-kit runs inside Orion's stages; its workflow engine, extension hooks and bundles are declined

- Status: Accepted
- Date: 2026-09-07
- Ticket: OR-355 (epic), OR-363 (this record)
- Load-bearing: yes — `toolkit.stages` names four spec-kit commands and
  nothing else of spec-kit's is invoked; a later "the workflow engine already
  does gates and resume, just call it" reads as removing duplication and hands
  sequencing to a second owner.
- Related: [0001](0001-precedence-rule-orion-owns-orchestration.md) (this is
  its second worked example, beside [0002](0002-superpowers-declined-as-dependency.md)),
  [0019](0019-toolkit-agnostic-nj-agents-is-the-default.md),
  [0020](0020-a-toolkit-must-ship-skills-and-vendoring-is-global.md)

## Context

[0019](0019-toolkit-agnostic-nj-agents-is-the-default.md) named spec-kit as
the alternative toolkit a project might declare, and
[0020](0020-a-toolkit-must-ship-skills-and-vendoring-is-global.md) found how
to install it. Neither looked at what spec-kit had become by the time it was
tried. It is no longer ten command prompts. Read from its repository at
`4a7341a` (2026-09-04), it is six layers:

| Layer | What it owns |
|---|---|
| Core CLI `specify` | `init`, `check`, `version`, and the layers below |
| Commands | `constitution`, `specify`, `clarify`, `plan`, `checklist`, `tasks`, `analyze`, `implement`, `converge`, `taskstoissues` — markdown prompts, installed per agent |
| Extensions | new commands plus **hooks** before/after every core command, registered in `.specify/extensions.yml` |
| Presets | override templates, commands and scripts by priority, with `replace`/`prepend`/`append`/`wrap` strategies |
| Workflows | a YAML engine: `command`, `prompt`, `shell`, `gate`, `if`, `switch`, `while`, `fan-out`/`fan-in`, `slot` steps; runs persisted under `.specify/workflows/runs/<id>/` and resumable |
| Bundles | versioned composition of the above, installed from catalogs that need `~/.specify/auth.json` |

Three of those layers own control flow across stages, which is the one
authority [0001](0001-precedence-rule-orion-owns-orchestration.md) says a
toolkit never holds. The workflow engine is the obvious one: `workflows/
speckit/workflow.yml` runs specify → gate → plan → gate → tasks → implement,
and its `gate`, `while` and `fan-out` steps are Orion's confirmations, fix
loops and `orion fan` reimplemented. `implement` drives execution of
`tasks.md` phase by phase, and `converge` appends new tasks to it — a
toolkit deciding that more work exists.

The extension hooks looked like the place Orion could live *inside*
spec-kit, so that was evaluated too, and it fails for a reason worth
recording exactly. A hook is not code that runs; it is a paragraph in the
command prompt. Every core template carries the same "Pre-Execution Checks"
block, which tells the model to read `.specify/extensions.yml`, print an
`EXECUTE_COMMAND:` line for a mandatory hook, and then "actually invoke the
hook and wait for it to finish". On the Python side `HookExecutor` registers
and lists hooks; the reference states that `auto_execute_hooks` "is
currently reserved and is not consulted" and that the templates "do not
evaluate conditions". So an Orion budget gate as a spec-kit hook would be a
sentence asking the agent to please stop spending. The extension RFC says
the same thing from the other side: "Extensions run in same context as AI
agent (trust boundary)". Orion's whole model is that the agent runs inside
a boundary Orion draws — the curated config directory of
[0014](0014-supervised-runs-get-a-curated-config-directory.md), tool-call
limits enforced by hooks, egress denied — and an extension would put Orion
inside the agent's context instead of around it.

The bundled extensions each collide with a decision already made. `git`
registers a mandatory `before_specify` hook that creates a feature branch;
Orion cuts its own branch from `develop` in `internal/provision`, so two
things would create branches. `agent-context` writes a managed section into
`CLAUDE.md`, which 0014 curates. `assess` is an agent-to-agent intake →
research → define → shape → decide pipeline, which is the interview
[0006](0006-new-and-plan-are-sequential-phases.md) puts in front of a human.

## Decision

**spec-kit supplies methodology inside four stages Orion already
sequences, and nothing else of it is invoked.**

| Orion stage | spec-kit command | What Orion reads back |
|---|---|---|
| constitution | `/speckit-constitution` | `.specify/memory/constitution.md`; the artifact gate fails on any `[ALL_CAPS]` placeholder left |
| spec | `/speckit-specify` | `<FeatureDir>/spec.md`; the discovery gate blocks on any `[NEEDS CLARIFICATION: …]` marker |
| plan | `/speckit-plan`, then its own handoff to `/speckit-tasks` | `plan.md`, `research.md`, `data-model.md`, `contracts/`, and `tasks.md` for the decompose step |
| analyze | `/speckit-analyze` | its report is read-only by its own contract; Orion parses `Critical Issues Count` and blocks when it is not zero |

Each is one step inside a stage and reports back; none decides what runs
next. That is [0001](0001-precedence-rule-orion-owns-orchestration.md)
applied to a toolkit that now has its own sequencer.

**Declined:** the workflow engine and everything `specify workflow` runs;
extension hooks and `.specify/extensions.yml`; bundles, catalogs and the
`~/.specify/auth.json` credentials they need; `implement`, `converge` and
`clarify`; the bundled `git`, `agent-context` and `assess` extensions; and
"Orion as a spec-kit extension".

**Adopted alongside, because it owns content and not control:** presets.
The `orion` preset wraps `speckit.specify` to remove its "at most three
markers, best-guess the rest" rule and its interactive clarification loop,
and adds an `## Open questions` section to the spec template — the same
rule Orion's own intent prompt states, in spec-kit's own extension
mechanism, with no sequencing in it.

## Consequences

- This is 0001's second worked example. 0002's objection was
  `/execute-plan`; this record's is the workflow engine plus the fact that
  hook enforcement is prose. Re-proposing spec-kit's engine has to answer
  those two points, not argue that the engine is well built — it is.
- Two of spec-kit's ideas are still worth having and are adopted the way
  0002 adopted superpowers': as Orion-owned gates. The constitution stage
  exists because spec-kit's `plan`, `tasks` and `analyze` all read a
  constitution and Orion had nothing writing one; the analyze stage exists
  because spec-kit's cross-artifact check is exactly a verdict-shaped step.
  Both are gates Orion enforces, which is why neither is delegated
  wholesale.
- `clarify` is declined rather than adopted because Orion already has the
  stronger form: `orion new` asks a human, synchronously, before anything is
  written; `orion answer` walks what a stage could not decide. A stage that
  best-guesses and then interviews an agent is what the `orion` preset
  removes.
- Anything spec-kit's `converge` finds — code that drifted from the spec —
  is a verify-class finding for a later stage to report, never a licence
  for a toolkit to append work to the plan. If it is adopted, it is adopted
  as a report.
- **spec-kit's own files are never edited in place.** Not a template, not a
  script, not a `SKILL.md`. When one of its commands needs to behave
  differently under Orion, the change goes in the `orion` preset
  (`internal/provision/presets/orion/`) and spec-kit composes it through
  `specify preset add`; when the behaviour cannot be expressed as a preset,
  it becomes a gate on Orion's side rather than an edit on spec-kit's. The
  reason is the upgrade: a composed preset survives
  `specify integration upgrade` (verified, OR-398), while an edited file is
  a conflict on every release and, worse, a silent revert. Verified on
  1.0.5.dev0: every template, script and skill a project holds is
  byte-identical to a fresh `specify init` except the `speckit-specify` the
  preset composes. The contract test (OR-397) fails the day that stops being
  true.
- How spec-kit is installed, and where its artifacts live, is
  [0022](0022-per-project-toolkit-install-via-specify-init.md). What a
  feature is, and how a spec changes after it is written, is
  [0023](0023-living-spec-project-vs-feature.md).
