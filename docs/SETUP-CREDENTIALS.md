# Getting the Slack and Jira credentials

## The short version

```bash
orion config          # asks for everything, stores it, secrets never echoed
orion config show     # what is set and where it came from, masked
orion doctor          # proves the credentials actually work
```

`orion config` writes `~/.orion/config.env` at mode 0600. **Orion reads that
file itself**, which is the point: a shell profile like `~/.zshrc` is read by
*interactive* shells only, so credentials exported in a terminal are invisible
to cron and launchd. A scheduled `orion report --notify` would silently see
nothing. An exported environment variable still wins over the file, so a
one-off override works as expected.

The rest of this document is how to obtain the values `orion config` asks for.

Both are optional. Orion works without either; `orion doctor` reports them as
degraded capabilities rather than failures. Set `tracker.enabled` or
`slack.enabled` in `orion.json` and a broken one becomes blocking instead,
which is the point of opting in.

- **Slack** gives you a channel per project and the notification stream.
- **Jira** gives you a project per idea and the decomposed work tree.

---

## Slack: create the app and get a bot token

You need a **bot token** (`xoxb-...`). An incoming webhook will not do: a
webhook is bound to one channel at creation and cannot create any. This is the
single most common wasted hour here, so it is worth stating twice.

### Before you start

Check whether your workspace lets members install apps. Many workspaces
require an admin to approve them, in which case your app will sit in a pending
state after step 4 and nothing will work until someone approves it. Slack →
your workspace name → **Tools & settings** → **Manage apps** → **App
management settings** shows the policy. If approval is required and you are
not an admin, ask before building the app rather than after.

### 1. Create the app from a manifest

Go to **https://api.slack.com/apps** → **Create New App** → **From a
manifest** → pick your workspace → paste this:

```yaml
display_information:
  name: Orion
  description: AI-native SDLC orchestrator. Creates a channel per project and reports there.
  background_color: "#1a1a2e"
features:
  bot_user:
    display_name: Orion
    always_online: false
oauth_config:
  scopes:
    bot:
      - channels:manage
      - channels:read
      - channels:join
      - groups:write
      - groups:read
      - chat:write
      - chat:write.public
settings:
  org_deploy_enabled: false
  socket_mode_enabled: false
  token_rotation_enabled: false
```

Why each scope is there, so you can cut any you do not want:

| Scope | Needed for |
|---|---|
| `channels:manage` | creating **public** channels |
| `groups:write` | creating **private** channels (the default) |
| `channels:read` / `groups:read` | finding an existing channel by name, so a re-run attaches instead of failing |
| `channels:join` | joining a **reused** public channel. A bot is a member of a channel it created, but not one it merely found, and `setTopic` requires membership |
| `chat:write` | posting anything at all |
| `chat:write.public` | posting to a public channel without joining it first |

If you only ever want private channels, drop `channels:manage` and
`channels:read`. If only public, drop the `groups:*` pair and set
`"private": false` in `orion.json`.

**`token_rotation_enabled: false` is deliberate.** Rotating tokens expire and
must be refreshed programmatically; Orion reads a static token from the
environment and has no refresh loop. Turning rotation on would make Orion stop
working roughly twelve hours later, which is a hard failure to diagnose.

**`socket_mode_enabled: false` is also deliberate.** Socket mode is for
receiving events. Orion only posts, so enabling it adds a listening surface
with nothing behind it.

### 2. Install it

**Install App** in the left sidebar → **Install to Workspace** → review the
permissions → **Allow**.

### 3. Copy the token

Still under **Install App**, or under **OAuth & Permissions**, copy the **Bot
User OAuth Token**. It starts `xoxb-`.

```bash
orion config --only ORION_SLACK_TOKEN
```

The prompt disables terminal echo, so the token does not land in your
scrollback or a screen recording. It is stored at mode 0600 and never written
to a log or a task record.

### 4. Turn it on and verify

```json
"slack": { "enabled": true, "create_channel_per_project": true,
           "channel_prefix": "orion-", "private": true }
```

```bash
orion doctor
```

You should see the workspace named:

```
[OK  ] slack    Your Workspace as orion (workspace T01ABCDEF)
```

**Read that line, do not just check it is green.** A token for the wrong
workspace authenticates perfectly and posts into a room nobody reads, which
looks identical to working.

### 5. One thing that will confuse you later

