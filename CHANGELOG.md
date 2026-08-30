# Changelog

Notable changes per release. Written for someone deciding whether to upgrade
and what to watch afterwards, so each entry says what changed **and what it
now refuses to do**.

## Unreleased

## v0.8.1

### Added

- The QA stage now derives its test cases through a cheap subagent before the
  verification run starts, so the ticket's acceptance criteria and the
  branch's diff no longer ride along on every turn of the test authoring that
  follows: only the derived list of cases does. The deriving is wide reading
  with a short answer, and everything in a prompt is re-sent on every turn, so
  the criteria were being paid for repeatedly to say once what a dozen lines
  of case list say. The derive run is attributed to its own `case-derive`
  actor (haiku by default, configurable like any other agent through
  `orion config agents`) and its spend appears as its own row in the ticket's
  cost report rather than hiding inside QA's total. The list it returns is
  written to the event log, and if the step fails for any reason QA runs
  exactly as before — reading the criteria and deriving its own cases — so
  this can never be the reason a ticket has no tests (OR-182).

- Stage boundaries in the run log. Every crossing a ticket makes — routed to
  implementing, implementing to QA, QA to a fix round and back, QA to push,
  push to pull request, pull request to CI, CI to human approval, a red build
  to devops, approval to merge — now prints its own line naming both sides,
  so a reader no longer has to know which actor holds which role and infer the
  handoff from the names changing. The commit count rides on the boundary out
  of implementation rather than standing in for one.
- A `stage` event kind in `.orion/events.jsonl`, carrying the two stage names
  and the actor identifier on each side. "How long did this run spend in QA"
  and "how long did it sit waiting for a human" are answerable from the log
  for the first time.

- `orion routes` prints the ticket-routing table: every routable actor with the
  exact issue-type, component and label keywords it accepts, plus the actors
  that are deliberately unroutable and the path that reaches each of them
  instead. The vocabulary is now a published contract rather than a private
  constant, so whatever creates a ticket can set the marker instead of keeping
  its own copy that drifts.
- `orion queue` reports the routing distribution of the work it is about to
  show, before any of it runs. A queue in which every ticket takes the default
  is now visible as such, with a pointer to `orion routes`.

- `orion doctor` runs `dun verify` and prints its verdict for both the checkout
  and the sandbox clone, rather than only reporting whether hooks exist.
  `orion doctor --fix` additionally runs `dun replay --apply`, retrying failed
  determinations against a journal that has since learned more. Git history is
  not rewritten; only the replay log gains the corrected outcome.

- A tripped breaker now leaves an escape path. A session whose breaker has
  fired may still run six commands — `git status`, `git diff`,
  `git checkout -- <path>`, `git restore`, `git add`, `git commit` — enough to
  revert what it touched and commit what compiles, and nothing else. Compound
  commands and `git commit --amend` are refused, the allowance does not
  refill, and spending it never clears the trip. Full rules in
  `docs/BREAKERS.md`.
- `plans/BLOCKED.md` is now written by the breaker itself, as part of
  tripping, so the record of why a run stopped no longer depends on the agent
  getting one more tool call. An existing note is appended to, never replaced.

- `orion config agents --list` prints the whole agent roster as a table: every
  actor's id, name, designation, model and effort as a run will actually resolve
  them, plus which of those fields `~/.orion/agents.json` overrides. The override
  file holds only overrides, so most actors are absent from it and their
  effective model was previously only readable in Go source — this is the one
  table that shows what an unattended run will cost before you start it. The
  flag never prompts and works with stdout redirected; piped output carries no
  escape codes and reads the same as the coloured form.

- `orion config limits` shows every circuit breaker with the value actually in
  force and where it came from — this project's `orion.json` or the shipped
  default — and ends with the concurrency line `orion watch` prints in its own
  banner, taken from the watcher's own resolver rather than computed a second
  time. Until now `max_concurrent_tickets` could only be changed by opening
  `orion.json` in an editor, and an adopted repository's `limits` block does
  not usually contain the key at all, so the setting an operator was told to
  change was not there to find.
- `orion config limits KEY N` sets one limit without an editor —
  `orion config limits max_concurrent_tickets 3`. The key is **written when it
  is absent**, not only updated when present, which is the common case. The
  whole `limits` block is covered, so `max_session_minutes`, `max_tool_calls`
  and `max_files_touched` are reachable too rather than needing their own
  setter later.
- A value above the ceiling is **refused, naming the ceiling, and nothing is
  written**. `max_concurrent_tickets` is clamped to 5 when it is read, so
  storing 40 would leave the file saying 40 while the watcher ran 5.
- The write goes to the file the watcher reads — the registered project's
  working copy, resolved through the registry — so the change takes effect on
  the next start with no commit or push, even when the command is run from
  inside a sandbox clone. Every set says that a watcher already running keeps
  the value it started with and has to be restarted.

Edits are made as text, so the `_comment_*` keys stay next to the settings
they explain and no other key in the block is touched.

