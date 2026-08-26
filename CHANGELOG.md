# Changelog

Notable changes per release. Written for someone deciding whether to upgrade
and what to watch afterwards, so each entry says what changed **and what it
now refuses to do**.

## v0.4.0

The release where Orion runs a ticket end to end without being asked.

Before this, `orion` guarded an agent you started yourself. Now a label on a
Jira ticket is enough: it claims the ticket, works it in a sandbox, answers
its own design questions, opens a described pull request, waits for CI, asks
Slack for approval, merges, closes the ticket, fast-forwards your checkout
and cleans up after itself.

### The loop this exists for

**Architect and PM advisors.** When the implementer stops to ask something,
Haiku classifies the question and either the architect (`spec.md`, `plan.md`)
or the PM (`intent.md`) answers it — grounded in committed artifacts, citing
the clause, or **refusing**. The decision is committed to `docs/decisions/`
and the implementer is resumed in the same session rather than restarted.

A refusal is a success. It says the design is incomplete, which is a human's
decision and then an amendment to the document, so the next ticket does not
re-ask at full price.

### New commands

- `orion watch` — run the queue by itself: reconcile, start one job, sleep
- `orion collect` — finish tickets awaiting CI: merge, close, refresh, prune
- `orion sandbox` — see, enter or prune the worktrees agents worked in
- `orion logs <KEY> -f` — live event log, per project or per ticket
- `orion slack test` — prove Slack delivery, or name exactly what is broken

### Pull requests describe themselves

A PR opened by Orion used to just restate the ticket summary and a commit
count. It now reads the branch's whole diff and commit history and drafts a
real summary / what-changed / why / test-plan via the `nj-agents` pr-describe
skill — read-only, it cannot push or commit anything itself. Any failure in
that pass falls back to the old two-line description rather than blocking
the PR.

### Gates and safety

- **CI is scaffolded at adoption.** `orion init` writes `scripts/test.sh` and
  a workflow that runs it — one entry point for CI, for you, and for the
  agent. Without checks, Orion has no honest verdict to gate a merge on.
- **Merges need a person.** With `slack.require_approval`, a passing branch
  asks the project channel; an allowlisted ✅ merges it. An empty allowlist
  means nobody. A rejection beats an approval however late it arrives.
- **A red build can be sent back to the agent**, bounded by an attempt
  ceiling *and* by an identical-failure check that stops sooner. Off by
  default.
- **Merges rebase by default**, so each commit keeps its author. Squash
  collapses a branch into one commit authored by whoever opened the pull
  request, which erases the agent from the trunk's history.
- **Never `--admin`.** Orion does not bypass branch protection.

### Budget

`budget.weekly_tokens` no longer has to be invented. Orion reads the plan's
own `rate_limit_info` from each run and a watcher sleeps until the real reset
time. There is **no percentage** available to any process outside the Claude
Code TUI, so Orion gets a yes/no and a reset time rather than a warning at
80%.

### Observability

Runs are narrated live. The previous `--output-format json` emitted a single
object at exit, so a forty-minute run was indistinguishable from a hung one.
Every event now carries the model that produced it — a single ticket is
worked by three.

### Fixed

- guardrails silently stopped firing after `brew cleanup`, because hooks were
  pinned to a versioned Cellar path
- `orion doctor` could not say which credential source Jira was failing from,
  so a stale shell export shadowing `config.env` looked like a broken token
- Slack messages were never sent: the channel was created but never recorded,
  so `notify` was handed an empty channel and skipped delivery without error
- every Slack call sent a JSON body, which cursor-paginated reads ignore —
  private channels were invisible and pagination never advanced
- the sandbox served stale policy, so a committed config change appeared to
  do nothing
- a merged ticket now clears every label Orion owns, not just
  `orion-ci-wait` — previously it could keep a stale `orion-failed` label
  from an earlier failed attempt, so `orion queue` reported the same ticket
  as failed and Done on the same line
- Slack notifications about the worktree/checkout outcome (removed vs kept,
  fast-forwarded vs refused) are now told directly rather than assumed, so a
  Slack message can no longer contradict what the terminal reports one line
  above it
- `orion watch fcia --max-jobs 1` read the `1` as a second project — a flag
  value after a value-taking flag could be misread as a positional project
  key
- a mistyped project name looked identical to an empty queue; an unknown key
  is now refused, naming what IS registered
- `orion watch --dry-run` without `--once` looped forever, re-printing an
  identical rehearsal every two minutes since a dry run changes nothing
  between ticks — it now rehearses once
- a permanent (non-transient) error was retried forever; only transient
  errors keep the retry loop going now

### Upgrading

Nothing in `orion.json` is required to change; every new field defaults off
or to the previous behaviour. To turn on the new gates:

```json
"slack": { "require_approval": true, "merge_approvers": ["U01234567"] },
"ci":    { "auto_fix": true, "max_fix_attempts": 3 }
```

Slack approvals need `reactions:read`, `reactions:write`,
`channels:history`, `groups:history` and `users:read`, then an app
**reinstall** — new scopes never apply to an already-issued token.

## v0.3.3

Hook paths pinned to a stable binary location, and `orion init` reports its
own errors rather than failing quietly.

## v0.3.2

`orion doctor` names the credential source when Jira authentication fails.
