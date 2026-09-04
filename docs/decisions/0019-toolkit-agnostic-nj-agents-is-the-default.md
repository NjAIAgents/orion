# 0019: Orion is toolkit-agnostic; nj-agents ships as the default, not a hardcoded dependency

- Status: Accepted
- Date: 2026-09-03
- Load-bearing: yes — a config shape (`toolkit.stages` as a map) enforces this
  by construction, not just by prose; see `internal/config/toolkit.go`.
- Related: [0001](0001-precedence-rule-orion-owns-orchestration.md) (this
  decision is 0001 enforced by shape rather than by prose)

## Context

Every prior integration decision ([0001](0001-precedence-rule-orion-owns-orchestration.md),
[0002](0002-superpowers-declined-as-dependency.md)) assumed nj-agents as
*the* toolkit, because until now it was the only one. That assumption leaked
into code as literal constants and strings rather than a configurable
choice, at four verified sites:

- `internal/njagents/njagents.go:30` — `RepoURL` is a `const` pointing at
  `github.com/navjyotnishant/nj-agents`, not a value read from config.
- `internal/njagents/njagents.go:34` — `RequiredSkills` is a package-level
  `var` naming nj-agents' own skill names (`pre-push-review`, `review-secrets`,
  `pr-describe`, `pm-plan`, `scaffold-project`, `review-tests-build`).
- `internal/njagents/njagents.go:64` — `RequiredDocs` names nj-agents' own
  shared contract file, `CONVENTIONS.md`.
- `internal/supervisor/prompts.go` — stage prompt strings tell the agent to
  invoke specific nj-agents skills by name (`/capture-intent`,
  `/scaffold-project`, `/pm-plan`, and others), rather than reading which
  command a stage should run from anywhere configurable.

Each site was a reasonable shortcut when there was one toolkit to write
against. Together they mean a second toolkit cannot be adopted without
editing Orion's own source — the opposite of the division of labour
[0001](0001-precedence-rule-orion-owns-orchestration.md) describes, where a
toolkit supplies methodology inside a stage and Orion owns everything
around it. A future maintainer reading only the code at these four sites,
without this record, could reasonably conclude that a single hardcoded
vendor was the deliberate design — it was never evaluated and rejected,
it simply hadn't been needed yet.

## Decision

**Orion is toolkit-agnostic.** A project may declare, in `orion.json`, a
`toolkit` block naming a different skill repository and what each stage
delegates to inside it. `internal/config/toolkit.go` is the enforcement
point:

- `toolkit.stages` maps a stage to a **COMMAND**, and never to an order — e.g.
  `"review": "/my-org-review"` — and never a list or an ordering key
  (`order`, `sequence`, `stage_order`, `pipeline` are rejected by name with
  an error that cites this ADR).
- A map can only answer "what does the review stage run", which is
  methodology inside a stage. A list could answer "what runs after review",
  which is control flow across stages. The two look nearly identical in
  JSON, which is why the shape is validated in `parseToolkit` rather than
  merely documented — a maintainer relaxing the map to accept a list later
  would silently regrant a toolkit the one authority it must never hold.
- **Orion retains ownership of artifact paths, gates and verdicts**
  regardless of which toolkit a stage's command belongs to. A configured
  command is invoked as one step inside a stage Orion already sequences; it
  reports a verdict back (PASS/WARN/BLOCK, an exit code) and does not decide
  whether the next stage runs, does not merge, and does not keep its own
  competing record of what happened. This is [0001](0001-precedence-rule-orion-owns-orchestration.md)
  applied to a *configurable* toolkit rather than the one Orion happened to
  be written against.

## Consequences

- **nj-agents remains the shipped default.** `njagents.RepoURL`,
  `RequiredSkills` and `RequiredDocs` are unchanged and still govern what
  Orion validates and clones when a project declares nothing. Nothing about
  this decision migrates an existing project or edits its `orion.json`.
- **An absent `toolkit` block is a supported, zero-change configuration.**
  `defaultToolkit` fills `Toolkit.Repo` from `njagents.RepoURL` and leaves
  `Toolkit.Stages` empty, and an empty stage command is a normal answer that
  falls back to Orion's own built-in prompt — never an error and never
  "run nothing". A project that never opens `orion.json` keeps behaving
  exactly as it did before this ADR.
- The four sites named above are not being rewritten by this record. They
  remain correct as nj-agents' own defaults; what changes is that a second
  toolkit no longer requires editing them, because `toolkit.stages` is the
  configurable seam that sits in front of them.
- Four choices made at the same planning pass, not evaluated as
  alternatives but worth recording alongside the shape decision above:
  - **Foreign cloning requires confirmation.** Orion will clone a
    project-declared `toolkit.repo` that is not nj-agents, but only with the
    operator's confirmation first — a config file is not sufficient
    authorization to run `git clone` against an arbitrary URL unattended.
  - **Running the toolkit's `install.sh` stays optional**, not automatic on
    discovery. `Clone` deliberately does not invoke it: cloning copies
    files, while running a freshly downloaded repository's installer
    executes third-party code and edits the user's runner configuration —
    two different risk levels that a single "toolkit found" event must not
    conflate.
  - **Both stage-name spellings are accepted, and a collision between them
    is rejected.** `canonicalStages` maps synonyms (`design`/`spec`,
    `test`/`verify`, `ship`/`pr`, `implement`/`build`) to one canonical key,
    but if a project's `toolkit.stages` names the same canonical stage twice
    under different spellings with different commands, `parseToolkit`
    errors rather than silently picking whichever key map iteration reached
    last.
  - **The vendor directory is derived from the repository name**, not fixed
    at `vendor/nj-agents`. `VendorDirFor` keys the clone path off
    `repoLeaf(repoURL)`, so a project's own toolkit lands beside — not on
    top of — Orion's managed nj-agents clone. The default repo still
    resolves to the same `vendor/nj-agents` path, so no existing clone
    moves.
- This makes [0001](0001-precedence-rule-orion-owns-orchestration.md)
  **enforced by shape rather than by prose**: before this decision, "a
  toolkit never owns control flow across stages" was a rule a reader had to
  trust an integrator to have followed. After it, `toolkit.stages` cannot
  *represent* an order at all — `parseToolkit` rejects the list shape and
  the ordering-key names outright — so the rule holds even for an
  integrator who never read 0001.