- Every supervised run now also appends one row to `~/.orion/usage-history.jsonl`
  — a global, append-only usage history that is never rotated or truncated. The
  usage event still goes to the workspace event log (`orion cost` reads it there
  and nothing about that changes), but that log is capped at 2 MiB across five
  generations, so the oldest rows — the ones a benchmark wants most — used to
  disappear silently. Both copies are written from one function, so they cannot
  drift. Each row carries the ticket key, project, actor, model, effort, stage,
  session id, start and end timestamps, and the existing turn, token and cost
  fields, so it joins to the tracker and to the event log. JSONL, not a
  database: `grep`, `jq` and DuckDB read it in place.
- The usage event and the history row now record the **model and effort the run
  was dispatched with**. Reading them back from the roster later would answer
  for today's `agents.json` — so moving an actor from opus to sonnet used to make
  every earlier run ambiguous about which model produced it.

- The QA stage now puts what it found, and how it was resolved, on the
  ticket. Each fix round leaves exactly one comment carrying three facts —
  what QA found, what the implementer changed in response (one line, in their
  own words), and whether the re-verification passed — attributed to QA and
  naming the actor that made the change. A run where QA found nothing leaves
  one line saying so, because silence on a ticket cannot be told apart from a
  run where QA never happened. When the round ceiling is reached with findings
  still open, the ticket says that too, which was the case most likely to be
  lost. The comment count is bounded by the existing round cap, so it is at
  most two exchanges plus a verdict per ticket. Previously a CI failure
  reached the ticket and a QA failure did not, so the entire exchange existed
  only in the run log and the terminal — and the ticket is the artifact anyone
  revisits weeks later. Comments only: no run log is ever attached, because a
  raw log is hundreds of kilobytes against a 2 GB quota, cannot be grepped
  once uploaded, and carries everything the agent read and wrote (OR-200).

### Changed

- A boundary whose next party is CI or a person says so and says no agent is
  running, rather than naming an agent that is neither running nor costing
  anything.
- The terminal echo of a notification now renders in the same columns as every
  other line — timestamp, ticket key, and its level in the status column —
  instead of the bare `[orion:<level>] <title>` that broke the alignment
  wherever it landed. It is still title-only; the formatted body still goes to
  Slack.
- Boundaries degrade to ASCII on a non-UTF-8 terminal with the transition
  intact, the same way the status icons already did. The status vocabulary is
  unchanged: a handoff is still `ok`, and is told apart by its layout.

- The routing table reaches five actors instead of three. The architect and the
  product manager are now routable (`architecture`, `architect`, `adr`;
  `product`, `pm`, `requirements`), alongside the existing docs and frontend
  rules and the backend developer as the default. Matching is still equality
  rather than containment, and precedence is still the written order.
- The decompose stage now tells the planner to run `orion routes` and set the
  marker on every item it creates. Without this, routing metadata was set by
  luck and in practice never.
- A ticket with no routing marker now reports "defaulting to the implementer;
  no routing marker on this ticket" rather than "no issue type, component or
  label matched a route". The announcement is unchanged in substance; it no
  longer phrases the normal outcome as a miss.

- The test suite no longer sleeps through most of its own runtime. Two
  supervisor tests proved the wall-clock deadline kills a process group by
  waiting out a real sixty seconds each, and `scripts/test.sh` ran them three
  times over — plain, coverage and race — so roughly 410 seconds of every CI
  job was spent deliberately idle. The deadline is now injectable, so those
  tests assert the same property against a sub-second budget, and coverage is
  measured during the test pass rather than in a second run of the whole
  suite. Neither test was weakened or skipped: they still assert that no child
  survives, which is the OR-141 process-group kill they exist to cover. CI
  wall time drops from roughly nine minutes to under three and a half.

- `limits.max_concurrent_tickets` now defaults to **4**, raised from 2. The
  original 2 was a deliberate starting point rather than a permanent setting:
  every hazard concurrency introduces — git against the one shared clone, a
  budget checkpoint crossed by runs already in flight, one rate limit reached
  by several sessions, tickets picked that all edit the same files — is
  invisible at 1 and obvious at 2, so the rule was "prove it at 2, then raise
  it". Two has now been proven across a full release, and this is that raise.
  It stops short of the hard ceiling of 5 so that reaching the maximum stays
  an explicit choice. Repos with an explicit value in `orion.json` are
  unaffected; `orion init` writes 4 for new ones.

### Fixed

- Agent commits made in a job worktree now carry an AI-Attribution trailer.
  `orion init` only ran `dun init` against the checkout you typed; the sandbox
  clone under `~/.orion/projects/<id>/repo` is a separate git repository and
  was never instrumented, so every commit an agent made had no attribution
  trailer at all. Each job now instruments the clone before it starts, which
  covers all of its worktrees at once. Note that those commits will still read
  `unmatched` until whodunit's Claude Code adapter is fixed: it maps `/` to `-`
  when locating transcripts but not `.`, while Claude Code maps both, so no
  transcript is ever found for a sandbox under `~/.orion`. See the note in
  `internal/adopt/attribution.go`.
- `orion status` no longer reports "NOT instrumented" from inside a worktree.
  It read `<dir>/.git/hooks`, and in a worktree `.git` is a file, so the answer
  was wrong in the one place every agent commit is made. It now asks git where
  the hooks actually are, honouring `core.hooksPath`.

