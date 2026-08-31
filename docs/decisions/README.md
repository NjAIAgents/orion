# Architecture decisions

One file per decision: what was decided, why, and what it binds later work
to. Read before re-proposing something that looks like a gap — it may
already have been decided against, on purpose.

- [0001](0001-precedence-rule-orion-owns-orchestration.md) — Orion owns orchestration; a toolkit supplies methodology inside a stage, never control flow across stages
- [0002](0002-superpowers-declined-as-dependency.md) — Superpowers declined as a dependency; three of five ideas adopted natively
- [0003](0003-ponytail-scoped-to-development.md) — Ponytail kept, scoped to development only
- [0004](0004-no-sqlite-file-based-storage.md) — No SQLite; config, event logs and the audit trail stay as files
- [0005](0005-agent-roster-is-global.md) — Agent roster is global, not per-repo
- [0006](0006-new-and-plan-are-sequential-phases.md) — `orion new` and the plan stage are sequential phases, not two front doors
- [0007](0007-auto-effort-standing-preference.md) — Auto effort is a standing preference, not per-ticket
- [0008](0008-parallelism-level-ordering.md) — Parallelism ships level 3, then level 1, then level 2
- [0009](0009-canonical-slug-one-name.md) — One canonical slug names the Jira project, workspace and git repo
- [0010](0010-routing-vocabulary-is-a-published-contract.md) — The routing vocabulary is a published contract, and five actors are routable
- [0011](0011-orion-owns-the-landing-queue.md) — Orion owns the landing queue; GitHub's merge queue is not adopted
- [0012](0012-one-workspace-per-tracker-project.md) — A tracker project gets one workspace; a second `orion plan` refuses
- [0013](0013-new-creates-the-tracker-project-not-a-workspace.md) — `orion new` creates the tracker project and no workspace
- [0014](0014-supervised-runs-get-a-curated-config-directory.md) — a supervised run gets a config directory Orion curates, and no MCP servers
- [0015](0015-ci-authority-under-a-merge-ref.md) — under a merge ref Orion is the gate on landing, and `develop` keeps a post-merge check as the backstop
- [0017](0017-integration-state-machine-not-a-job-queue.md) — the integration state machine carries the sophistication; the queue is Go channels and JSON, with a git SHA recorded at every transition
