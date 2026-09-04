# Installing and using Orion

Orion takes an idea to a reviewed pull request through a committed artifact
chain, in an isolated sandboxed workspace, under guardrails that are enforced
rather than suggested.

---

## 1. Install

### macOS and Linux

```bash
brew install navjyotnishant/tap/orion
```

### Windows

```powershell
scoop bucket add navjyotnishant https://github.com/navjyotnishant/scoop-bucket
scoop install orion
```

> Windows caveat, stated plainly: the guardrail hooks depend on Claude Code's
> hook protocol, and the OS sandbox Orion generates settings for is macOS and
> Linux only. `orion doctor` will tell you which capabilities are degraded.

### From source

```bash
git clone https://github.com/NjAIAgents/orion && cd orion
make test          # build, vet, gofmt and the full suite
make install       # to ~/.local/bin
```

### Staying current

`status`, `doctor`, `watch`, `collect`, `work` and `init` print one yellow
line when a newer release exists, naming the upgrade command for how this
machine installed Orion:

```
update    orion v0.5.1 is available (you have v0.5.0)
          brew upgrade navjyotnishant/tap/orion
```

The check is cached for 24 hours in `~/.orion/state/update.json` and
refreshed by a background process, so no command waits on it; with no network
nothing is printed and nothing fails. It never runs in hook mode, off a
terminal, or when `CI` is set. To silence it permanently — you are pinned to
a version on purpose — set `ORION_NO_UPDATE_CHECK=1` in the environment or
add the same line to `~/.orion/config.env`.

### Install nj-agents, which is not optional

