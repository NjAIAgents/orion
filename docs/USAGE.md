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

You get an isolated workspace under `$ORION_HOME/projects/<slug>-<id>/` with
its own git repo, generated sandbox settings, and `main` + `develop` already
created. Nothing here touches your other work.

```bash
orion run <id> --stage intent      # /capture-intent -> docs/intent/<slug>.md
orion run <id> --stage spec        # -> specs/<slug>.spec.md
orion run <id> --stage plan        # -> plans/<slug>.plan.md   (review this)
orion provision <id>               # remote repo, branches, Jira project
orion run <id> --stage scaffold    # /scaffold-project, OSPS baseline layout
orion run <id> --stage decompose   # /pm-plan tree, approved before creation
orion run <id> --stage build       # implementation + tests, on a branch
orion run <id> --stage verify      # runs it, reports, fixes nothing
orion run <id> --stage review      # severity-ranked findings
orion run <id> --stage pr          # /pr-describe, PR into develop
orion status <id>
```

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

```bash
orion new "add rate limiting to the status endpoint" --from https://github.com/you/your-repo
```

The repo is cloned shallow into a fresh workspace. Everything above applies.
The change leaves as a pull request against your real remote once you push the
branch; nothing is written to your local checkout at any point.

### 4b. Adopt Orion inside the repo itself

Best when you want the guardrails to apply to your ordinary Claude Code
sessions in that repo, not just to supervised runs.

```bash
cd /path/to/your-repo
cp "$(brew --prefix orion)/share/orion/orion.json" ./orion.json   # or from the source tree
mkdir -p docs/intent specs plans evals
orion doctor            # confirms the config parses and limits are in force
```

Then wire the hooks into that repo's `.claude/settings.json`:

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

**Adopting in an existing repo has a consequence worth knowing before you do
it.** `require_plan_before_edit` is on by default, so Orion will refuse edits
until a plan exists in `plans/`. In a repo where you routinely make small
changes without writing a plan, that will feel obstructive. Either write plans,
or set it to `false` in `orion.json` and keep the other guardrails.

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
| `gates.require_plan_before_edit` | true | implementation before a written plan |
| `gates.protect_tests_during_fix` | true | an agent weakening the test that defines "fixed" |
| `gates.production_requires_authorization` | true | an unauthorised production deploy |
| `vcs.protected_branches` | `[main, develop]` | direct pushes to either long-lived branch |

A limit of `0` restores the default rather than meaning unlimited. "No limit"
is never a safe reading of an absent value in a circuit breaker.

After a breaker trips, a human decides whether to continue:

```bash
orion reset --session <id>
```

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

## 8. Keeping nj-agents current

It ships independently of Orion, so its improvements arrive only when
something fetches them.

```bash
orion njagents status     # where it is, which commit, how stale
orion njagents update     # fast-forward Orion's own clone
```

`update` refuses to touch a global install: that clone is yours and may hold
work in progress. It prints the `git -C ... pull` to run yourself.

---

## 9. Cross-project memory

A correction learned in one project should not be relearned in the next.

```bash
orion lessons add "Money is BigDecimal, never double"
orion lessons list
```

Scope is earned rather than assumed. A lesson starts local to its project and
reaches others only after it actually recurs somewhere else: recurring five
times in one repo proves it matters there and proves nothing about anywhere
else. The injected block is capped at 25 entries, because the agent reads
`CLAUDE.md` in full every session and an unbounded list degrades every session
invisibly.

---

## 10. Releasing Orion itself

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