- A run that ends with its breaker tripped no longer abandons a dirty
  worktree. Orion reverts uncommitted **tracked** changes, leaves commits and
  untracked files alone, and reports it in the run output and to the
  operator's Slack channel. Left in place, that residue blocks the automatic
  rebase of the branch on the next `orion collect`, which is a slow and
  indirect way to discover that a run needed a person.

- Forcing `orion watch` to quit (a second ctrl-c, or a second `kill`) now kills
  the agents it started instead of only itself. The second signal used to be
  handed back to the default disposition, which terminated the watcher and left
  every `claude -p` running — reparented to init, still holding a worktree and
  still spending, killable only by pid once somebody noticed. The watcher now
  SIGKILLs each agent's whole process group, names any pid that survived, and
  exits 130. SIGTERM behaves the same as ctrl-c throughout.
- The force warning said forcing "risks leaving a ticket claimed with nothing
  running", which was the opposite of what happened. It now says what forcing
  does, and the force path names the tickets left claimed rather than going
  silent — their agents are dead, but `orion-working` stays on them until it is
  removed in the tracker.

- `orion watch` now prints the slot arithmetic behind every dispatch, so a run
  at less than full capacity is visible instead of having to be inferred —
  `cap 2, 1 claimed elsewhere (OR-192), 1 free; starting 1 of 5 queued`. A
  claim held by another watcher was subtracted from the free slots correctly,
  but the reduction was only *reported* when it reached zero, so a cap of 2
  with one stale claim ran one ticket at a time and said nothing about it. The
  holders are named rather than counted, because "1 claimed elsewhere" is a
  fact and "OR-192" is something you can go and look at. Terms that trim the
  number further — `--max-jobs`, a rate-limit pause — are named too, so the
  sum always adds up.
- The line is printed even when nothing is claimed elsewhere, so "2 free,
  starting 2", "2 free, starting 1 because only 1 is queued" and a slot lost
  to a stale claim are now three distinguishable outcomes rather than three
  identical-looking ones.
- Where a claim is held outside this watcher, the terminal now says what to do
  about it: a ticket finished outside Orion's own close path keeps its
  `orion-working` label, and removing that label is what releases the slot.
  Previously the operator had to know the fix involved a label they were never
  shown.

- The event log now records the reasoning, not just the mechanics. Two of its
  22 kinds had never fired once in the life of the project: `answer` (what an
  advisor replied to a question the implementer stopped on) and `decision`
  (a choice, and why). `orion logs` could tell you an agent ran
  `sed -n 460,560p supervisor.go` and never why it changed approach.
  - An advisor that could not be reached used to return a refusal to the caller
    and emit nothing, so the log recorded a question and never what became of
    it. Every `ask` is now closed by an `answer` or a `refuse` on every path.
  - `ask`, `answer` and `refuse` carry the whole text rather than its first
    line, the way `say` already carries the agent's own prose. The last ask ever
    recorded ends `...worth having on record:` — cut one line into the thing it
    was about to put on record.
  - Which actor a ticket routed to, and why, is a `decision` rather than a
    `note`; the decision recorded on the branch says what was chosen and what
    it was grounded in, rather than only naming the file it was written to.
  - What separates a `decision` from a `note` — an alternative not taken, and
    the reason — is written next to the constants, so the distinction survives
    the next person choosing a kind.

## v0.8.0

### Added