Orion delegates review, secret scanning, test and build verification, PR
authoring and work decomposition to
[nj-agents](https://github.com/navjyotnishant/nj-agents). Those stages have no
fallback, so `orion doctor` grades a missing toolkit as **FAIL**, not a
warning.

```bash
orion doctor --fix      # clones it if absent
```

Or install it yourself, which is preferable if you intend to work on it:

```bash
git clone https://github.com/navjyotnishant/nj-agents
cd nj-agents && ./install.sh
```

Orion reads a global install and never writes to it. Only a clone Orion
fetched itself is one Orion will update.

### Configure credentials (optional)

```bash
orion config        # Jira, Slack, webhooks; secrets are not echoed
orion config show   # what is set, where from, masked
```

Stored in `~/.orion/config.env` at mode 0600 and read by the binary itself,
so it works under cron and launchd where a shell profile would not. An
exported environment variable still overrides it.

### Confirm the machine is ready

```bash
orion doctor
```

Every check is graded. **FAIL** blocks; **WARN** means a reduced capability,
not a dead stop. Expect `jira` to warn until you configure it — that is
correct, not a problem to fix, unless you want tracker provisioning.

| Check | What it actually proves |
|---|---|
| `claude CLI` | the binary Orion supervises exists and runs |
| `git` | present, **and** `user.name`/`user.email` are set, or commits are unattributable |
| `gh repo scope` | the token can create a repository, not merely that you are logged in |
| `nj-agents` | the toolkit is present **and intact**, resolved through the skill symlink to its clone root |
| `os sandbox` | Seatbelt or bubblewrap is available |
| `jira` | reachable, authenticated, and whether the account may create projects |

---

## 2. Optional: tracker and notifications

Only needed if you want Orion to provision a Jira project and decompose plans
into issues.

```bash
export ORION_JIRA_URL=https://yourorg.atlassian.net
export ORION_JIRA_EMAIL=you@example.com
export ORION_JIRA_TOKEN=...   # id.atlassian.com/manage-profile/security/api-tokens
```

Orion creates a project per idea by default. That needs the "Create
team-managed projects" global permission and accumulates projects a non-admin
cannot delete. To use one existing project instead, set `tracker.project_key`
in `orion.json` and Orion will create its issues there.

---

## 3. Using Orion on a NEW idea

This is the path Orion is built for: nothing exists yet.

```bash
orion new "customers should see claim status in the portal"
```

This is the one interactive step in the whole system. It asks you five things
the flat text does not say — who it is for, what is wrong for them today, how
you will know it worked, what is explicitly out of scope, what constrains it —
then asks you to finalise the project name. It creates the tracker project
carrying that elaborated description, behind a confirmation that first says a
Jira project cannot be deleted without admin rights.

It creates **no workspace** and writes nothing to disk. The project is the
handoff artifact, and `orion plan` reads its description back as the statement
of the work.

```bash
orion plan <KEY>                   # workspace, roster and cost shape, then stop
orion run <id> --stage spec        # -> specs/<slug>.spec.md
orion run <id> --stage plan        # -> plans/<slug>.plan.md   (review this)
orion provision <id>               # remote repo, branches
orion run <id> --stage scaffold    # /scaffold-project, OSPS baseline layout
orion run <id> --stage decompose   # /pm-plan tree, approved before creation
orion run <id> --stage build       # implementation + tests, on a branch
orion run <id> --stage verify      # runs it, reports, fixes nothing
orion run <id> --stage review      # severity-ranked findings
orion run <id> --stage pr          # /pr-describe, PR into develop
orion status <id>
```

### Why the questions are asked now

Every stage after `new` runs through `claude -p` and **cannot ask you
anything**. Without this conversation the agent's only options are to invent
answers or write questions nobody reads, and one ambiguous sentence then
propagates into spec, plan, scaffold and a tracker tree. Each stage carries a
token floor of roughly 30k, so a wrong premise costs nine floors plus the
rework, against a conversation costing nothing but your attention.

A question you leave blank is recorded in the description as unstated rather
than dropped, so a later stage can tell "nobody decided this" from "this does
not apply".

Anything left unresolved under **Open questions** in an intent file blocks the
`spec`, `plan`, `scaffold` and `decompose` stages until it is answered:

```bash
orion answer <id>       # lists what is blocking, and where to answer it
```

Answers go in the intent file itself, not a prompt, so every later stage reads
them. Mark one resolved with `[x]`, `~~strikethrough~~`, or an inline
`Answer: ...`.

**Stop and read `plans/<slug>.plan.md` before letting build run.** It is the
cheapest moment to change direction: editing a document rather than a diff.
The bar is that an engineer who never saw the conversation could implement
from it alone.

Inside Claude Code, `/orion:start`, `/orion:next`, `/orion:status` and
`/orion:learn` drive the same chain conversationally.

---

## 4. Using Orion on an EXISTING repo

Two ways, and they answer different questions.

### 4a. Work on a copy, in a sandbox

Best when you want Orion's isolation and do not want it near your working
tree.

**Currently unavailable.** `orion new --from <repo>` cloned a repository into a
fresh workspace, and `orion new` no longer provisions a workspace at all
([0013](decisions/0013-new-creates-the-tracker-project-not-a-workspace.md)).
`orion plan`, which now owns workspace provisioning, has not yet grown the
equivalent flag, so the flag is refused rather than silently ignored. Use 4b
below meanwhile.

### 4b. Adopt Orion inside the repo itself

Best when you want the guardrails to apply to your ordinary Claude Code
sessions in that repo, not just to supervised runs.

```bash
cd /path/to/your-repo
orion init
```

That writes `orion.json`, creates the artifact directories, and merges the
hooks into `.claude/settings.json`. It is idempotent: running it twice
changes nothing the second time.

**It never overwrites what is already there.** That file usually holds your
own hooks, permissions and MCP servers, and a copy-paste recipe invites you
to lose all of it. `orion init` backs the file up first, adds only its own
entries, recognises them on a re-run rather than duplicating, and **refuses
outright** if the file is unparseable, because a file it cannot read may
still be precious.

**Restart any running Claude Code session afterwards.** Hooks are read at
session start, so an already-open session stays unguarded.

The hooks it merges in are:

```json
{
  "hooks": {
    "SessionStart": [{ "hooks": [{ "type": "command", "command": "orion hook session-start" }] }],
    "PreToolUse": [
      { "matcher": "Bash", "hooks": [{ "type": "command", "command": "orion hook gate" }] },
      { "matcher": "Edit|Write|MultiEdit|NotebookEdit", "hooks": [{ "type": "command", "command": "orion hook shield" }] },
      { "matcher": "*", "hooks": [{ "type": "command", "command": "orion hook breaker" }] }
    ],
    "PostToolUse": [{ "matcher": "*", "hooks": [{ "type": "command", "command": "orion hook breaker" }] }]
  }
}
```

The breaker is wired to **both** PreToolUse and PostToolUse deliberately.
PostToolUse counts what happened; PreToolUse refuses the next call. Wiring
only one gives you a breaker that reports but never stops.

`orion init` leaves `require_plan_before_edit` **off**, unlike a fresh
workspace. In a repo where small changes are made without writing a plan
first, leaving it on means hitting a wall on the first edit, and people
disable Orion entirely rather than change one setting. Turn it on with
`orion init --plan-gate`, or flip it in `orion.json` when the habit is there.

### 4c. Which actor works a ticket

Orion picks the actor from the ticket's **issue type, components and labels**
— a deterministic lookup, never a model call, and never inferred from the
summary. `orion routes` prints the whole table: each routable actor, the
exact keywords it accepts, and the actors that are deliberately unroutable
because something else already invokes them.

```bash
orion routes
```

Matching is by **equality**, case-insensitive. A component named
`docsite-infra` is not a documentation ticket, so containment would route it
to the wrong actor.

**Set the marker when the ticket is created.** Routing reads what planning
wrote; a ticket carrying no marker goes to the backend developer, which is
right for backend work and silently wrong for everything else. `orion queue`
prints the routing split of the work in front of it, so an all-default queue
is visible before the run rather than after the bill.

---

## 5. Tuning the guardrails

Everything lives in `orion.json` at the repo root, reviewable like code.

| Setting | Default | What it stops |
|---|---|---|
| `limits.max_tool_calls` | 400 | a session burning budget indefinitely |
| `limits.max_repeat_identical` | 4 | the same call looping with no new input |
| `limits.max_consecutive_failures` | 3 | retrying a broken thing forever |
| `limits.max_session_minutes` | 90 | wall-clock runaway |
| `limits.max_files_touched` | 60 | blast radius |
| `limits.max_concurrent_tickets` | 4 | how many tickets `orion watch` works at once |
| `qa.max_rounds` | 3 | QA and the developer arguing until the budget runs out |
| `ci.max_fix_attempts` | 3 | an agent re-fixing a red build all night |
| `gates.require_plan_before_edit` | true | implementation before a written plan |
| `gates.protect_tests_during_fix` | true | an agent weakening the test that defines "fixed" |
| `gates.production_requires_authorization` | true | an unauthorised production deploy |
| `vcs.protected_branches` | `[main, develop]` | direct pushes to either long-lived branch |
| `vcs.work_branch` ≠ `vcs.default_branch` | enforced | Orion merging agent work straight into the release branch |

A limit of `0` restores the default rather than meaning unlimited. "No limit"
is never a safe reading of an absent value in a circuit breaker.

**Set them with the command, not by hand.** `orion config limits` lists every
bound with its effective value and where that value came from — `orion.json` or
the shipped default — and `orion config limits KEY VALUE` writes one. It edits
the file the runner actually reads, which is not always the one you are standing
in, and it leaves the `_comment_*` keys where they are. The two fix-round
ceilings take their block as a prefix, because that is where they live in the
JSON:

```
orion config limits                        # every bound, with provenance
orion config limits qa.max_rounds 3
orion config limits ci.max_fix_attempts 3
```

### The two fix-round ceilings

`qa.max_rounds` and `ci.max_fix_attempts` are the same bet made twice: an agent
is handed what went wrong and asked to try again, and the number bounds how many
times that is worth paying for. Both default to **3**, raised from 2.

That number is a decision with a cost, not a constant. A second fix round has
demonstrably been productive here, and stopping at two escalates to a person
work that one more exchange would have finished — but three raises the **worst
case by half** on every ticket that fails to converge, and that spend lands on
the implementer, the actor that dominates per-ticket cost. Set them to `2` to
buy the old ceiling back.

For the CI loop the ceiling is the **outer** bound, not the usual stop. An
identical repeated failure ends that loop immediately, on the reasoning that an
agent handed back a byte-identical error has learned nothing — so a third
attempt is only ever reached by a run producing a *different* failure each
round, which is the only kind of run a further attempt can help. Raising the
ceiling does not reach past that brake.

Neither is clamped from above. A value beyond 5 is confirmed rather than
refused: too high is the expensive direction, so `orion config limits` states
what it costs and asks, because a repository whose failures genuinely take four
exchanges to converge is not something Orion can tell apart from a typo — and
the person typing the number can.

`limits.max_concurrent_tickets` is the one that bounds how many agents exist
rather than what one agent may do, so it is also clamped from above: **5 is a
hard ceiling**, and a larger number is reduced rather than honoured. The
default was 2 first, because everything concurrency breaks — git against the
one shared clone, a budget checkpoint crossed by runs already in flight,
tickets picked that all edit the same files — is invisible at 1 and obvious at
2. The rule was "prove it at 2, then raise it"; 2 has now been proven across a
full release, and 4 is that raise. It stops short of the ceiling on purpose, so
that reaching 5 stays an explicit choice rather than the default. Set it to `1`
for the previous strictly-sequential watcher.
Note that approvals do not parallelise: N tickets finishing means N approvals
waiting on one person.

### The branch model is enforced, not merely documented

Orion's responsibility ends when work merges into the **integration branch**
(`vcs.work_branch`, `develop` by default). Promotion from there to the
**release branch** (`vcs.default_branch`, `main`) is a human decision — it is
where somebody decides that a set of merged changes constitutes a release. An
agent must not make that call.

So setting the two equal is **refused**: at config load, at `orion init`, and
by `orion doctor`, each naming both values and the remedy. `orion init`
creates the integration branch when it does not exist yet, so a repository
with only a release branch is offered one rather than having its release
branch adopted as the work branch.

A repository that genuinely has one branch and no release process says so
explicitly:

```json
"vcs": { "allow_release_branch_merges": true }
```

That is a named opt-in, not something reachable by editing one string, and
every run that uses it prints what is being given up: there is no human
promotion step left. **Orion's own repository does not set it** — `develop`
is the integration branch, `main` is promoted by a human PR when a release is
cut. (For a while it did the other thing: `work_branch` was pointed at `main`
because `develop` had gone unused, and Orion merged agent output into the
release branch for several releases, reporting it accurately the whole time.
That is the failure this rule exists to prevent.)

After a breaker trips, a human decides whether to continue:

```bash
orion reset --session <id>
```

### Declaring which toolkit you delegate to

The optional `toolkit` block in `orion.json` names the skill repository Orion
delegates to, and what each stage invokes inside it. **Leaving it out changes
nothing**: `toolkit.repo` falls back to the nj-agents URL Orion has always
used, `toolkit.dir` and `toolkit.ref` fall back to the older
`delegation.nj_agents_dir` / `delegation.nj_agents_ref` spellings, and a stage
with no command declared runs Orion's own built-in prompt.

```jsonc
{
  "toolkit": {
    "repo": "https://github.com/navjyotnishant/nj-agents.git",  // the default
    "ref": "v1.4.0",                 // pin a tag; empty clones the default branch
    "dir": "/home/me/nj-agents",     // an existing clone; overrides the vendor path
    "stages": {
      "review": "/pre-push-review",
      "pr": "/pr-describe"
    }
  }
}
```

A team with its own skill repository points Orion at it without a Go change:

```jsonc
{
  "toolkit": {
    "repo": "https://github.com/github/spec-kit.git",
    "stages": {
      "intent": "/specify",
      "spec": "/plan",
      "plan": "/tasks",
      "decompose": "/breakdown",
      "review": "/analyze"
    }
  }
}
```

Orion clones a toolkit it manages into `<ORION_HOME>/vendor/<repo-name>` —
`vendor/spec-kit` above, `vendor/nj-agents` for the default — so two toolkits
never land on the same directory. `toolkit.dir` overrides that entirely.

**Stage names.** Only the stages Orion runs: `intent`, `spec` (or `design`),
`plan`, `ticket`, `scaffold`, `decompose`, `build` (or `implement`), `verify`
(or `test`), `review`, `pr` (or `ship`). Either spelling of a pair means the
same stage. Naming a stage twice with two different commands is refused, with
both keys quoted, rather than one being picked silently; so is a stage name
Orion does not run, because a typo that is ignored is a stage nobody notices
is still unconfigured.

**No ordering.** `stages` is a map — what a stage runs — and never a list, an
`order` key or a `sequence` key. Those express what runs *after* what, and
sequencing across stages is Orion's, not a toolkit's
([decisions/0001](decisions/0001-precedence-rule-orion-owns-orchestration.md)).
A block that expresses order is rejected with an error citing that decision,
so the rule holds by shape rather than by prose.

### Creating the tracker tree from a spec-kit task list

If your `plan` stage runs spec-kit's `/speckit.tasks`, the artifact it leaves
behind — `specs/<nnn-feature>/tasks.md` — is a phased task list with `[P]`
parallel markers, `[USn]` story groups and exact file paths. `orion decompose`
turns that into the tracker tree itself, without a skill in the middle:

```bash
orion decompose CAT                       # finds specs/*/tasks.md
orion decompose CAT specs/003-cat/tasks.md
```

One Epic, one Story per `[USn]` group, and each task as a child of its story —
a task in no story group (Setup, Foundational, Polish) hangs off the Epic
directly, since that is where it belongs and no story was described for it. The
`[P]` marker, the phase, the dependency section and the file paths all survive
into the descriptions, and the routing marker `orion routes` publishes is set
on each item **by Orion** rather than requested in a prompt.

The whole tree is printed first, with `+` for what would be created and `=` for
what a previous run already made, and **one answer covers all of it**. A run
with nobody present to answer creates nothing and says so. A re-run searches by
the tree's identity label (`orion-spec-<feature>`), links what is there and
creates only the rest — so a run that failed halfway is resumed by running the
same command again, and it reports the item it stopped at.

**This is opt-in and Jira-only for now.** The `decompose` STAGE still runs
whatever your toolkit block names (`/pm-plan` by default), on any tracker, and
that path is unchanged — a project with no spec-kit output decomposes exactly as
it did before. The tracker-neutral seam that would let this reach Linear, Notion
and GitHub Issues is tracked as OR-303.

---

## 6. Weekly budget checkpoints

Orion accounts for what it spends over a rolling seven days and stops for
confirmation at 50%, 75%, 90% and 95%.

```bash
orion budget status        # spend, tokens, next checkpoint, recent runs
orion budget ack           # confirm the current checkpoint and continue
```

Set the limit in `orion.json`:

```json
"budget": { "weekly_usd": 40, "weekly_tokens": 0, "pause_at_percent": [50, 75, 90, 95] }
```

**This is your budget, not your Anthropic plan's weekly limit.** Orion cannot
read the latter: `claude` has no usage command, and a run's JSON result
reports what that run consumed, never what remains on the plan. Any
percentage shown against the provider's real quota would be invented, so
Orion does not show one.

Zero means unlimited, which is the opposite of the circuit-breaker
convention where zero restores a default. A budget nobody set should not be
invented; a missing breaker limit is never safe.

Acknowledging one checkpoint does not consent to the rest. The next threshold
stops again.

## 7. Context and token burn

Two numbers worth knowing before designing a long chain.

**Every invocation carries a floor of roughly 30k input tokens.** Measured,
not estimated: `claude -p "say ok"` reports ~34k input tokens and about $0.19,
almost all of it system prompt and cached context. That is paid per
invocation, so nine small stages cost nine floors before any work happens.
Prefer fewer, larger stages over many trivial ones.

**Orion cannot trigger compaction, and mostly does not need to.** The CLI
exposes no compaction flag or setting. What Orion does instead is
architectural: every stage is a separate `claude -p` invocation that reads
committed artifacts rather than inheriting a transcript, so context resets at
each stage boundary by construction. The artifact chain *is* the compaction
strategy.

Within a single long stage, context can still climb. Orion reports it: when a
run's input passes 70% of the model's context window, it says so and suggests
splitting the stage, because that is the only lever that exists.

## 8. Monitoring, failures and Slack

```bash
orion report                  # digest: failures, workspaces, budget, usage
orion report --since 24h      # narrower window (7d, 24h, 90m)
orion report --notify         # also send it to your webhook
orion logs <id> --tail 60     # the failing tail of a workspace's runs
orion logs <id> --runs 3      # the last three runs
```

`orion report` **exits 1 when something needs a human** and 0 otherwise, so
cron stays quiet unless there is a problem:

```cron
0 9 * * *  /opt/homebrew/bin/orion report --notify
```

The digest leads with what is actionable — quota-parked workspaces, an
unacknowledged budget checkpoint, failed runs with their log paths — because
a digest that buries the actionable part under a status table becomes
wallpaper.

> **Setting the tokens up: [docs/SETUP-CREDENTIALS.md](SETUP-CREDENTIALS.md)**
> — a paste-ready Slack app manifest, and the Jira token steps.

### Slack: a channel per project

Orion can create a Slack channel for every workspace and report into it. This
needs a **Slack app bot token**, not an incoming webhook — a webhook is bound
to one channel at creation and cannot create any, which is the single thing
most likely to waste an afternoon here.

1. Create an app at api.slack.com/apps, then under **OAuth & Permissions**
   add these **bot** scopes:
   - `channels:manage` for public channels, or `groups:write` for private
   - `chat:write`
   - `channels:read` or `groups:read` so an existing channel can be found again
2. Install the app to the workspace and copy the bot token (`xoxb-...`).
   **Adding a scope later requires reinstalling** — an existing token does not
   gain scopes retroactively, and the resulting `missing_scope` error is
   otherwise baffling.
3. Configure:

```bash
export ORION_SLACK_TOKEN='xoxb-...'
```

```json
"slack": { "enabled": true, "create_channel_per_project": true,
           "channel_prefix": "orion-", "private": true }
```

`orion doctor` then reports the workspace the token belongs to by name. That
check matters more than it looks: a token for the *wrong* workspace
authenticates perfectly and posts into a room nobody reads, which is
indistinguishable from working.

`orion plan` creates `#orion-<slug>`, sets the channel topic to the idea, and
posts an opening message naming the workspace id and the commands to drive
it. Every stage failure, breaker trip, quota wait and budget checkpoint for
that project then lands in that channel rather than one shared firehose.

Two things worth knowing before turning it on. Channels **accumulate** exactly
as the Jira projects do, and a bot cannot delete them — archive a finished
project's channel instead. And `private: true` is the default because a public
channel cannot be made private afterwards, while a slug can easily reveal an
unreleased project.

If Slack is unreachable when a workspace is created, Orion says so and carries
on. A workspace without a channel is usable; refusing to provision because
Slack was down would not be.

### Webhook-only, without an app

Outbound works today with no extra software. Create a Slack incoming webhook
and export it:

```bash
export ORION_NOTIFY_WEBHOOK='https://hooks.slack.com/services/...'
```

Orion posts JSON with a Slack-compatible `text` field, so a raw incoming
webhook works with no adapter. You will get: any stage failure, a tripped
breaker, a quota wall with its resume time, and whatever `orion report
--notify` sends.

`ORION_NOTIFY_COMMAND` runs an arbitrary command instead, if you would rather
route through something else. Desktop notifications fire on macOS and Windows
regardless.

### Talking back to Orion

Orion is a CLI invoked by you or by cron. It has no listener, so Slack is
one-way by design. To make it conversational, drive it from an interactive
Claude session with the Slack MCP connected: you ask Claude, Claude runs the
`orion` commands and reports back into the channel. That puts the
conversation where Claude already is and adds no service to run or secure.

A real Slack app with socket mode would let you command Orion from Slack
directly. It is also a long-running process with tokens to protect, which is
a lot of attack surface for a single-user supervisor. It is deliberately not
built.

## 9. Keeping nj-agents current

It ships independently of Orion, so its improvements arrive only when
something fetches them.

```bash
orion njagents status     # where it is, which commit, how stale
orion njagents update     # fast-forward Orion's own clone
```

`update` refuses to touch a global install: that clone is yours and may hold
work in progress. It prints the `git -C ... pull` to run yourself.

---

## 10. Cross-project memory

A correction learned in one project should not be relearned in the next.

```bash
orion lessons add "Money is BigDecimal, never double"
orion lessons list           # what is recorded -- and whether anything is observing
orion lessons pending        # proposals waiting for your yes or no
orion lessons approve <sig>  # record one   |   orion lessons reject <sig>
```

Most entries should arrive without you typing anything. When a branch whose CI
went red is fixed and merged, `orion collect` files an observation: a mistake
with its own correction attached, both seen by the system rather than inferred.
The same observation twice becomes a proposal, and a proposal becomes a lesson
only when you approve it — the block is read at the start of every session, so
a wrong lesson is durable and misdirects every future run.

`orion lessons list` deliberately distinguishes "nothing has been recorded" from
"nothing is writing here". Those look identical from the outside, and for the
whole life of this store it was the second.

Scope is earned rather than assumed. A lesson starts local to its project and
reaches others only after it actually recurs somewhere else: recurring five
times in one repo proves it matters there and proves nothing about anywhere
else. The injected block is capped at 25 entries, because the agent reads
`CLAUDE.md` in full every session and an unbounded list degrades every session
invisibly.

---

## 11. Releasing Orion itself

```bash
make release TAG=v0.1.0 DRY=1   # rehearse, publishes nothing
make release TAG=v0.1.0
```

Uses your existing `gh` login, so there are no tokens to create or rotate. It
refuses a dirty tree, refuses any branch but `main`, refuses a `main` that
differs from origin, and runs the full gate before publishing anything.

---

## Known limits

- **Branch protection is not enforced server-side on a private free-plan
  repository.** GitHub returns 403. Orion's gate hook still refuses pushes to
  `main` and `develop`, but that constrains the agent, not a human with a
  terminal.
- **The OS sandbox is not a VM.** It stops credential reads and network
  egress; it does not defend against a determined exploit. For untrusted code
  use `--container`.
- **Token budget is proxied by tool-call count**, not real tokens. Hooks are
  not given token counts.
- **Unparseable hook input exits 0 and allows the call**, so a malformed
  payload from a future harness version cannot brick a session. The cost is
  that a harness change could silently disable enforcement.