If you add a scope after installing, **the existing token does not get it**.
You must reinstall the app. Orion's error message says so when it sees
`missing_scope`, but it is worth knowing before you hit it.

### After it works

`orion new` creates `#orion-<slug>`, sets the topic to the idea, and posts an
opening message. Bear in mind:

- **Channels accumulate.** A bot cannot delete them. Archive a finished
  project's channel rather than deleting it.
- **Private is the default** because a public channel cannot be made private
  afterwards, and a slug can reveal an unreleased project.
- **Orion still cannot listen.** The channel is the record and the
  notification stream. To talk back, drive Orion from a Claude session with
  the Slack MCP connected.
- **A pre-existing PRIVATE channel needs the bot invited.** If
  `#orion-<slug>` already exists and is private, Orion will find it but
  cannot join: Slack provides no way for an app to add itself to a private
  channel, by design. Run `/invite @Orion` in that channel once. Public
  channels are joined automatically.

---

## Jira: get an API token

### 1. Create the token

Go to
**https://id.atlassian.com/manage-profile/security/api-tokens** →
**Create API token** → name it `orion` → copy it immediately. Atlassian shows
it once.

### 2. Give them to Orion

```bash
orion config --only ORION_JIRA_URL,ORION_JIRA_EMAIL,ORION_JIRA_TOKEN
```

The email must be the account the token was created under. Jira answers
**HTTP 401** for a revoked token, a mismatched email, and a token truncated on
copy — all three look identical, so if it fails, re-enter both rather than
assuming the token is bad.

Environment variables still work if you prefer them, and override the file:

```bash
export ORION_JIRA_URL='https://yourorg.atlassian.net'   # no trailing slash
export ORION_JIRA_EMAIL='you@example.com'
export ORION_JIRA_TOKEN='...'
```

> **The trap that costs an hour.** An export made in one terminal beats
> `config.env` for the life of that terminal, and lives in no file, so nothing
> on disk reveals it. Fix the token with `orion config`, and `doctor` in that
> window keeps failing on the stale export while the same command passes in a
> new tab. Doctor now names the source on failure — `authentication failed
> (credentials from: environment)` — and gives you the `unset` line. If you see
> that, do not re-run `orion config`; the file was never the problem.

### 3. Verify

```bash
orion doctor
```

Three outcomes, and they mean different things:

```
[OK  ] jira   authenticated as Navjyot Nishant, can create projects (via CREATE_PROJECT)
[WARN] jira   cannot create projects
[WARN] jira   permission undetermined
```

**"Undetermined" is not "denied".** Some deployments restrict the permissions
endpoint, so Orion cannot tell. It will attempt creation and fall back
cleanly. Do not go asking for permissions you may already have on the strength
of that line.

### 4. If you cannot create projects

Orion defaults to a project per idea, which needs the **Create team-managed
projects** global permission. Most accounts do not have it, and that is fine.
Bind to an existing project instead:

```json
"tracker": { "provider": "jira", "project_key": "PLAT",
             "create_project_per_idea": false,
             "confirm_tree_before_create": true }
```

Orion then creates its issues inside `PLAT` and never tries to make a project.

### 5. Worth knowing before you enable per-idea projects

- **Keys are globally unique** per instance and capped at 10 characters.
  Orion derives one from word initials (`claim-status-self-service` → `CSSS`)
  and appends a digit on collision.
- **A non-admin cannot delete a Jira project.** One per idea accumulates
  permanently. If you run many small ideas, bind to one existing key instead.
- **The work tree is always previewed** before anything is created, and that
  approval is not waived by `auto_merge`. A sandboxed workspace can be
  deleted; issues in a shared tracker are seen by other people and cannot be
  cleanly withdrawn.

---

## Keeping the secrets out of the way

Credentials live in `~/.orion/config.env`, outside any repository, at mode
0600. `orion config show` reports the mode and tells you how to fix it if it
ever widens.

Nothing is ever echoed: the prompt turns off terminal echo for secrets, and
`config show` masks them to a recognisable prefix and suffix only.

Orion's generated workspace settings already deny the agent read access to
`.env*`, `~/.ssh` and cloud credential files, and the supervisor strips cloud
credentials from the child environment. `ORION_SLACK_TOKEN` and
`ORION_JIRA_TOKEN` are read by the **binary**, not passed to the agent.