- `vcs.require_up_to_date` in `orion.json` controls whether `orion protect` requires branches to be up to date before merging (GitHub's `strict` check). Defaults to `true`, matching prior behaviour. `orion protect` now states the value it is applying and where it came from, and `--dry-run` reports any mismatch against what the branch currently has on GitHub instead of silently overwriting it.

- A `Fan` primitive in `internal/supervisor` runs N subagent runs concurrently
  instead of one after another, capped at a configurable
  `limits.max_concurrent_children` (default 2). One child failing does not
  discard the others: every child runs to completion and its result comes
  back regardless of what its siblings did. Each child keeps its own actor,
  model and ticket key, so the cost report still shows one row per child, and
  Fan states the fleet size, cap and models before any child starts. This is
  concurrency inside a single run only -- more than one ticket in flight at
  once is a separate, larger change (OR-181).

- `orion explore "<question>"` answers one narrow question about the repository
  in a subagent's own context and cites the files the answer came from, so an
  agent that needs to know where something is defined no longer greps and reads
  until it finds out with every file it opened staying in its context, re-sent
  on every remaining turn of the run. The implementer is told about it in its
  prompt. The explore run is its own actor on its own pinned cheap model,
  attributed to the same ticket, so its spend is a separate row in that ticket's
  cost report rather than hidden inside the asking run's total. The answer and
  its cited paths both go to the event log, so what a run was told can be
  checked afterwards. An answer citing no file is reported as unproven, and an
  explore that fails for any reason sends the caller back to reading the
  repository itself — it can only ever save reading, never prevent it (OR-183).

- `orion watch` now works several tickets at once, capped by
  `limits.max_concurrent_tickets` in `orion.json` — 2 by default, 5 is a hard
  ceiling and a larger number is clamped rather than honoured. A tick
  reconciles, then tops the running set back up to the cap, instead of
  starting one ticket and blocking until it finishes. The cap and where it was
  read from are printed in the startup banner. Set it to 1 for the previous
  strictly-sequential behaviour.

- `internal/conflict` and `orion conflict verify` check a merge resolution for
  changes that were silently dropped. Beyond conflict markers and merge-tool
  litter, it compares the resolved tree against BOTH parents: a file that both
  sides changed and which came out byte-identical to one of them does not
  contain the other side's edit. That is occasionally correct, so it is
  reported with the specific claim to check rather than treated as a failure.
- The reason this is not "run the tests and trust them": on 2026-08-29 a
  hand-resolved three-way conflict built, vetted and passed the package's own
  tests while having reverted one ticket's actor routing and violated a
  property another had just introduced. It was caught only because one of the
  conflicting tickets happened to have added a test that failed. A green
  build is the floor, not the proof, because it cannot distinguish a line
  deliberately removed from one that was lost. This is the guardrail half of
  OR-186; dispatching the devops agent to resolve conflicts in parallel, and
  the bounded Slack question when the diff cannot decide, are still to come.
  The guardrail is usable on its own, because whoever resolves a conflict
  today needs exactly the check the agent will need later.

- `orion release status <version>` answers "what is in this release, and does
  the changelog agree" before the tag exists rather than after. It lists the
  milestone's tickets split into done and not done, and reconciles them
  against the `.changelog.d/` fragments in BOTH directions: a done ticket with
  no fragment is a change that would ship unmentioned, and a fragment with no
  ticket in the version is a note for something that is not in this release.
  It also lists open tickets carrying no `fixVersion` at all, which is the gap
  the convention exists to close.
- Mismatches are reported, never resolved: the only two ways to "fix" one
  automatically are to invent a release note or delete one, and both are worse
  than telling a person. Exit is non-zero on a mismatch so it can gate a
  release, but an INCOMPLETE milestone is not a failure -- the correct
  behaviour there is to ship what is done and roll the rest forward, because
  one stuck ticket must never hold a tag hostage.

- `orion release verify <version>` runs the five promotion checks: the
  milestone is complete, fragments and the version reconcile, the integration
  branch is green ON THE EXACT COMMIT being promoted, no open pull request is
  about to land, and every commit in the range is attributable to a ticket in
  the version. Blocking and warning are split deliberately -- a gate that
  refuses for everything is bypassed within two releases, and one that warns
  about everything is not a gate. Unfinished tickets and hand-pushed commits
  warn; a shipped change with no release note, a build that failed or ran on a
  different commit, and a pull request one click from landing all block.
- "Green on the exact SHA" is two separate findings, because they mean
  different things: a failing build is a broken tree, while a build for a
  different commit means the verdict being trusted was produced by code other
  than the code about to ship.
- The decision is a pure function over gathered inputs, so the blocking and
  warning split is asserted in tests without needing a git repository, a Jira
  and a forge. This is the verification half of OR-188; opening the promotion
  pull request, asking in Slack at its own level, and merging, tagging and
  publishing on approval are still to come.

- `orion release create <version>` and `orion release list` manage Jira
  versions, which Orion uses as release milestones. Creating one is
  idempotent: a version that already exists is reported rather than erroring,
  because a command that fails on re-run cannot be called from automation,
  which is the whole reason it exists -- an automated promotion has to create
  the next milestone unattended. `internal/tracker` gains `CreateVersion`,
  `ListVersions`, `FindVersion` and `MarkReleased` over the same REST client
  that already provisions projects. The Jira MCP cannot create a version,
  which is why this had been a manual step in the Jira UI; that is a limit of
  the MCP rather than of Jira.
- `release` is a noun with subcommands and never acts on its own: a bare
  `orion release` prints its usage and exits non-zero. In this repository
  "release" already means cutting the binary -- a tag, the Homebrew tap and
  the Scoop bucket -- and the failure modes are not symmetrical, so the
  dangerous meaning can only be reached by naming it explicitly.

### Changed

- The budget checkpoint is now admission control rather than a pre-flight
  check. A run is admitted only if the budget covers it *including* what is
  currently running: its expected cost — the mean of the runs actually
  recorded in the window, never an invented figure — is reserved on dispatch
  and released on completion. Before this, several concurrent runs all read
  the same spend, all passed the same check, and all spent through it, so a
  95% stop was crossed by the runs already in flight.
- Git against a project's shared sandbox clone is serialised. Worktrees
  isolate files and share the object store, refs and packed-refs, so
  concurrent job starts, worktree removals and auto-rebases were writing to
  one `.git` — which handed two jobs the same branch name and failed on ref
  and config locks.
- A rate limit is decided once for the whole watcher instead of per run: the
  first run to report an exhausted window pauses *dispatch* until the reset
  (capped at 30 minutes, as before), while everything already running is left
  to finish.
- When more than one ticket starts together, they are chosen to spread across
  areas — a ticket's Jira component, or failing that its project — rather than
  taken strictly top-N by priority. Concurrency does not cause merge
  conflicts; starting N tickets that all edit the same files does. Rank still
  decides what gets worked, and with a cap of 1 the order is unchanged.

### Fixed

- The develop-to-main promotion pull request no longer rebuilds a tree the
  push build already tested. `push: [develop]` and a bare `pull_request:`
  both matched the promotion, whose head branch is always develop, so the
  full three-OS matrix ran twice on the same SHA; excluding `base=main` from
  the pull_request trigger drops the duplicate and nothing else, since
  feature pull requests target develop and still build. Fixed in both this
  repository's workflow and the one `orion init` scaffolds -- the scaffold is
  where it costs money, because a private repository bills macOS at 10x and
  Windows at 2x, roughly 61 billable minutes per release. Corrects the record
  from OR-172, which attributed the duplication to the concurrency group:
  grouping decides which runs cancel each other and can never merge two
  events into one run.

- The in-flight check counts claimed tickets instead of answering yes/no, so a
  ticket claimed by another watcher consumes one slot rather than holding the
  whole queue.

- Quota exhaustion is no longer detected from the agent's own words. The
  patterns were matched against combined stdout and stderr, and stdout is
  where the agent's prose lives, so a run working a ticket *about* rate
  limits paused itself for a limit that did not exist and then backed off
  again with the delay doubled. Detection now reads the error channel only:
  bare stderr lines and structured NDJSON entries that are not assistant
  messages or tool results. Tightening the patterns was rejected as a fix,
  because the next false positive is a ticket about HTTP status codes or a
  test for this very file -- the channel was wrong, not the wording.
- A quota notification's title no longer claims a reset the provider never
  stated. `Verdict.Parsed` already kept the body honest, but the title read
  "waiting Nm for quota reset" either way, and the title is what a Slack
  notification and a mobile push actually show.

## v0.7.10

The parallelism release. Two of these are the same bug in different clothes:
the loop breaker punished fan-out, and the write primitive underneath it was
not safe for it either. Both had to go before subagents could be used at all.

### Added

- The CI fix loop triages a failing job's log through a cheap subagent before
  the fix run sees it, so the raw log no longer rides along on every turn of
  that run: only a short report of what broke and why does. A CI log runs to
  thousands of lines and everything in a prompt is re-sent every turn, so the
  log was being paid for repeatedly to say one thing once. The triage run is
  its own actor on its own pinned cheap model, attributed to the same ticket,
  so its spend is a separate row in the cost report rather than hidden inside
  the fix run's total. Triage failing for any reason falls back to the raw
  log, so this can only reduce what is sent, never withhold it (OR-143).
- Tickets are routed to the actor matching their label, component or issue
  type instead of every ticket landing on the backend developer. A
  `documentation` label goes to a new docs actor on sonnet; a UI label goes to
  the frontend actor, which had been in the roster and configurable since it
  was written and had never once worked a ticket; everything else still
  defaults to the implementer, and the run says which actor it picked and
  why. QA's fix loop resumes the actor that opened the branch, so a routed
  ticket's fix is not committed under a different actor's session (OR-171).

### Fixed

- The loop breaker no longer trips on parallel fan-out. A subagent shares its
  parent's session id but writes its own transcript, and the identical-call
  counter keyed on the session, so a parent and its child each innocently
  reading the same file summed into a false `breaker/loop` trip that killed
  the run outright. It now keys on the transcript. This breaker was
  penalising exactly the behaviour it was least suited to judge, and it had
  already killed two runs (OR-170).
- `procsafe.WriteFile` could publish a mixture of two writers inside one
  process. The temporary file was named from the pid and the clock, and every
  goroutine in a process shares a pid, so two starting in the same nanosecond
  opened the same temporary file, interleaved into it, and renamed the blend
  into place. Across processes, which is what it was written for, it was
  always safe; within one it was not, and that is what parallel agents are
  made of. Now uses `os.CreateTemp`, and restores the caller's file mode
  before publishing so the visible file is never briefly 0600 (OR-180).
- `orion collect` reads the branch a job actually used instead of recomputing
  it by convention, so a retried ticket, whose branch takes a `-2` suffix to
  avoid colliding with a prior attempt's open pull request, is no longer
  looked up under a name that never existed (OR-173).
- A ticket in `orion-ci-wait` whose pull request cannot be found is released
  and marked for a human instead of being polled forever. One ticket sat
  printing the same warning every two minutes for ninety minutes while its
  work was finished, green and unmerged (OR-173).
- The CI fix loop tells a sandbox-policy denial apart from an agent that
  could not see the fix. When the sandbox refuses the agent's only edit, for
  example under `.github/workflows/**`, Orion now reports "blocked by policy"
  naming the tool, path and matched rule, hands off the agent's own diagnosis
  in full rather than discarding it, and does not spend the denied attempt
  against the three-attempt budget. It previously reported a correct,
  complete diagnosis as "the agent does not know how to fix this" (OR-174).
- The CI fix loop's activity goes through the same attributed logger as every
  other supervised run rather than a hand-rolled callback: console lines
  carry the ticket, actor and model, and every tool call reaches the event
  log again. They were not being recorded at all (OR-176).
- Lesson proposals are keyed on the fix run's stated root cause rather than
  the CI check name, which in a repo with one job matrix is identical for
  every failure that will ever happen and collapsed all of them into a single
  vacuous "CI sometimes fails". The cause is normalised, so two sightings of
  one mistake in different files still count as a repeat, and a run that
  stated no cause proposes nothing at all (OR-177).
- A pending lesson no longer prints between "merged on approval" and "merged
  into the integration branch" at WARNING level in bare past tense, which
  made a correctly merged green branch read as a merge over a red build. It
  prints after the merge is fully reported, informationally, and names the
  commit and time of the failure it is about (OR-178).

## v0.7.9

### Added

- QA's own tests are now proven against the commit the branch started from,
  before they count. Every test file QA adds or changes is laid onto that
  pre-change commit, alone, in a throwaway worktree, and this repository's
  own suite is run against it; a test that already passes there does not
  exercise the change and is reported as such -- on the console and in the
  event log -- rather than silently accepted. This does not block the
  branch: QA reports findings and does not gate on its own authority, and
  this check carries the same authority (OR-156).

### Fixed

- QA's full findings now reach the event log every round, not only when the
  round ceiling escalates to a person, so the common case (fixed in round
  one) leaves a durable record instead of nothing. The console line shows
  the first SUBSTANTIVE line of the findings, skipping a header like
  "Verification done. Summary:" which previously consumed the one line that
  survived, and says when the full text is in the event log. Findings are
  also posted to the ticket every round, not only at the ceiling (OR-167).
- A Slack approval request now reads differently from a status update from
  its first word, not only its colour. `notify.Level` (info / warning /
  blocked) was computed on every event and then dropped: `slack.Post` sent
  only a channel and text, so "FCIA-8 needs a decision from you" and
  "FCIA-8 is ready for review" arrived looking identical -- which matters on
  a phone, where a push notification shows text and nothing else. A blocked
  message now opens with "Action needed", a warning with "Heads up"; an
  attachment colour bar reinforces the distinction on desktop and never
  replaces it (OR-163).
- CI concurrency groups now key on the pull request number where there is
  one, falling back to the ref for a plain push, in the scaffolded CI and
  secret-scan workflows and in this repository's own. This keeps a pull
  request's run and a push run on the same branch name from cancelling one
  another, so a rebase force-push cancels only its own superseded run
  (OR-172).

  It does NOT stop the develop-to-main promotion pull request from building
  twice, which is what OR-172 originally claimed to fix. That doubling comes
  from the trigger list, not the grouping: `push: { branches: [main,
  develop] }` and `pull_request:` both match a pull request whose head
  branch is `develop`, and the two events are by construction in different
  groups, so no grouping expression can merge them. Feature branches are not
  in the push list and were never affected. Cost is one extra matrix per
  release, not per pull request. Tracked in OR-175.

## v0.7.8

### Added

- Nine architecture decisions from 2026-08-28 are now recorded under
  `docs/decisions/` instead of living only in Jira ticket descriptions:
  the Orion/toolkit precedence rule, declining superpowers as a
  dependency, scoping ponytail to development, no SQLite, the global
  agent roster, `orion new`/plan as sequential phases, standing auto
  effort, parallelism level ordering, and the canonical slug. The
  precedence rule is also now stated in a new root `CLAUDE.md`, since it
  binds future integration work rather than only recording a past choice
  (OR-161).
- `docs/BREAKERS.md`: what each breaker means, which recoveries exist for
  which trip, what an agent should do when one fires, and what a human should
  do afterwards. Two runs stopped to ask this and an advisor correctly
  refused to invent the answer for lack of a written spec. The block message
  now points here, so the next run reads the policy instead of asking
  (OR-169).

### Changed

- The CI fix loop now requires the agent to state a root cause, distinct from
  the failure log, before it writes a patch. A symptom-derived fix is the
  most common way a fix attempt gets spent without moving the ticket forward,
  and the loop only gets three per ticket (OR-157).

### Fixed

- Ending a `claude -p` run now kills its whole process tree on **every** exit
  path, not only the wall-clock timeout. A grandchild the agent spawned via
  `bash` (a test run, `npm`, a dev server, `docker`) previously survived the
  kill and kept running orphaned, holding the worktree, burning CPU, and
  possibly still writing files after Orion reported the run stopped. Unix
  only for now; Windows keeps today's direct-child-only kill (OR-141).
- A tripped breaker no longer advertises a recovery it does not have. The
  block message printed "if the trip is unverified-edits, running the tests
  or build is still allowed" on **every** trip kind, and two agents in a row
  read that on a loop trip as "Bash is open", tried it, were refused, and
  reported the breaker as contradicting itself. The line is now specific to
  the trip that actually fired: a sealed trip says plainly that there is no
  self-service recovery, and only an unverified-edits trip offers the verify
  as the way out (OR-169).
- A tripped session is now told to **commit whatever compiles** before it
  stops, not only to write its stop-note. A branch with commits can be
  resumed; a plan file describing uncommitted work cannot, and one run
  stranded a half-written function exactly that way (OR-169).

## v0.7.7

### Fixed

- `orion watch` no longer parks itself for days on a rate limit that is not
  exhausted. At 80% of the weekly allowance the CLI reports a graded status,
  every value other than the literal `allowed` was read as a refusal, and the
  watcher printed "the seven_day limit is exhausted" and slept until Monday.
  Statuses are now classified in three: allowed (including graded
  `allowed_*` values near a ceiling), a genuine refusal, and one this build
  does not recognise. An unrecognised status still stops, because the CLI's
  vocabulary is undocumented and guessing wrong in the other direction is
  worse, but it is now reported *as* unrecognised and quotes the raw value
  instead of asserting something false about the account (OR-162).
- Every rate-limit window is kept, not just the last one reported. The CLI
  emits one event per window and Orion overwrote on each, so a five-hour
  pause and a weekly one were indistinguishable and whichever arrived last
  decided both the message and the wait. `Wait` now counts down to the
  **soonest** blocking window, so a two-hour pause no longer sleeps until the
  weekly reset, and the message names the window actually blocking (OR-162).
- A single rate-limit reading can no longer park the watcher indefinitely:
  sleeps are capped at 30 minutes and then re-checked. Waking early costs one
  refused tick; waking late costs every ticket the queue would have finished
  (OR-162).
- `agents.<id>.model` and `agents.<id>.effort` now reach the `claude`
  invocation for every actor, not only QA. The implementer's runs (the first
  one and the one resumed with an advisor's answer, including the fix run
  inside the QA loop), the ci-fix run attributed to the devops engineer, and
  the PR describer previously inherited whatever the operator's CLI was
  configured with, so configuring a model changed the banner, the event log
  and the cost attribution but not what actually ran. An actor with no model
  or effort configured still passes neither flag, which continues to mean
  "whatever the CLI is set to" (OR-133).

