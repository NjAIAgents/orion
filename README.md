# Orion

An AI-native SDLC orchestrator. Hand it an idea; it takes the work from
intent to a reviewable pull request through a committed artifact chain, in an
isolated sandboxed workspace, under deterministic guardrails.

Built on the plays in Anthropic's
[AI-native SDLC playbook](https://claude.com/blog/the-ai-native-sdlc-playbook).

> **Status: builds and passes its tests** on go1.26.5 darwin/arm64.
> `go build`, `go vet` and `go test ./...` are clean, `gofmt` is clean, and
> the hooks, `doctor` and workspace provisioning have been exercised end to
> end against the real binary. The tracker's Jira calls have **not** been run
> against a live instance. See [Known gaps](#known-gaps).

## Why a binary

Most of this could have been shell scripts in a plugin. Three things could
not:

1. **A hook only fires when the agent calls a tool.** It cannot stop an agent
   burning turns without tool calls, cannot enforce wall clock, and cannot
   kill a wedged process. Only a parent process can.
2. **Parallel worktree sessions share one budget.** That is cross-process
   mutable state with locking.
3. **A quota wall needs waiting out.** Parse the reset, sleep, retry, notify.

So Orion is one static binary, no cgo, empty `go.sum`, builds offline.

## Install

Source lives privately in `NjAIAgents/orion`; binaries are published to the
public `NjAIAgents/orion-releases` so Homebrew and Scoop can fetch them
unauthenticated.

```bash
brew install navjyotnishant/tap/orion     # macOS, Linux
scoop bucket add navjyotnishant https://github.com/navjyotnishant/scoop-bucket
scoop install orion                        # Windows
```

From source:

```bash
git clone https://github.com/NjAIAgents/orion && cd orion
make test     # build, vet and the full test suite
make install  # to ~/.local/bin
orion doctor --fix
```

## Releasing

```bash
make release TAG=v0.1.0 DRY=1   # rehearse, publishes nothing
make release TAG=v0.1.0
```

This uses the `gh` login already on your machine, so there are no tokens to
create or rotate. It refuses to release from a dirty tree, from a branch other
than `main`, or from a `main` that differs from origin, and it runs build, vet,
gofmt and the full test suite before anything is published. Then it tags,
builds every archive, publishes to `NjAIAgents/orion-releases`, and updates the
Homebrew formula and Scoop manifest.

What it cannot do is prove the build works on a machine other than yours. The
GitHub Actions workflow does that, and is the reason it still exists, but it
runs on GitHub's servers where your `gh` credentials do not reach, so it needs
`RELEASES_GITHUB_TOKEN` and `TAP_GITHUB_TOKEN` as repository secrets:

```bash
gh secret set RELEASES_GITHUB_TOKEN --repo NjAIAgents/orion   # contents:write on orion-releases
gh secret set TAP_GITHUB_TOKEN      --repo NjAIAgents/orion   # contents:write on the tap and bucket
```

## nj-agents is a hard dependency

Orion delegates review, secret scanning, test and build verification, PR
authoring and PM decomposition to
[nj-agents](https://github.com/navjyotnishant/nj-agents). Those stages have
no fallback, so `orion doctor` grades a missing toolkit as FAIL rather than
a warning.

```bash
orion doctor --fix        # clone it if absent
orion njagents status     # where it is, which commit, how stale
orion njagents update     # nj-agents ships independently; pull its changes
```

Discovery order is deliberate: an explicit `delegation.nj_agents_dir`, then
`ORION_NJ_AGENTS_DIR`, then resolving an installed skill's symlink back to
its clone, then Orion's own managed clone under `$ORION_HOME/vendor`. Your
copy always wins over Orion's, because two clones drifting apart with Orion
silently using the stale one is a genuinely nasty failure.

Which clone it is decides what Orion may do. A **global** install is yours,
very possibly the repository you develop nj-agents in: Orion reads it and
never writes to it, with no override, because anyone who wants it updated
can run `git pull` themselves. **Orion's own** clone, fetched by
`doctor --fix` when you had none, is Orion's to maintain and updates without
ceremony.

For the same reason `orion njagents install --project` is usually
unnecessary and says so: a global install is already visible to every
`claude` run, whatever directory it starts in. It exists only for the
fallback case, so that Orion can wire its own clone into a workspace without
modifying your `~/.claude`.

The check resolves the symlink rather than trusting `~/.claude/skills`.
Skills install as links back to a clone, and the shared contract they all
read (`CONVENTIONS.md`) lives at that clone's root, two levels up. Looking
only in the skills directory passes while the file the skills depend on is
missing.

`orion doctor` checks the Claude CLI, git identity, `gh` auth, OS sandbox
availability and your project config, and grades each OK / WARN / FAIL. Only
FAIL blocks.

**Full instructions: [docs/USAGE.md](docs/USAGE.md)** — installing, using
Orion on a new idea, adopting it inside an existing repo, and tuning the
guardrails.

## Use

```bash
orion new "customers should see claim status in the portal"
orion run  <id> --stage intent
orion run  <id> --stage spec
orion run  <id> --stage plan
orion provision <id>                 # remote repo, branches, Jira project
orion run  <id> --stage decompose    # /pm-plan tree, approved before creation
orion run  <id> --stage build
orion status <id>
```

## Branch model

Two long-lived branches, created at `git init` before any work can start:

- **`main`** is the release branch.
- **`develop`** is the integration branch and the base for every pull request.

Every task gets its own branch cut from `develop`, merges back into
`develop`, and `develop` reaches `main` later through its own reviewed pull
request.

Both are push-protected by the gate hook, and both get server-side protection
at provisioning time. Protecting only `main` would leave the pull request
into `develop` optional, and an optional review gate is not a gate.

It is `develop` rather than `dev` on purpose: `dev` already means the dev
*environment* in the `autonomy` block, and one word meaning two things in the
same config file is a trap.

Inside Claude Code, `/orion:start`, `/orion:next`, `/orion:status`,
`/orion:learn`.

## The artifact chain

```
docs/intent/<slug>.md -> spec.md -> plan.md -> diff+tests -> PR -> incident
```

Each stage ends by committing an artifact; the next begins by reading it.
Coordination lives in files, not in a conversation, so no stage depends on
another's context surviving. The chain of commits is the audit trail.

## Guardrails

Enforced by hooks, deterministically. A skill makes a violation unlikely; a
hook makes it impossible.

| Hook | Stops |
|---|---|
| **breaker** | identical repeated calls, repeated failure of one command, consecutive failures, tool-call budget, edits without verification, blast radius, wall clock |
| **gate** | production deploys with no named authorization, pushes to the default branch, force pushes, hard reset onto a remote ref |
| **shield** | edits to protected paths, edits to test files during a fix, implementation before a plan exists |

The breaker is wired to **both** PreToolUse and PostToolUse. PostToolUse
counts what happened; PreToolUse refuses the next call. Wiring only one
produces a breaker that reports but never stops.

Every block message names a route forward. A block that only says no gets
worked around.

Tune in `orion.json`. A limit of `0` restores the default rather than meaning
unlimited: "no limit" is never a safe reading of an absent value in a circuit
breaker.

## Isolation

`orion new` provisions:

```
$ORION_HOME/projects/<slug>-<id>/
├── repo/          the project, git initialized
├── worktrees/     for parallel sessions
└── .orion/
    ├── task.json      idea, stage, status, run history
    ├── settings.json  generated: permission denies + OS sandbox
    ├── state/         breaker counters
    └── logs/          full transcript per run
```

Three layers: a dedicated directory, tool-level permission denies, and the
OS sandbox with a network allowlist and credential denies. The supervisor
also strips cloud credentials from the child environment, so a misconfigured
sandbox is not the only thing between an agent and your AWS keys.

**The OS sandbox is not a VM.** It stops credential reads and network egress.
It does not defend against a determined exploit. For untrusted code use
`--container`.

## Quota handling

On a provider limit, Orion parses the reset time, waits, retries, and tells
you. Waits longer than 90 minutes are not sat through: it records a resume
time and hands back rather than holding a process open for hours.

An estimated wait is always labelled an estimate. Presenting a guess as a
stated reset time is how a tool loses trust.

Notifications go to stdout always, plus desktop notification, plus
`ORION_NOTIFY_WEBHOOK` (Slack-compatible) or `ORION_NOTIFY_COMMAND`.

## Cross-project memory

A lesson learned in one project should not be relearned in the next.

```bash
orion lessons add "Money is BigDecimal, never double"
orion lessons list
```

Two constraints keep this from rotting:

**Scope is earned.** A lesson starts scoped to its own project. It reaches
other projects only after it actually recurs in a different one. Recurring
five times in one repo proves it matters there and proves nothing about
anywhere else.

**The injected block is capped.** The agent reads `CLAUDE.md` in full every
session, so an unbounded list degrades every session invisibly. Capped at 25,
ranked by recurrence weighted toward recency, expiring after 90 days.
Hand-written content outside the managed markers is preserved.

## Relationship to nj-agents

Orion delegates review, secret scanning, test/build verification and PR
authoring to [nj-agents](https://github.com/navjyotnishant/nj-agents) rather
than reimplementing them. See [docs/nj-agents-integration.md](docs/nj-agents-integration.md),
including the deliberate carve-out from its propose-never-act contract.

## Known gaps

Read these before relying on it.

- **Branch protection cannot be enabled on this repository.** GitHub returns
  403 for branch protection on a private repo without a paid plan, so the
  human gate on `main` and `develop` is not enforced server-side. Orion's gate
  hook still refuses pushes to both, but that constrains the agent only, not
  a human with a terminal. Verified, not assumed.
- **The Actions release workflow is manual (`workflow_dispatch`).** `make
  release` is the default path and needs no tokens; a tag-triggered workflow
  would fail on every release for want of secrets that deliberately do not
  exist, and a permanently red CI teaches you to ignore it.
- **The Jira REST calls have never touched a live instance.** Shapes and the
  project-creation permission key are inferred. The key is discovered at
  runtime rather than hardcoded, and an unrecognised key is treated as
  "unknown" rather than "denied", but a real `orion doctor` run against your
  Jira is what would confirm it.
- **Unparseable hook input exits 0, allowing the call.** A malformed payload
  from a future harness version must not brick a session. The cost is that a
  harness change could silently disable enforcement. `orion doctor` does not
  detect this.
- **Token budget is proxied by tool-call count**, not real tokens. Hooks are
  not given token counts. True accounting needs the OpenTelemetry export,
  which is after the fact.
- **Quota error wording is not a stable contract.** Detection is a pattern
  list that will need extending. A miss degrades to treating quota as an
  ordinary failure, which is annoying rather than dangerous.
- **The nj-agents mapping is unverified**, derived from its README. In
  particular it is unknown whether those skills work from a non-interactive
  `claude -p` run, which is how the supervisor drives them.
- **State locking is best-effort** with a 3 second timeout, then proceeds
  unserialized and says so. A wedged lock blocking every tool call would be
  worse than a briefly racy counter.
- **Branch protection fails on free-plan private repositories.** Provisioning
  reports this rather than swallowing it. Orion's gate hook still refuses
  pushes to `main` and `develop`, but that constrains the agent only; a human
  with a terminal is unconstrained until server-side protection exists.
- **Creating a Jira project per idea accumulates.** Keys are globally unique
  per instance, capped at 10 characters, and a non-admin cannot delete a
  project. Set `tracker.project_key` to bind an existing project instead.
- **Auto-merge is off by default** and should stay off until `evals/` holds
  at least 20 real cases. Green means nothing when the suite is empty.

## Layout

```
orion/
├── cmd/orion/              CLI entry
├── internal/
│   ├── config/             orion.json, defaults that never widen on error
│   ├── state/              per-session counters, file-locked
│   ├── hook/               breaker, gate, shield + the hook protocol
│   ├── workspace/          isolated project provisioning, settings generation
│   ├── supervisor/         claude -p process control, stage prompts
│   ├── quota/              exhaustion detection, reset parsing, backoff
│   ├── lessons/            cross-project memory
│   ├── notify/             desktop, webhook, custom command
│   ├── doctor/             preflight
│   └── match/              glob with ** support
├── skills/                 kalp (spec+plan), forge (build); intent is delegated
├── commands/               /orion:start, next, status, learn
├── hooks/                  hooks.json + dispatch shim
├── templates/              intent.md, spec.md, plan.md, orion.json
└── docs/
```

## License

MIT
