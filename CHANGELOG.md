# Changelog

Notable changes per release. Written for someone deciding whether to upgrade
and what to watch afterwards, so each entry says what changed **and what it
now refuses to do**.

## Unreleased

## v0.7.2

### Added

- A cost report is now posted to the ticket when Orion transitions it to Done,
  and printed to the `watch`/`collect` console the moment the merge is
  announced — both rendered by the same code path, so the two cannot disagree.
  It totals every agent run the ticket caused (the implementation run, each
  fix-loop re-entry, and runs that died on a timeout, breaker or quota), broken
  down per actor: runs, turns, input, output, cache creation and cache reads
  reported separately, wall time, and the estimated USD the runner itself
  reported per session. Runs that failed are marked, and a run whose usage
  never arrived is stated rather than dropped — the report then says its totals
  are a floor. Re-running `collect` on a closed ticket never posts it twice.

### Fixed

- A passing verification command (`go test`, `make test`, etc.) no longer counts toward the identical-repeat loop breaker. Re-running the tests is the normal edit-test cycle and exactly what the unverified-edits breaker demands after every edit; counting it as a loop made the two breakers trip each other. A failing verify still counts (OR-124).
- A `claude -p` subprocess that exits cleanly without ever emitting its own stream `result` line (killed externally mid-run, a crash after a partial flush) is now reported as a failed run with a clear reason, instead of reading as a silent success with no cost recorded and an empty session id (OR-127).
- Every `gh` call on the `orion watch`/`orion collect` hot path (PR status, merge, prune, and the fix loop's CI-log fetch) is now bounded by a 45s timeout, so a hung `gh` process can no longer block the watcher indefinitely with nothing on the console to explain why (OR-128).
- `orion collect` prints a line before searching Jira for tickets awaiting CI, so the first network call of every watch tick has visible feedback rather than possible silence (OR-128).

## v0.7.1

### Fixed

- The sub-task budget scaling now actually applies on the watch path. `orion
  watch` filled its flag defaults (120 turns, 90 minutes) in as EXPLICIT
  values, and an explicit bound always wins — so a story's `120+25N` turn
  allowance could never trigger, and a four-sub-task story died at turn 121
  after $17 of opus. The flag default is now the zero sentinel and the real
  defaults live in `turnsFor`/`minutesFor` beside the scaling; a childless
  ticket keeps exactly 120 turns and 90 minutes, a typed flag still wins as
  typed, and the chosen budget is printed at claim time so a wrong number is
  visible before it costs anything.

- `orion collect` no longer reads config from the sandbox clone, which went stale the moment the config it needed (like `work_branch`) changed in the user's checkout, and then kept itself stale. Config now always loads from the registered source checkout, and the sandbox is refreshed against it (OR-118).
- The merged message and post-merge refresh now name the branch the pull request actually merged into, taken from the forge's own base ref, instead of assuming the configured work branch (OR-118).

- A tripped breaker no longer seals its own exits. An unverified-edits trip
  blocked every tool — including the `go build` that is the designed reset
  for that counter, and the `plans/BLOCKED.md` stop-note the trip message
  itself demands. Both recovery paths now stay open: verification commands
  are allowed through and a PASSING verify clears the trip, and the
  stop-note is writable whatever the trip kind. Everything else stays
  blocked, and trips with no self-service recovery (loops, budgets, wall
  clock) still require a human's `orion reset`.

- `orion queue` now works on a project whose key is a JQL reserved word (`OR`, `AND`, `ORDER`, ...). The project key was interpolated into the query unquoted, so Jira rejected it as a syntax error. Every value Orion puts into JQL — project keys, labels, ticket keys — is now quoted by one shared builder.

## v0.7.0

Agents now have names, output has a face, and the changelog stopped being a
merge conflict. First release cut through the develop-to-main promotion flow.

### Added

- `orion init` now scaffolds a secret-scanning workflow into every project it
  adopts: gitleaks at a pinned version, installed through the Go module proxy
  so the download is checksum-verified, scanning the full history rather than
  the diff, printing findings with the secret itself redacted, and failing the
  build on a hit. CI has no implementer to talk to, and a secret already
  pushed to a public repository is not a finding to negotiate.
  A project that already runs gitleaks, trufflehog or detect-secrets keeps its
  own scanner and gets nothing added. Unlike the test workflow, the scan is
  still added when other workflows exist — otherwise every repository that
  already had CI, which is most of them, would stay unscanned.

### Changed

- A ticket now writes `.changelog.d/<KEY>.md` instead of editing
  `CHANGELOG.md`. Every ticket writes a changelog entry and every entry went
  into the same section of the same file, so any two branches in flight
  conflicted there whatever code they touched — three tickets once partitioned
  the code across three packages cleanly and still blocked each other on
  `CHANGELOG.md` alone. A file per ticket cannot collide.

  `orion changelog --version vX.Y.Z` collates the fragments into
  `CHANGELOG.md` — grouped by section, always in keepachangelog order (Added,
  Changed, Deprecated, Removed, Fixed, Security) — and deletes them, so the
  edit and the deletions land in one commit. Collation is a plain merge in Go
  rather than an agent run; with no fragments present the command still
  generates from the commit history as before.

  It refuses rather than guesses: a fragment naming a section outside that
  list stops the release instead of being dropped, and a version already in
  `CHANGELOG.md` is not collated over. Tickets that shipped since the last tag
  with no fragment are listed afterwards, so a missing entry is visible rather
  than simply absent.

  Nothing changes for a reader of `CHANGELOG.md`. `orion init` scaffolds
  `.changelog.d/` for an adopted repository; fragments are committed, not
  ignored.

## v0.6.0

The first release where Orion's memory does something. Also the release that
restores the branch model: agents merge into `develop`, and `main` moves only
when a person promotes it.

### Added

- Orion now proposes lessons by itself. When a branch whose CI went red is
  fixed and merged, `orion collect` files that as an observation — a mistake
  with its own correction attached, both seen by the system rather than
  inferred by an agent. The same observation twice becomes a proposal you are
  asked about; approve it with `orion lessons approve <sig>` and it enters the
  store, reject it and it is never raised again. `orion lessons pending` lists
  what is waiting.

  The cross-project memory has been shipping since v0.1 and had never held
  anything: every writer was a command a human had to remember to type, so
  nothing ever counted to two and nothing was ever lifted. The ranking,
  expiry and promotion downstream were all correct, and all operating on an
  empty list.

  Nothing is written without a yes. The store is append-only and injected into
  every session's `CLAUDE.md`, so a wrong lesson is durable and re-read
  forever — which is why an agent is allowed to observe, never to record.

### Changed

- The generated sandbox settings are regenerated for every job, not only at
  adoption. A sandbox adopted before a policy fix used to keep the old policy
  until someone re-ran `orion init` — and the run that would benefit is the
  one that cannot know it should.

- `orion lessons list` can no longer report an empty store as if it were a
  working one. When nothing has been recorded it now says whether anything has
  ever been *observed*, and how many proposals are waiting. An empty answer and
  a broken subsystem used to be the same output.

- A lesson without a project is refused. Provenance — what happened, where, and
  when — is what makes a durable rule worth trusting later.

- A run that correctly changes nothing is no longer reported as a failure. An
  agent that inspects the tree, finds the issue's work already present and
  declines to invent a diff now ends in a distinct **no-op** outcome: no
  `orion-failed` label, a tracker comment saying what it found and why it did
  nothing, the claim released, and the ticket moved off In Progress. Only an
  explicit `NOTHING TO DO:` line from the agent counts — an agent that stopped
  to ask a question is still blocked, and still labelled as one. Conflating the
  two teaches the reader that `orion-failed` sometimes means "fine, actually",
  which is how a failure label stops carrying information.

### Fixed

- The sandbox no longer denies loopback binds. Any test that stands up a
  local HTTP server — Go's `httptest`, and its equivalent everywhere else —
  panicked on `bind: operation not permitted`, so those packages could not be
  tested inside a sandbox at all. Three of Orion's own failed that way on
  every run, and an agent had to recognise the failure as environmental and
  proceed anyway, which is a habit worth not teaching. The network allowlist
  is unchanged and still governs egress: a socket on `127.0.0.1` reaches
  nothing but the process that opened it.

- Each sandbox now gets a persistent Go build cache at
  `.orion/cache/go-build`, provisioned once and shared by every job. The
  default cache is outside the sandbox's writable set, so runs had been
  redirecting `GOCACHE` to a fresh temp directory and recompiling the
  standard library from cold once per ticket.

- A merged ticket is no longer worked a second time. `orion work` asks the
  forge whether the ticket's branch has already merged **before** it claims
  anything, and ends the run there if it has: labels cleared, ticket moved to
  Done, nothing spent. The claim label is meant to be the lock, but a label
  that was never cleared — or was cleared after the next tick had already read
  the queue — left a window in which a finished ticket was still workable, and
  a re-run costs a full agent at full token price to produce nothing. Asking
  the forge closes the window whichever way it opened. A check that cannot be
  made (no `gh`, no network) is a warning, not a merged branch, so the work
  still runs.

## v0.5.1

A cleanup release. Everything here was found by running Orion on Orion, which
is why so much of it is about the pipeline's own housekeeping rather than
about what agents produce.

The one entry that affects you even if you ignore the rest: `orion watch
--max-jobs N` now waits for the work it started. Before, a run that started a
job and put it into CI reported `started 1 job(s) and finished them` and
exited, abandoning it — the drain learned "something is in flight" only from
a reconciliation pass that ran *before* the job existed. Nothing was lost,
but nothing was finished either, and a later `orion collect` had to pick up
the pieces.

Merged branches are now actually deleted, locally and on the remote. Until
now, none of them were.

`orion collect` removed the worktree and then ran `git branch -d`, whose
safety check is an ancestry test: is the branch tip reachable from the base.
Orion merges by **rebase**, which replays the commits as new objects, so the
originals are never reachable and `-d` refused — every time, for every
ticket, however cleanly it landed. The refusal was caught and downgraded to a
warning on the reasoning that git disagreeing about merged-ness was worth
surfacing. That is right for a merge-commit workflow and wrong for ours,
where the disagreement is the guaranteed consequence of the merge strategy we
chose. So the happy path printed a warning that meant nothing, and every
branch survived. Two tickets left two orphans; a week of `orion watch` would
have left dozens.

The pull request is now the authority on merged-ness, which it always should
have been: `Prune` is only ever reached from the merged path, so the forge
has already answered the question before git is asked. The remote branch is
deleted too, which nothing did before. Finding the remote already gone is
treated as success rather than failure, because it is now the *expected*
case — see the next paragraph.

`orion init` sets `delete_branch_on_merge` on the repository, so GitHub
deletes the head branch at merge time. This is deliberate belt-and-braces:
the situations where `collect` never runs are exactly the messy ones — a
watcher killed mid-run, a merge done by hand in the web UI, a network failure
between merging and pruning — and server-side deletion is the only cleanup
that survives all of them.

**New: `orion protect`.** Applies branch protection using the checks the
repository is *observed* to run, read from real check runs rather than
guessed. The rule that carries the weight is "require branches to be up to
date before merging", which is the server-side form of the staleness gate
added in v0.4.4. Both are worth having and they are not redundant: the gate
needs no admin rights and works anywhere, but it can only warn a human, who
can skim the warning and merge anyway. This refuses the merge.

It is a separate command rather than part of `init` for a reason worth
knowing before you run it: a required status check that never reports blocks
every pull request **forever**, with no timeout and no recourse short of an
admin editing the settings by hand. At adoption time no CI has ever run, so
any check name chosen then would be a guess. Run `orion protect` once CI has
run at least once. `--dry-run` shows what it would require.

Provisioning no longer hardcodes one required approving review. On a solo
repository that rule can never be satisfied — GitHub does not let anyone
approve their own pull request — and you discovered it at the moment you
tried to merge your first change, with the branch already protected. The
count is now derived from who can actually push, and both the number and the
reason for it are printed, because a value that silently differs between two
repositories is worse than either default.

**Note for private repositories on the free plan:** branch protection is a
paid feature there, and GitHub's error says only "upgrade". Making the
repository public also enables it, at no cost, and Orion's message now says
so.

`orion init` now prepares the sandbox's Python environment once, rather than
leaving every ticket to work it out again.

A sandbox is a fresh clone, and a fresh clone has no `.venv`. `scripts/test.sh`
already looks for one in the main worktree — a git worktree does not carry
ignored files — but there was never anything there to find, so every ticket
fell through to bootstrapping a virtualenv inside its own worktree and threw
it away afterwards. On one measured run that discovery was 17 of 31 shell
calls. Worse, when the bootstrap failed the script exited 1, which is
indistinguishable from a failing test: the branch went red for a reason
unrelated to the change, and with `ci.auto_fix` on, Orion paid an agent to
react to an environment problem it could not fix from inside a worktree.

The virtualenv is now built in the sandbox clone at adoption, only when the
project declares dependencies (`pyproject.toml` or `requirements.txt`), and
reinstalled when a manifest is newer than the last install. Re-running
`orion init` is how you repair or refresh it. It is non-fatal throughout: if
it cannot be built, runs still work exactly as they did before.

The other half is that the agent was never told any of this. `scripts/test.sh`
was named only in the CI-fix prompt, which a ticket run never sees, so the
implementer found the entry point by guessing and then found out how to make
it work by trial and error — rediscovered from zero on every ticket. The
implementer prompt now names the command, and names the provisioned
interpreter when there is one. Both lines appear only when the thing they
name exists: a prompt that confidently names a missing script is worse than
silence, because the agent runs it, watches it fail, and goes exploring
anyway.

### Changed

- `orion init` creates and refreshes `.venv` in the sandbox clone for
  projects that declare Python dependencies, so per-ticket worktrees find one
  through the fallback `scripts/test.sh` already has
- the implementer prompt names `./scripts/test.sh` and the sandbox's
  interpreter when they exist, instead of leaving each ticket to discover
  both

### Fixed

- `orion watch --max-jobs N` waits for the jobs it started instead of
  abandoning them. `oneTick` learned whether anything was in flight from a
  reconciliation pass that runs *before* a job is started, so the one job it
  had just launched was invisible to it and the run exited announcing
  `started 1 job(s) and finished them`. The job was fine; nobody was watching
  it.
- `orion sandbox prune --force` does something. The flag was parsed and then
  ignored — the call site passed a hardcoded `false` — so the escape hatch
  for a worktree prune refused to remove silently did nothing, twice as
  confusingly because the command reported success.
- CI no longer runs twice per push. Without a concurrency group a force-push
  left the superseded run burning through all three OS legs while its
  replacement queued behind it, and a rebase is a force-push, so every branch
  that went stale under `ci.require_up_to_date` paid for itself twice. Orion
  scaffolds this into the workflows it writes for adopted projects and did
  not have it itself: the repository that generates the good default was the
  one missing it.
- `collect`, `work` and `watch` now refuse a key that cannot exist, before
  any Jira or GitHub call. `orion collect or or-39` used to take `or` as a
  ticket key and report that no pull request was found for branch
  `orion/or` — an accurate sentence about a key no branch or PR could ever
  carry — then process `or-39` and exit 0, so a typo in a cron line warned
  once per tick forever. A bad token now fails the whole invocation with a
  usage error naming it and the shape expected; a mix of valid and invalid
  keys does no partial work. `watch`'s positionals are project keys, so it
  rejects the inverse mistake (`orion watch or-39`) and its usage line now
  reads `[PROJECT...]`.
- `orion init` no longer leaves its `.claude/settings.json` backups for git to
  pick up. It writes a timestamped `.bak` before rewriting the file, and init
  is not a once-ever command — it re-runs on adoption, on `--force`, and after
  any change to the hook wiring — so those files accumulated in the working
  tree, were swept up by a `git add -A`, and landed in history permanently, in
  whatever repository adopted Orion. A run that keeps a backup now ensures
  `.gitignore` covers `.claude/*.orion-*.bak`, appending only when the line is
  absent and creating the file when there is none, and says so in its output
  rather than editing a file you did not name in silence. If `.gitignore`
  cannot be written it warns and finishes: working hooks are worth more than a
  tidy tree.

## v0.5.0

Orion was flat: no concept of parent, sub-task, or children in tracker, work,
or watch — the queue was just a label search, and the unit of work was
whatever carried the label. A story broken into sub-tasks had two bad
options: label the story and the agent never learns the sub-tasks exist, or
label each sub-task and get separate branches/PRs/CI runs that can collide —
FCIA-8 and FCIA-10 both created `src/fcia/cli.py` from scratch as sibling
tasks under the same story, and only avoided conflict by luck.

### Added

- A claimed story now works its sub-tasks itself, in one branch, one commit
  series, one pull request. Orion reads the children with one JQL query
  (`parent = KEY ORDER BY Rank ASC`, covering both classic sub-tasks and a
  team-managed project's child-issue shape), drops any already `Done`, and
  hands the agent the rest as a numbered checklist to report against by key.
  A sub-task whose parent is also queued is silently dropped from `watch` —
  the parent already covers it — rather than refused as a mistake. On merge,
  the story's sub-tasks are transitioned to Done and commented with the PR
  link, best-effort so a tracker hiccup never turns a successful merge into
  a reported failure. A tracker that can't answer the parent query (unusual
  project shape, missing permission) isn't a failure either — Orion falls
  back to working the ticket as itself and says so.
- The turn/time budget for a claimed story now scales with how many
  sub-tasks it carries, instead of applying one fixed allowance regardless
  of size: 120 turns / 30 minutes with no sub-tasks, up to 600 turns / 180
  minutes at 40. A story above 20 sub-tasks gets a warning, not a refusal —
  it used to hard-refuse above 15, which broke on the twenty-five-task
  stories people actually write. An explicit `--max-turns` or
  `--max-minutes` always wins over the computed budget and is never raised
  automatically, since naming a bound is a deliberate choice to cap spend. A
  story queried for children is still capped at one page (100) — more than
  that is a data problem, not a plan.

## v0.4.4

FCIA-8 and FCIA-10 exposed two more variants of the same failure family:
evidence going stale without anyone noticing. FCIA-8 and FCIA-10 were cut
from develop in parallel, both wrote `src/fcia/cli.py` from scratch, and
only conflicted by luck — had the two subcommands landed in different files,
both merges would have gone in clean, both CI runs green, and develop would
have quietly lost one of them. The same run also caught a budget checkpoint
that offered Slack as a way to answer and then ignored it, and a watcher
that told operators to run the command they were already running.

### Added

- Orion now refuses to merge a branch whose base has moved since its last
  green CI run. A passing check is evidence about one commit; once the base
  moves, that evidence no longer describes what merging would produce. The
  check is one local `git merge-base --is-ancestor origin/<base> origin/<branch>`
  — no API call, no admin rights — gated by new config `ci.require_up_to_date`
  (default true, since merging on a verdict that no longer describes the
  code is a correctness failure, not a preference). A stale branch is
  refused and reported the way a conflict is: said once per HEAD, with
  rebase commands for the branch's actual directory, ticket kept in
  `ci-wait` so a pushed rebase resumes without re-labelling. Deliberately
  not worded as a conflict — a conflict is git refusing; this is Orion
  refusing something git would happily do. An unanswerable check (missing
  directory, failed fetch, absent branch) counts as unknown, not stale, and
  does not block the merge. GitHub's own "require up to date" branch
  protection was considered and rejected: unavailable on private repos on
  the free plan, and even off, `gh` reports a stale branch as
  `mergeStateStatus CLEAN` — so detection is missing there too.

### Fixed

- The budget checkpoint offered two ways to answer — a Slack reaction or a
  terminal prompt — but only listened to one: it posted to Slack, then
  blocked on a terminal read with nothing watching Slack while stdin held
  the process. Someone could answer exactly as invited and the run would
  hang forever. `awaitConsent` now races both routes, polling Slack every
  3s while a terminal read is outstanding, and takes whichever answers
  first. No terminal means Slack-only; no Slack means terminal-only;
  neither means not-consent, immediately, since silence isn't consent to
  spend. Bounded at 30 minutes so an overnight watcher doesn't hold a
  checkpoint open indefinitely — the request stays visible in Slack and the
  next tick asks again. Known limitation: a blocked terminal read can't be
  cancelled, so it outlives a Slack answer and consumes the next line typed
  at that terminal.
- `orion watch` told people to re-run the command they were already
  running — "run `orion collect FCIA-8` again to act on it (orion watch
  does this on its next tick)" printed from inside the watcher itself. New
  `collect.Options.Unattended`, set by watch, swaps that for "react there
  and the next tick merges it."

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