## v0.7.6

### Fixed

- Running more than one `orion watch` at a time, one per project repository,
  no longer loses spend. The usage ledger and the repository registry were
  both load-modify-save with no lock and a shared `.tmp` filename, so two
  Orion processes writing at once published whichever snapshot renamed last:
  measured at **one run recorded out of twelve** concurrent writers. Budget
  enforcement was therefore materially weaker than it appeared for anyone
  running two watchers, because spend accumulated far slower than reality and
  the weekly checkpoint fired late. Both now take a cross-process lock across
  the whole read-modify-write, and write through a per-process temp file so
  two writers can never interleave inside one (OR-138).
- The lock is the one `internal/state` has used for the hook path since the
  beginning, lifted into `internal/procsafe` so there is a single
  implementation rather than a second one growing beside it. Its behaviour is
  unchanged, including the part that matters most: a lock it cannot take
  degrades to an unserialized write that says so, and never blocks a watcher
  or returns a nil release function (OR-138).

## v0.7.5

### Changed

- The agent roster (name, model, effort) is now global -- `~/.orion/agents.json`,
  shared by every project -- instead of a block in each repository's
  `orion.json`. Who the implementer is and what it runs on is an operator
  preference, not something that should differ by checkout, or need
  restating in every project it's adopted into. `orion config agents` writes
  this file directly; existing per-project `agents` blocks in orion.json are
  no longer read and can be deleted (OR-132).

