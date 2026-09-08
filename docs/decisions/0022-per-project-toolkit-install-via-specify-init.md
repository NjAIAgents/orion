# 0022: spec-kit is installed per project by `specify init` at provisioning, and Orion pins the feature directory

- Status: Accepted
- Date: 2026-09-07
- Ticket: OR-355 (epic), OR-364 (this record), OR-358 (the code)
- Amends: [0020](0020-a-toolkit-must-ship-skills-and-vendoring-is-global.md)
  — vendoring stays global for a skills repository; it is not how spec-kit
  is installed.
- Related: [0009](0009-canonical-slug-one-name.md),
  [0014](0014-supervised-runs-get-a-curated-config-directory.md),
  [0021](0021-spec-kit-inside-stages.md)

## Context

[0020](0020-a-toolkit-must-ship-skills-and-vendoring-is-global.md) decided
two things: a toolkit ships `skills/<name>/SKILL.md`, and every clone lands
under `<ORION_HOME>/vendor/<repo-name>`, shared by every project on the
machine. Its own postscript then found that a clone of spec-kit is not an
install. spec-kit's commands live in `templates/commands/` as inputs to its
installer; `specify init --here --integration claude` writes
`.claude/skills/speckit-*/SKILL.md` into the project — the layout `Validate`
wanted, produced per project. The amendment was stated in a postscript and
nowhere a reader configuring a toolkit would find it, and the stale clone
under `vendor/spec-kit` kept making `orion doctor` grade a directory no
stage could run.

Where that installer runs is decided by what it is, not by the network:
`specify init --help` states that "project files are scaffolded from assets
bundled inside the specify-cli package, so initialization does not need
network access". It is provisioning -- deterministic, no model, the same
class of work as creating the remote -- and it has to have happened before
the first stage that delegates to spec-kit reads the commands it writes. So
it runs in Orion's own process as a frame step of the chain, not inside a
stage. (An earlier draft of this record said the installer downloads and
that egress denial forced the placement; the placement is right and that
reason was wrong.)

One fact about spec-kit's own resolution decides what Orion must tell it.
`/speckit-specify` derives a two-to-four-word name from the description and
creates `specs/NNN-<that-name>/`, persisting the choice to
`.specify/feature.json`, which is gitignored and machine-local. Its
resolution order (`scripts/bash/common.sh`, `get_feature_paths`) reads the
`SPECIFY_FEATURE_DIRECTORY` environment variable first and only invents a
name when it is unset. Left unset, one piece of work has two names — the
slug [0009](0009-canonical-slug-one-name.md) exists to make unique, and a
directory spec-kit made up — recorded in a file that is not committed.

## Decision

**`orion plan` installs spec-kit into the workspace repository as a frame
step, before any stage, and skips when it is already there.** The step runs
`specify init --here --force --non-interactive --integration claude` --
non-interactive because a chain has nobody at the installer's prompt, force
because a provisioned workspace is never an empty directory -- then `specify
preset add --dev .specify/presets/orion` for the preset
[0021](0021-spec-kit-inside-stages.md) adopts, and commits what they wrote.
A project whose stages name no spec-kit command has nothing to install and
the step reports done. `.specify/` present means done; a
resumed chain never re-initialises. `specify` absent is an error naming the
install line (`uv tool install specify-cli --from
git+https://github.com/github/spec-kit.git`), raised before anything spends.

**Orion exports `SPECIFY_FEATURE_DIRECTORY=<paths.specs>/001-<slug>` to
every supervised run.** One helper, `config.FeatureDir(slug)`, spells that
path for the prompt, the artifact gate, the discovery gate and the
environment, so the four cannot disagree. spec-kit honours the variable
first and persists it to `.specify/feature.json`, so the machine-local file
records Orion's name rather than inventing one.

**Vendoring stays global for a skills repository and is not used for
spec-kit.** `<ORION_HOME>/vendor/<repo-name>` remains where nj-agents and
any toolkit laid out as skills is cloned. `orion doctor` resolves a toolkit
installed inside the project — `.claude/skills` or `.claude/commands` under
the project root — as the first candidate, grades every `toolkit.stages`
command against a file that exists there or in the toolkit root, and FAILs
naming the stage when one does not. `orion doctor --fix` never clones
spec-kit; it prints the install line instead. The stale
`~/.orion/vendor/spec-kit` clone is deleted.

**`orion doctor` reads spec-kit's version and feature roster.** `specify
version --features --json` returns `{"version": …, "features": {…}}`; the
feature keys are what Orion depends on, not the version string, which on a
development build (`1.0.5.dev0`) does not compare as semver. A missing
required feature is a WARN naming it and `uv tool upgrade specify-cli`. No
network is used for this; there is no "latest release" comparison.

## Consequences

- 0020's header gains `Amended by: 0022`. Its first decision — a toolkit
  ships the skills layout — stands unchanged; `specify init` is precisely
  what produces that layout.
- Two projects on one machine may run different spec-kit versions, which
  0020 listed as the reason to revisit global vendoring. Accepted: doctor
  reports the version per project, and a per-project install is what
  spec-kit's own documentation describes.
- Under a delegated `spec` or `plan` stage, the artifact Orion owes is
  `<FeatureDir>/spec.md`, `<FeatureDir>/plan.md` and
  `<FeatureDir>/tasks.md`, and the artifact gate checks those files. Without
  delegation the old `specs/<slug>.spec.md` and `plans/<slug>.md` paths are
  unchanged. Go still decides which artifact a stage owes
  (`internal/supervisor/artifact.go`); a configured command selects between
  two layouts Orion owns and can name no other path.
- `001-` is pinned for the chain's feature. A second feature on an existing
  project needs a counter and an entry point; both are out of scope here
  and recorded in [0023](0023-living-spec-project-vs-feature.md).
- The CLI is pinned to one release (`provision.SpecKitTag`): the gates read
  strings its templates contain, and a contract test against the real CLI
  (`speckit_contract_test.go`, skipped where `specify` is absent) is what
  makes moving the pin a checked change rather than a discovered one (OR-395).
- `specify preset add --dev <path>` copies the preset into the project's own
  `.specify/presets/orion/` and recomposes the installed skill in place;
  nothing is written under `~/.specify`. Verified on 1.0.5.dev0 (OR-382), so
  "per project" above is exact, and the preset's presence there is what the
  toolkit step checks before deciding it has nothing to do.
