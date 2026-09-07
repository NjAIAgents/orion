# 0020: A toolkit must ship a `skills/` directory, and vendoring is global to the machine

- Status: Accepted
- Date: 2026-09-06
- Load-bearing: yes — `internal/toolkit.Validate` rejects a clone with no
  `skills/` directory, and `VendorDirFor` puts every clone under `ORION_HOME`.
- Related: [0019](0019-toolkit-agnostic-nj-agents-is-the-default.md) (this
  records two things 0019 assumed without stating, both found by trying to
  adopt the toolkit 0019 named as its example)

## Context

[0019](0019-toolkit-agnostic-nj-agents-is-the-default.md) made Orion
toolkit-agnostic and named spec-kit as the alternative a project might
declare. Every child of the epic shipped. Nobody then adopted spec-kit, and
two assumptions inside 0019 went unstated because nj-agents satisfied both
without anyone noticing they were assumptions.

Both surfaced the first time somebody asked what a real project was actually
running:

**A toolkit is assumed to BE a skills repository.** `Validate` requires a
`skills/` directory before it will call a directory a toolkit at all
(`hasSkillsDir`, `internal/toolkit/toolkit.go`). nj-agents is laid out that
way. **spec-kit is not**: it is a Python CLI (`src/specify_cli`) whose
`specify init` copies `templates/commands/*.md` into a project's own
`.claude/commands/`. Cloned into `vendor/spec-kit`, it is reported as not
installed — correctly, by the rule as written, and uselessly, since the
commands are right there under a different name.

**A toolkit is assumed to be installable once per machine.** `VendorDirFor`
puts every clone under `<ORION_HOME>/vendor/<repo-name>`, shared by every
project Orion manages. nj-agents suits that: it is a set of skills, and one
copy serves everything. spec-kit's own model is the opposite — it installs
*into* a project, and different projects may hold different versions of its
commands.

Neither of these is written down anywhere a reader would find them. The
practical cost is a session spent asking the operator questions the
repository should have answered: which toolkit a project runs, where a clone
lands, and why the combination that was agreed had never actually run.

`docs/USAGE.md` made it worse rather than better. Its spec-kit example named
`/specify`, `/plan`, `/tasks`, `/breakdown` and `/analyze` — every command
missing spec-kit's own `speckit.` prefix, one (`/breakdown`) not existing at
all, and each stage mapped one slot out of place. A different line of the
same document used the correct `/speckit.tasks`. Anyone configuring a toolkit
by copying that example got a config whose stages silently fell back to
Orion's built-in prompts, which is indistinguishable from having configured
nothing.

## Decision

**A toolkit must ship a `skills/<name>/SKILL.md` layout.** That is what
`Validate` checks and what `HasSkill` reads, and it stays the contract. A
repository organised otherwise is not adopted by pointing `toolkit.repo` at
it; it needs either an adapter that presents its commands in that shape, or a
decision to widen discovery — recorded separately, not assumed.

**Vendoring is global, under `<ORION_HOME>/vendor/<repo-name>`.** Stated
here because it was only ever a code comment: a toolkit is shared by every
project on the machine, one clone per repository, and `toolkit.dir` is the
escape hatch for a project that needs its own. The alternative — vendoring
into each project root, pinned with its code — buys per-project
reproducibility and was considered. It is rejected for now on the grounds
0019's implementation already gives: a tool that writes clones into
someone's source tree is harder to update safely and noisier in a diff. That
trade is worth revisiting if two projects ever need different toolkit
versions; it is not worth pre-solving.

**Every command in a config example must be one the toolkit publishes.** A
plausible-looking command name is worse than no example, because a stage
whose command does not resolve falls back to the built-in prompt and reports
success.

## Postscript, 2026-09-07

The first real run under this decision found that **cloning spec-kit was the
wrong way to install it**. `uv tool install specify-cli --from
git+https://github.com/github/spec-kit.git`, then `specify init --here
--integration claude` inside the workspace, writes
`.claude/skills/speckit-*/SKILL.md` — the same layout nj-agents ships, and the
one `Validate` wanted all along. The raw clone's `templates/commands/` are
inputs to that installer, not skills.

So the discovery widening recorded above is still correct and no longer the
point: a toolkit installed the way its own documentation says produces the
skills layout. Two things the run established that the documentation did not:
the Claude integration installs `speckit-specify` with a HYPHEN while the
README's prose writes `/speckit.specify` with a dot, and `specify init` is
per-project, which cuts against the global vendor model this ADR chose.

## Consequences

- spec-kit IS adoptable, through its own installer rather than a clone. See
  the postscript: `specify init --here --integration claude` writes the skills
  layout, and `orion doctor` grades it when `toolkit.dir` points at that
  repository.
- A project running Orion today runs nj-agents, whatever its `orion.json`
  says, unless that file names commands the discovered toolkit really has.
- `orion doctor` reports which toolkit resolved and how. That output is the
  answer to "what is this project actually using", and it is the first thing
  to read before assuming a configuration is in force.