### Fixed

- `orion config --help` (and `orion config agents --help`) fell through to
  the interactive credentials wizard instead of printing help, and blocked
  on stdin waiting for a Jira URL nobody meant to type -- the only way out
  was Ctrl-C. A help flag is now checked before any subcommand dispatch
  (OR-132).

## v0.7.4

### Added

- `orion config agents` -- an interactive wizard for the agent roster.
  Per actor: a free-text name (Enter keeps it, `-` clears it) and two
  numbered menus, model and effort, so neither can be set to a typo. An
  actor can now also run at a chosen `claude --effort` level (`low`,
  `medium`, `high`, `xhigh`, `max`) via `agents.<id>.effort` in `orion.json`,
  the same way it can already be renamed or moved to a different model.
  `orion config agents --reset [id...]` restores the shipped defaults for
  one agent, several, or the whole roster, without going through the wizard
  (OR-131).

## v0.7.3

### Added

- Orion now rebases a stale branch itself. `ci.require_up_to_date` makes every
  merge invalidate every other open pull request, and the answer was always the
  same three commands typed by a person — fetch, rebase, force-push — once per
  merge per open branch, which grows with the square of the queue. A branch that
  is behind its base **and merges cleanly** is now replayed onto that base and
  pushed with `--force-with-lease`, and the ticket stays in ci-wait so the checks
  re-run against what would actually be merged. A branch that **conflicts** is
  still handed to a human, exactly as before: resolving an overlap is judgement
  (OR-114).
