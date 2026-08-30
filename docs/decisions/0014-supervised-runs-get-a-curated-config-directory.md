# 0014: A supervised run gets a curated config directory and no MCP servers

- Status: Accepted
- Date: 2026-08-30
- Ticket: OR-213
- Related: [0001](0001-precedence-rule-orion-owns-orchestration.md) (Orion owns
  the tracker contract), [0005](0005-agent-roster-is-global.md) (where an
  actor-level setting lives)

## Context

Every supervised run inherited the OPERATOR's entire Claude Code
configuration: their plugins, their MCP servers, their subagents, their slash
commands and their system-prompt customisations. Orion chose none of it, and
it decides what the agent can do, how it writes, and what a run costs.

Measured from the init line of a real run on 2026-08-30:

```
total tools:      179
mcp/plugin tools: 148
```

The transcript also carried a third-party plugin's system prompt verbatim,
including its instruction to compress output — so that plugin was shaping how
the implementer wrote commits, PR bodies and QA reports.

The mechanism: `--settings <workspace>/settings.json` set the workspace's
settings but isolated nothing else, and `childEnv` copied the whole parent
environment minus a credential denylist, never setting `CLAUDE_CONFIG_DIR`.
So `~/.claude` loaded in full.

Three consequences, in order of severity:

1. **Blast radius.** 148 of those tools were MCP tools against the operator's
   real, authenticated accounts, including writes: `createJiraIssue`,
   `editJiraIssue`, `createConfluencePage`. Orion's guardrails govern the
   filesystem, git and the sandbox. They do not govern these. An implementer
   working one ticket could edit any ticket in the tracker, and nothing in the
   breaker, the sandbox policy or the approval path would see it.
2. **Reproducibility.** A run's capabilities depended on whatever the operator
   happened to install, so the same ticket on two machines was two different
   runs. An eval baseline that varies with a plugin folder is not a baseline.
3. **Cost.** Tool definitions and injected system prompts are re-sent on every
   turn, and the implementer runs 120 to 600 turns per ticket.

What makes this non-trivial is that Orion DEPENDS on inherited configuration:
`/pre-push-review`, `/pm-plan`, `/pr-describe` and the rest of nj-agents
arrive by exactly this mechanism, and `orion doctor` grades a missing
nj-agents as FAIL. The fix could not be isolation.

## Decision

A supervised run gets a config directory Orion builds, and no MCP servers.

- **`CLAUDE_CONFIG_DIR`** points at `$ORION_HOME/agent-config`, built by
  `internal/agentcfg` and populated deliberately: the nj-agents skills and
  agents, symlinked from the discovered checkout, and nothing else. The
  operator's plugins, subagents and commands are simply not there.
- **`--strict-mcp-config`**, with no `--mcp-config` beside it, is passed on
  every run. This is a second lever because it reaches somewhere the directory
  does not: an account-level connector arrives regardless of which directory
  the CLI reads, so curating the directory alone would have left the write
  handles to the tracker exactly where they were.
- **The run states what it was given.** The `run-start` event records the tool
  count and the MCP servers, read off the CLI's own init frame rather than
  inferred from the flags Orion passed. Asserting the toolset is what let 179
  tools go unnoticed; this measures it.
- **An operator can opt in**, per stage or per actor, via
  `delegation.inherit_operator_config` in `orion.json`. Empty by default. An
  opted-in run gets the operator's whole configuration, and the event log
  names the opt-in that put it there.
- **The tracker's own MCP is not given to a working agent**, opt-in or not, by
  simply never being in the curated set. Orion already talks to the tracker
  itself through `internal/tracker`, under its own rules. An agent with an
  independent write path could move a ticket Orion believes it owns, which is
  the same class of problem [0001](0001-precedence-rule-orion-owns-orchestration.md)
  settles for control flow: one thing, one way to invoke it.

The setting lives in `orion.json` rather than the global `agents.json` that
[0005](0005-agent-roster-is-global.md) makes the default home for actor-level
settings, because it names STAGES as well as actors, and which capabilities a
stage needs is a property of the repository being worked — a frontend repo
wanting a design MCP in its build stage says nothing about the next repository
on the same machine.

## Consequences

- Adding a capability to a run is now an edit to Orion, not an install on the
  operator's laptop. That is the point, and it is also the cost: a genuinely
  useful MCP server has to be argued for and added to the curated set.
- Failing to build the directory fails the run. Degrading to the operator's
  configuration would silently restore the whole blast radius on the machine
  least likely to be watching for it, so a security default that turns itself
  off when a `mkdir` fails is not a default.
- A missing nj-agents warns here and still FAILs in `orion doctor`. One
  question, one answer: `doctor` grades the toolkit, this only reports that
  the run went out without it.
- The cost saving is real but is NOT the argument. It has not been measured
  across two runs of one ticket, and the security and reproducibility cases
  stand on their own if it turns out to be small. What is measurable now, per
  run, is in the event log.
