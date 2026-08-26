# Changelog

Notable changes per release. Written for someone deciding whether to upgrade
and what to watch afterwards, so each entry says what changed **and what it
now refuses to do**.

## v0.4.3

Two more failures with the same shape: an unattended watcher hit a stop
condition and had no way to tell anyone, or told someone who could not
hear. FCIA-8's budget checkpoint printed an answerable question to a
terminal nobody was watching and re-asked it every two minutes with no
route to a yes; the Slack channel wired up at init looked audited and
was reachable by every message, but its only member was the bot.
Neither failure showed up as an error — both looked like success until
someone went looking. This release refuses two things it used to allow:
spending past a budget checkpoint with no way to answer it, and calling
a Slack channel a working audience when it's just the bot.

### Added

- Budget checkpoints now ask instead of stopping silently. `budgetGate`
  replaces `budgetBlocked` and puts the question in front of a human by
  both routes an approval already uses: a Slack message with a
  checkmark reaction, read on the next pass, or a terminal prompt when
  someone is actually there. It asks once per threshold crossed, not
  once per tick, and drops the stored question once the checkpoint
  clears so the next threshold is asked fresh rather than answered by a
  stale tick. Consent covers exactly one checkpoint — acknowledging 50%
  does not authorize 75%. A non-interactive stdin (the unattended
  watcher case) answers no rather than hanging or defaulting to yes.

### Fixed

- `orion init` and `orion doctor` previously judged a Slack channel
  audience-ready by whether Orion had just created it, so a channel
  found and reused from an earlier broken init — the exact case that
  stranded fcia — skipped the check entirely and only warned when no
  invite list was configured, a warning easy to miss in a long init
  scroll. `ensureAudience` replaces that logic: it runs for a found
  channel too, verifies membership actually took after inviting instead
  of assuming the invite worked, and tries to auto-invite the operator
  by looking up `git config user.email`, persisting the resolved id so
  later runs don't repeat a lookup that may not be permitted. If no
  audience can be found it now fails with the exact fix instead of
  warning. Also added to `orion doctor` as a `slack audience` check,
  since a channel can lose its only human member long after init ran.

## v0.4.2

Six fixes from the first real unattended runs of FCIA-6 and FCIA-7. Each one
shares a shape: something reported success while the thing it claimed to
have done had not actually happened.

The sharpest of these was Slack delivery: `orion slack test` checked the
token, the channel, the post, and the approval scopes — all four passed —
while every message landed in a private channel with no human members.
Slack accepted them silently; nobody could read one, search for one, or
know it existed. A related bug meant the channel a run actually posted to
depended on where the command was invoked from, because two different code
paths resolved it two different ways — one read the workspace record, the
other re-derived it from `orion.json` by slugifying the repo's absolute
path. Standing in a checkout could produce a channel name built from your
own home directory. Both now go through one resolver, and `orion slack
test` fails loudly when the audience is nobody.

The job-limit fix matters for anyone running `--max-jobs`: because a tick
reconciles before it starts new work, a tight cap could collect the ticket
it had just finished, push, open the PR, move it to ci-wait, and exit —
with no watcher left running to notice CI go green, ask for approval, merge,
or prune the worktree. The ticket would sit "done" in CI with nobody told.
The cap now only stops *starting* new work; it keeps draining until
everything already in flight finishes.

The rest are smaller but still change visible behavior: merge conflicts
between two in-flight branches used to be silently retried forever, because
`gh` was never asked whether a merge was actually possible, so an impossible
merge looked identical to any other transient failure. `orion collect` used
to ask for approval and immediately exit — correct for a watcher, but wrong
for the manual invocation people reach for specifically because no watcher
is running. And two numbers in Orion's own output were simply wrong: a
context-usage figure that claimed 991% of the window on a real run (true
occupancy: 16%), and a "nothing is waiting on you" message after a
develop merge that hid the still-outstanding develop → main merge.

### Fixed

- `orion slack test` passed all four checks (token, channel, post, approval
  scopes) while posting into a channel with no human members — messages
  were delivered but unreadable and unsearchable by anyone; the test now
  checks channel membership and fails when the audience is nobody
- Channel resolution differed depending on where `orion slack test` was
  run — with a workspace record it used that record, without one it
  re-derived the channel from `orion.json` by slugifying the repository's
  absolute filesystem path, producing names like
  `users-navjyotnishant-desktop-github-njai`; both paths now share one
  resolver, and the output states when the workspace record overrides
  `orion.json`
- `tell()` discarded the specific reason a message wasn't sent, so an
  unconfigured project failed silently with no explanation surfaced
  anywhere
- `--max-jobs 1` (and any tight cap) could collect a ticket, push, open its
  PR, and exit with no watcher left running — CI going green afterward
  triggered no approval request, no merge, and no worktree cleanup; the cap
  now only blocks starting new work and keeps draining in-flight tickets
  until they finish
- A merge conflict between two in-flight branches was indistinguishable
  from a transient failure, so Orion retried the same impossible merge
  every tick indefinitely without ever telling a human to rebase; conflicts
  are now reported once per HEAD (and again after a rebase that doesn't fix
  it), with the ticket held in ci-wait so no re-labelling is needed to
  resume
- `orion collect` requested approval and exited immediately, which is right
  for a watcher's next tick but wrong for the manual path — used
  specifically because no watcher is running; it now waits (30 minutes by
  default, `--wait`/`--no-wait`), and Ctrl-C confirms the request still
  stands
- Context usage was reported as cumulative session throughput divided by
  window size, which always exceeds 100% on a long run because
  `cache_read` re-counts the whole cached prefix every turn — one real log
  showed 991% claimed against 16% true occupancy; it's now a peak measured
  over turns read off the stream, and the unfixable `budget.ContextPressure`
  metric has been removed rather than patched
- The post-merge message said "Nothing is waiting on you" after landing on
  develop, implying a release had happened, when a develop → main merge was
  still outstanding; it now names that merge explicitly, using the
  project's configured branch names instead of a hardcoded `develop`
- the same message reported "your checkout was fast-forwarded", which is
  git's word for it and left the reader to work out whether they still had
  to pull; it now answers that question directly — the merged changes are
  already in the local copy and there is nothing to do — and when the sync
  was refused it hands over the exact command to fix it

## v0.4.1

`orion init --force` no longer touches `orion.json`.

Before this, `--force` was the documented repair for broken hooks (e.g. after
`brew cleanup` moved the binary) — but it rebuilt hooks *and* silently reset
the whole config file to defaults: `require_approval`, `merge_approvers`,
`mention`, the `ci` block, `channel_prefix`, all reverted. A reverted prefix
can rebind Slack notifications to a different channel than the one a project
actually uses, with no error, because from `init`'s point of view it did
exactly what it was asked.

`--force` now rebuilds only the wiring (hooks, symlinks) and refuses to
reset configuration — a person's policy decisions stay put. To actually
start over, delete `orion.json` first; that's a deliberate, reversible act
while it's still in git. `orion doctor`'s repair hint now says as much, so
the moment someone reaches for `--force` they're told it's safe.

### Fixed

- `orion init --force` reset `orion.json` to defaults while repairing hook
  wiring, silently discarding `require_approval`, `merge_approvers`, `ci`
  settings, and `channel_prefix` — a reverted `channel_prefix` could rebind
  Slack delivery to the wrong channel with no error

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