- `collect.auto_rebase` turns it off, for anyone running Orion against a
  repository they do not own. It defaults on. Consecutive automatic rebases are
  capped at two per ticket; a ticket that hits the cap is escalated with the
  manual commands rather than pushed a third time. Every rebase is an event in
  the log and a comment on the ticket, attributed to Orion (OR-114).
- A worktree can be locked for manual work by dropping a `.orion-manual-lock`
  file in it; both the CI fix loop and the auto-rebase path back off entirely
  while it is present, so a person resolving a real conflict by hand no longer
  races an unattended agent force-pushing the same branch (OR-130).
- `vcs.allow_release_branch_merges` — a named opt-in for a repository that
  genuinely has one branch and no release process. Every run that uses it
  prints what it gives up: there is no human promotion step left (OR-115).
- Every `orion watch` and `orion work` console line now carries a `15:04:05`
  time prefix and a status icon (`✓ ◐ ⏳ ⚠ ✗ ○`), so a log read after the fact
  says when each step happened and a state change is visible while scanning.
  Terminals that announce they cannot render the glyphs — `NO_COLOR`,
  `TERM=dumb`, a non-UTF-8 locale — get the ASCII set (`+ > ~ ! x .`) instead.
  The status word is unchanged in both cases, and the event log format is
  untouched (OR-125).
- A QA agent and a QA stage. After the implementer's change is committed and
  before the pull request opens, a QA agent derives test cases from the
  ticket's acceptance criteria, writes the tests the implementer did not,
  runs them, and reports what failed. Findings go back to the developer as a
  message, it fixes, QA re-verifies; after `qa.max_rounds` fix rounds
  (default 2) Orion tells a person what is still open. QA reports and never
  blocks: the pull request still opens (OR-126).
- `qa` in `orion.json`: `enabled` (absent means on; set `false` for a
  repository that does not need the spend), `max_rounds`, and `e2e_base_url`
  — the explicit non-production target an end-to-end run may use. Without
  one, QA authors and runs unit and integration tests only and says so
  (OR-126).
- A `qa` actor in the roster, "Anita · QA engineer" by default, with its
  name, designation and model configurable through the `agents` block like
  every other actor. Its runs are attributed to it in the event log, so they
  appear as their own row in the ticket's cost report (OR-126).
- The QA agent uses nj-agents' `/test-suite-author` and `/e2e-suite` when the
  toolkit is installed and the repository's own test tooling when it is not.
  Which of the two it used is printed and logged (OR-126).

### Changed

- **`vcs.work_branch` may no longer equal `vcs.default_branch`.** Orion merges
  agent work into the integration branch and a human promotes it to the
  release branch; setting them equal made Orion merge agent output straight
  into the release branch and report it as a routine merge. The configuration
  is now refused at config load, at `orion init`, and by `orion doctor`, each
  naming both values, the reason, and the remedy — including that `orion init`
  creates the integration branch when it does not exist. Repositories that
  want the old behaviour set `vcs.allow_release_branch_merges: true` (OR-115).
- Merge messages name the branch by role as well as by name — "merged into the
  integration branch develop" rather than "merged", so a misconfigured
  repository is obvious rather than plausible (OR-115).
- The fix loop's console and event-log lines now carry the agent's own
  one-line closing summary alongside the existing cost stats — what broke and
  what changed, not just turns, tokens and cost (OR-129).

### Fixed

- The rebase command Orion prints for a conflicted branch now names the base
  the pull request actually targets, taken from the forge, with
  `vcs.work_branch` as the fallback. It previously picked any branch literally
  named `develop` out of the workspace's branch list, so a repository listing
  `develop` in `protected_branches` while working on `main` was told to
  rebase onto `origin/develop` — a command that succeeds, quietly, onto
  abandoned code. The staleness and conflict messages now always name the
  same base for the same branch (OR-112).
- When neither the pull request nor the configuration names a base, Orion
  says it cannot determine one and points at `vcs.work_branch`, rather than
  printing a rebase command built on a guess (OR-112).
- A squash-merged ticket no longer leaves its worktree and local branch
  behind. With `delete_branch_on_merge` active the branch's commits are
  reachable from no remote ref, so the unpushed-commits guard warned about
  work the forge had already accepted; that guard now applies only to prunes
  without a merged-PR verdict, where it still protects genuinely unpushed
  commits (OR-122).
- A ticket closed outside Orion no longer wedges the queue. The
  `orion-working` label is the claim lock, and nothing but Orion's own close
  path cleared it — so a ticket fixed and moved to Done by hand kept it, and
  every later `orion watch` tick reported that finished ticket as "still
  running; not starting anything else". A resolved ticket is no longer
  treated as in flight, the stale lock is cleared when the watcher trips over
  it, and `orion queue` names a Done ticket that still holds one instead of
  listing it as working. Sub-tasks closed by a merged parent now give up
  their labels as well (OR-125).
- `orion watch` prints its startup banner — project, queue label, poll
  interval, job limit — before it reads a credential or makes a network call,
  so a freshly started watcher can no longer sit silent in a way
  indistinguishable from a hang (OR-128).
- An unresponsive Jira now fails with a message naming the server and how
  long Orion waited, instead of blocking a watch tick behind an error nobody
  could read. A Jira client built any other way than `NewJiraFromEnv` gets
  the same timeout rather than falling back to an unbounded one (OR-128).
- `gh pr create` and `git push` — the last two network calls on the watch
  path without a timeout — are now bounded, so a stalled credential prompt or
  dead connection cannot park the watcher indefinitely (OR-128).

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
