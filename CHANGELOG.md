# Changelog

Notable changes per release. Written for someone deciding whether to upgrade
and what to watch afterwards, so each entry says what changed **and what it
now refuses to do**.

## Unreleased

## v0.9.0

### Added

- **`orion plan` turns a tracker idea into a confirmed, scaffolded project.** A roster of
  agents chosen from the registry rather than hardcoded; a PM stage that records what is
  being built, why, and how success is measured; a database architect that recommends
  with its reasoning and waits; and question convergence bounded by a round ceiling so
  planning escalates rather than looping. Every recommendation stays a recommendation
  until a person confirms it -- nothing downstream reads an unconfirmed choice as fact.

- `orion plan` now selects the actors on a planning run from the registered
  roster plus deterministic signals in the idea itself, and prints which word
  selected each one. An idea that mentions a database puts the database
  architect on the run; an idea that names nobody says so rather than staying
  silent. Selection is a word lookup, not a model call, so the same idea always
  produces the same roster at the same cost — and registering a new actor puts
  it into planning without a code change.

- `discovery.max_rounds` bounds the question rounds that run before the
  discovery gate, defaulting to **2** and settable with
  `orion config limits discovery.max_rounds N`. The gate is "zero open
  questions"; with several agents each free to add questions it can recede
  faster than a round moves toward it, and every receding round is paid for.
  At the ceiling Orion escalates what is still open to a person — it never
  loops and never proceeds with an unanswered question. Every agent's
  questions are written into the one intent file, so `orion answer <id>`
  still resolves the whole round in one pass, and each round is logged with
  which agent added what and how many questions are left.

- A recommendation is now recorded as an artifact with two structural states, and
  only a confirmed one is readable by a later stage. An unconfirmed recommendation
  is written to `docs/recommendations/pending/<KEY>.md` and is in no agent's scope;
  it moves to `docs/recommendations/confirmed/<KEY>.md` -- and into what the
  advisors and the implementer may reason from -- only when somebody on
  `slack.merge_approvers` confirms it with a reaction on the Slack message that
  asked. The confirmation is appended to the record naming the approver and
  pointing at that message, and the ticket gets an attributed comment in both
  states, so a reader months later can tell what was proposed from what was agreed.

- The database architect now takes part in planning. `orion run <workspace> --stage database`
  reads the intent and the spec, recommends one database **with its reasoning** — what the
  requirements demand, what was rejected and why, what would make it the wrong answer — and
  records it as an unconfirmed recommendation, asking about it in Slack. Only once somebody
  confirms it does the next run design the initial schema on it, and that schema is recorded
  the same way. Neither the choice nor the schema is readable by a later stage until it is
  confirmed, so nothing gets built on a database decision nobody made.
- `orion plan` now prints the command that runs that step beside the database architect when
  the idea selects it. The step is not part of the fixed chain: a project that stores nothing
  is never billed for it.

- **Planning ends where the work queue begins.** A confirmed plan is scaffolded into the
  project and its tracker tree, handing off to the ordinary `orion watch` queue. Where
  nj-agents is installed its `/pm-plan` builds the tree; where it is not, Orion's own
  provisioning does, and the run says which path it took.

- **A plan-conformance review: is this the thing we agreed to build?** Three readers
  already look at a finished change and none of them asked that question. The review
  class reads the diff (is this code good), QA reads the acceptance criteria (does it do
  what the ticket said), and done triage reads those criteria against the diff (is it
  genuinely finished). A change can satisfy all three and still quietly build something
  other than what was agreed during planning, and that divergence is only visible to
  somebody reading the plan and the diff side by side — which, at approval time, is
  nobody. The new pass runs immediately after done triage, on the same green run and the
  same already-fetched diff, and compares the change against the **confirmed** plan
  artifacts only: the plan stage's own `plans/<slug>.plan.md` and this ticket's record in
  `docs/recommendations/confirmed/`. The pending directory beside it is deliberately not
  read — holding a change to a recommendation nobody answered would enforce a decision
  that was never made.
- **It reports and cannot block.** A divergence is frequently the implementer finding
  something better while building, so the pass never merges, never hands a ticket back,
  never moves a label, and returns nothing a caller could gate on — it puts the
  difference on the ticket and in the event log so a person decides, instead of it
  landing unremarked. A change that matches its plan gets no tracker comment at all; a
  tracker with a note on every ticket is one people stop reading.
- **Every outcome reaches the audit trail, including the ones where nothing ran.**
  "Checked and matched" and "never checked" are different facts, and the second is what
  an auditor is asking about later, so the event carries which happened, the artifacts it
  was judged against, and the divergences in their own words. A ticket with no confirmed
  plan artifact says so and costs no model call.
- **A `plan-conform` actor in the roster**, "Nadia · plan conformance reviewer" by
  default, on sonnet, configurable through the `agents` block like every other actor. Its
  own actor rather than done triage's, so its spend is its own row in the ticket's cost
  report and its findings are attributable — the two passes read the same commit and only
  one of them may hand work back.

- Tickets can now declare, at planning time, the packages, directories or files
  they expect to touch — a `Files:` line under `## Scope` in the description.
  `orion decompose` writes one on every story it creates, and the decompose
  stage prompt asks the delegated planner for the same line. The queue manager
  reads it back and will not admit two tickets that named the same ground into
  one batch, naming the overlap in the held line; a ticket already in flight
  counts, so a batch forming across passes is judged as one batch. `pick` uses
  the declaration where there is one, falls back to its area heuristic where
  there is not, and says which it used. The independence decision is
  `internal/fanout`'s, the same one `orion fan` applies to the import graph, so
  there is one implementation rather than two that drift.
- `orion decompose` now reports sibling stories that declared the same ground —
  in the preview, before anything is created, and on both stories' records. It
  states the coupling rather than resolving it: merging two items or ordering
  them with a blocking link is a judgement about the work, not about the text.
- A ticket's predicted scope is recorded beside the files its branch actually
  changed (`queue-scopes.json` in the workspace), so whether planning's
  estimates are worth anything can be judged rather than assumed.

An absent scope holds nothing back — unknown is not conflict, and most tickets
will carry no declaration. Nothing here replaces the assembly-time check that
ejects a branch which will not merge; it only makes that check fire less often.

- `orion.json` now takes an optional `toolkit` block — `repo`, `ref`, `dir`
  and a `stages` map of stage name to command — so a team can point Orion at
  its own skill repository without a Go change. Leaving the block out changes
  nothing: `repo` defaults to the nj-agents URL, `dir`/`ref` fall back to
  `delegation.nj_agents_dir` / `delegation.nj_agents_ref`, and a stage with no
  command keeps Orion's built-in prompt. An unknown stage name, both spellings
  of one stage carrying different commands, or any attempt to express ORDER
  (a list, an `order` or `sequence` key) is refused as a config error rather
  than ignored — sequencing across stages stays Orion's, per ADR 0001. A
  toolkit Orion clones itself now lands in `<ORION_HOME>/vendor/<repo-name>`
  so two toolkits cannot collide; the default still resolves to
  `vendor/nj-agents`.

- `toolkit.stages` now takes effect in the stage prompts. A project that
  declares, say, `"intent": "/speckit.specify"` gets a prompt naming its own
  command where Orion would have named nj-agents' `/capture-intent`; the
  scaffold and decompose stages resolve the same way. A partial map is
  supported — any stage with no entry keeps its built-in prompt — and a
  project that declares no `toolkit` block sees prompts identical to before,
  byte for byte. Orion still states the artifact each stage must commit and
  still owns the routing contract, whichever command fills the slot.

- A stage now fails immediately when it exits without leaving its artifact.
  The intent, spec and plan stages each owe one committed, non-empty file; a
  run that finishes without it stops there and the message names the artifact
  path, the stage and the command that was configured to produce it — so a
  wrong skill name in `toolkit.stages` is caught at the stage that carries it
  instead of surfacing several stages downstream. Which artifact a stage owes
  is decided in Go and cannot be changed from `orion.json`; stages that
  produce no single committed file (verify, review, pr, build, scaffold,
  decompose, ticket) are skipped.

- **ADR 0019 records that Orion is toolkit-agnostic** and that nj-agents is the shipped
  default rather than the only option -- so the next reader finds the decision written
  down instead of inferring it from the code.

- `orion decompose <KEY> [tasks.md]` creates the tracker tree itself, from a
  spec-kit `/speckit.tasks` task list, with no skill in the middle. One Epic,
  one Story per `[USn]` group, each task a child of its story, and a task in no
  story group hanging off the Epic; the `[P]` parallel markers, the phases, the
  dependency section and the exact file paths survive into the descriptions.
  The routing marker `orion routes` publishes is set by Orion rather than asked
  for in a prompt, so an unmarked docs ticket worked by the wrong actor stops
  being possible. The whole tree is previewed with new-versus-existing marked
  and one answer creates it; an unattended run creates nothing. A re-run links
  what a previous run made rather than duplicating it, and a run that fails
  halfway reports the item it stopped at and resumes from there.
  **Opt-in and Jira-only for now**: the `decompose` stage still runs the
  configured skill on every tracker, unchanged.

- **Every ticket in the queue keeps its row on the watch; only its status changes.** A row
  used to be an agent running in this process, so a ticket waiting on the batch, one not
  yet claimed, or one worked before a restart had no row at all -- the watch listed the
  queue at startup and then the rows vanished, leaving only the batch block. The queue is
  now read on every tick and every ticket carrying the queue label or a state label has a
  row: `queued`, `working`, `ci-wait`, `ready`, `failed`, and -- when it is a member of the
  batch on screen -- `in batch`, `landed`, `culprit` or `ejected`. A queued row neither
  spins nor draws a bar; a row an agent owns is never replaced by a tracker read; a ticket
  that leaves the queue leaves the screen on the next tick. The status line counts them:
  `1 running · 3 queued`.

### Changed

- The intent stage now leaves behind a capture with **Success measures** and
  **Open questions** sections, worded the way `internal/discovery` parses them.
  Anything the PM cannot decide has to be recorded as an open question rather
  than assumed, so the existing discovery gate blocks the later stages on it
  instead of letting an invented answer reach spec, plan, scaffold and the
  tracker tree. A re-run extends the capture that is already there; a question
  is settled in place with `[x]`, a strikethrough or an inline `Answer:`, never
  by deleting it.

- The decompose stage now hands the planner a fixed shape for every tracker
  item's description: a one-sentence summary and a two-line WHY above a
  horizontal rule, everything else (open questions, scope, grounding, tests)
  below it. A human triaging the backlog reads to the rule; an agent reads
  past it. Grounding is cited rather than quoted, and rejected alternatives
  are referenced by ADR id instead of re-argued in each ticket.

- **The test suite can run its packages in parallel.** `workspace.Home` is injectable
  rather than read from the process environment, which was the one thing preventing
  `t.Parallel` across the slowest packages. Measured at 3.7x on the work they dominate.

- **`internal/njagents` is now `internal/toolkit`.** A rename with no behaviour change,
  ahead of Orion supporting any skill repository rather than one by name.

- `orion doctor` now validates the toolkit against the skills your
  `toolkit.stages` actually name, instead of against nj-agents' own catalogue.
  A team pointing Orion at its own skill repository no longer fails the check
  for six nj-agents skills it never invokes, and a missing skill is reported
  with the stage that required it, so the message names the config line to
  change. With no `toolkit.stages` configured nothing moves: the required set
  is the same six skills, under the same `nj-agents` label.
- `CONVENTIONS.md` and `install.sh` are required only of the default
  nj-agents toolkit. A foreign toolkit shipping neither now validates as
  healthy, and `orion njagents install` explains that the toolkit ships no
  installer rather than failing on a missing file.
- `orion doctor --fix` clones the repository named in `toolkit.repo`. A
  non-default URL is confirmed before anything is fetched — a checked-in
  config naming a URL is not consent to run its code — and declining leaves
  the machine untouched, printing the `git clone` command to run by hand. A
  non-interactive run (CI) declines.

- **The two slowest test packages are roughly half as slow.** `internal/collect` and
  `internal/work` built their git fixtures per test; they are now built once and copied.
  Measured on CI: collect 351s to 189s on Windows, 22s to 11s on Linux.

- **The Windows CI leg has an honest baseline for the first time.** Every earlier figure
  measured a suite that stopped at its first failure, so the numbers the project had been
  planning against were time-to-give-up rather than run time.

- The fan-out's narration now reads like the rest of the log: each line carries
  the timestamp, ticket key and actor every other line has, the per-child lines
  are indented one level under the announcement that dispatched them, and each
  child names what it was given (its package, its question, its share of the
  cases) rather than only an index. The announcement says "5 children, 2 at a
  time" instead of "5 children (cap 2)" -- the bound is on concurrency, never on
  count -- and a landing reports its outcome once, in the verb column, instead of
  saying both "ok" and "exit 0".

- **The batch landing line says what was not tested again.** It read "landed 1 approved
  branch(es) as one, with no further CI run", which sounds like a claim about CI in
  general -- and merging to the work branch starts that branch's own checks a second
  later, so the next thing on screen appeared to contradict it. The saving is real but
  narrower, and the line now says so: the batch ref was already green and was not tested
  again, while the work branch runs its own checks as usual.

### Deprecated

- `delegation.nj_agents_dir` and `delegation.nj_agents_ref` are superseded by
  `toolkit.dir` and `toolkit.ref`. They still work; when both are set the
  `toolkit` spelling wins and the config names the older key.

### Fixed

- A project that sets a non-default `paths.plans` now gets stage prompts naming
  that directory. The plan, decompose, build and verify prompts hardcoded
  `plans/`, so on such a project the plan stage wrote where the prompt said and
  the shield's plan gate looked where the config said — and every edit after it
  was refused. Prompts and gate now resolve the plan artifact through one
  helper, so the two cannot drift.

- A release now tags the source repository for the commit it built, in the same
  run that publishes the assets, and refuses to publish if that tag cannot be
  pushed. Previously the release workflow used the dispatched tag only to name
  things — the channel, the package-manager version, the archive names — and
  created no tag at all, so a published version was unidentifiable from the
  source repo (`git log v0.8.10` failed) and every later local build reported
  itself as commits-past-the-*previous* release, because `make build` derives
  its version from `git describe --tags`. Re-running a release for a tag that
  already names the same commit is a no-op rather than a failure; one that
  names a different commit is refused rather than moved, since re-pointing a
  published tag rewrites what every existing checkout and archived build meant
  by that version.

- **A finished row draws its bar.** The bar was blank whenever there was no baseline to
  measure against, which is right for a run still going and wrong for one that has
  ended: completion is a fact rather than an estimate, and needs no baseline. A
  finished ticket with no history drew fourteen empty cells, indistinguishable from a
  row that had not started. A culprit's row still finishes red.

- **The row's columns line up.** The `/ ~4m` median was printed only when a row had one
  and sized to whatever it printed as, so a ticket with a baseline pushed the
  sparkline, the tool count and the notes right while a ticket without one did not, and
  two different medians disagreed by a character. Four rows made a ragged edge instead
  of columns. The suffix now reserves a fixed width, and gives it up entirely on a
  terminal too narrow for it.

- `orion watch`: a run past its actor's median no longer sits at a saturated bar
  that looks the same at 5 minutes as at 28. The cells past the median convert
  to overrun on a log scale of elapsed over that same median, so a long run
  still reads as long. A run with no baseline still draws no bar, and the
  median the bar measures against is still the one the row prints.

- `orion watch` now names the CI checks a ticket is waiting on — `go (ubuntu) ✓  go (windows) ⠹` — instead of only counting tickets in the footer. The row already existed but only a batch ever fed it, so an ordinary watch could not tell whether one platform had finished six minutes ago. Off a terminal the same line reaches the log, printed only when a check changes state. No extra API call: the checks come out of the read the verdict was already made from.

- **A finished ticket reaches the next batch instead of waiting forever.** The agent
  finished, QA gave its verdict, the branch was pushed — and then nothing happened. A
  ticket set `orion-ready`, and the pass that assembles batches searched only
  `orion-ci-wait`, so the integration queue's inbox was written to and never read.
  Four tickets sat for 41 minutes with no batch and no pull request; one did the same
  the day before and was rescued by hand. The two labels stay separate, because they
  mean opposite things — `ci-wait` says a machine is working and the answer is
  patience, `ready` says nothing is working and the next pass takes it — and the batch
  now reads both. The batch display returns with it: nothing was wrong with that code,
  it just lived behind the pass that never ran.

- **A red suite is no longer invisible to QA.** Orion runs the repository's own suite
  after the tests are written, and it was reporting the result to the log and to nobody
  else. QA then formed its verdict having never been told, so a ticket whose suite had
  already failed reported that every case passed. On one run that shipped a formatting
  error to a shared branch, where CI found it twenty minutes later — the same error
  Orion had already printed in the worktree. The failure output now reaches the QA
  session, which decides what it means: a failure belonging to this change is a
  finding, one that does not is something to name and explain. A green suite adds
  nothing, and a suite that could not run is not evidence of anything.

- **The recent-lines pane is on screen again.** A watch run was meant to show a bounded,
  framed window of the last few lines above the ticket rows — `recent 5 line(s)` at the
  top, `scrolls, then gone` at the bottom — and it had not appeared for two releases.
  Every part of it was still there and still tested: the frame, the labels, the height
  resolution against the terminal, the per-concurrency cap, the rule that the ticket
  rows outrank the window on a short screen. Nothing called any of it. The lines went
  straight to scrollback instead, so log output scrolled the terminal freely and there
  was no wall between the two zones.

  It was removed deliberately, on the argument that a ticket's own row now carries its
  latest tool call and the window was therefore redundant. What that missed is that the
  two show different things. A row is one ticket doing one thing right now. The window
  is what happened across the whole run — a file edited, a suite run with its duration,
  a branch pushed with its commit count, a batch merged — including actors like the
  batch itself, which has no row at all.

  `ctrl-r` still drops the cap and writes everything through, and closing a run still
  commits the visible lines to real scrollback, so nothing that was on screen is lost.

- A batch that merges now closes the tickets it merged. Every member the batch
  actually landed has its Orion labels cleared, is transitioned to Done, is
  commented with the batch's pull request URL, and has its sub-tasks closed --
  the same sequence the per-branch path has always run, now shared by both
  rather than copied. Previously a batch told only the screen and the log, so
  merged tickets stayed In Progress carrying the queue label and were collected
  again on every subsequent pass. Ejected members and the culprit are left
  untouched: their work did not land. A tracker with no Done transition still
  lets the merge stand, with a warning.

- **The watch no longer leaves the window's top border behind in scrollback.** On a real
  run the frame's top line stacked up the screen, once per redraw for a while, then a whole
  frame, then more borders. The cause was a moment with the terminal's width UNKNOWN: the
  size is re-asked once a second so a resize is noticed, and when that one poll failed --
  opening /dev/tty and forking `stty` beside five agent subprocesses -- the cache answered 0.
  At width 0 the region drew every row unclipped and counted each as one screen row; the
  terminal wrapped what the count did not, the erase moved up short, and the top of the
  block stayed behind. A failed poll now keeps the last known size, and the block is never
  drawn at an unknown width. Reproduced and pinned by a test that replays a watch against a
  small terminal emulator and counts the borders on screen: twelve before, one after.

- **A batch is asked about once, not on every pass.** The approval record was forgotten on
  the way out, and since every batch uses the same ref name the next pass found no record,
  decided nobody had been asked, and posted the request again -- four asks for two batches,
  each already carrying a green tick, and every duplicate a message whose reaction may be
  read from the wrong one. The record is kept now and compared by MEMBERS, so a genuinely
  different set of tickets on the same ref still asks afresh while the same batch stays quiet.

- **The batch block matches the OR-246 mockup, and fits the screen.** During a batch the
  foot of the watch display is now one CI block: a titled rule carrying the verdict and the
  measure (`── CI ─── running ──── 4m12s / ~11m median ──`), a chain with one cell per
  member and ONE fill sweeping across them (`┝━━ OR-223 ━━┿━━ OR-224 ──┿── OR-242 ──┥`),
  the jobs three to a row with a glyph each, and a tally (`4 of 6 green · waiting on 2`;
  when red, the failing job and the failing test are named). The three bars that used to
  share the batch line -- membership, checks and time -- overflowed 76 columns and clipped
  the elapsed, the median and the run number with `…`, and the check line clipped checks
  four to six. Nothing in the block clips at 76 columns now, in any phase.

- **The pinned block can no longer outgrow the terminal and repeat.** Nothing capped the
  region against the terminal's height; only the frozen window yielded. Once the rows,
  the status line and the batch block together exceeded the screen, the erase's cursor-up
  clamped at the top row, the lines that had scrolled into history were missed, and the
  next redraw painted them again -- the "recent lines came several times" report. The
  block is now trimmed to the terminal's height, from the top, so the status line and the
  batch stay nearest the cursor.

- **The status line fits inside its rule.** Single spaces around the separators, `$12.40`
  rather than `$12.40 this session`, the hint at the right edge, and no CI count while a
  batch is on screen (the CI block owns it). At two spaces a side it ran to 96 cells and
  wrapped at 76 columns.

- **The frozen window's frame is the mockup's:** `┌ ┐ └ ┘` corners, `│` side bars, and
  `recent 5 lines` rather than `5 line(s)`. The isolation tree puts the verdict before
  the set (`├─ ✓ [223 224]`), and the run estimate rounds up per split so a three-member
  search no longer prints `run 4 of ~3`.

- **The batch CI baseline no longer counts batches that ran no CI.** A pass that assembled
  nothing still wrote its note -- `0 run(s) in 1s` -- and eight of those outvoted every real
  run, so the CI rule read `median 1s` over a seven-minute run and said `running long` from
  its first second. A batch is a sample only when it ran; the elapsed of one that did not
  measures assembly, not the run.

- **A red batch no longer restarts CI every pass.** When a batch went red, the search for
  the culprit pushed its first split and read its checks at once; nothing had reported, and
  that silence was returned as an error. The batch was declared incomplete, its ref deleted,
  a `landed nothing … in 20s` note written, and the next pass assembled it again -- a fresh
  merge commit, a fresh CI run -- forever. Seen as three runs in flight on `orion/batch` at
  once, ninety seconds apart. An isolation run now waits for its check, polling every thirty
  seconds for up to thirty minutes, and says how long it waited if it gives up. The first
  test of a batch is unchanged: it still returns at once and resumes on the next pass.

- **An ejected member's row names the conflict while the batch tests.** `returns to the
  queue` alone could not tell a real clash from a dependency-order problem; the file is
  what a person acts on, and the assembling view already showed it.

- **A batch culprit now enters the CI fix loop.** When isolation convicted a member, the
  batch path set its verdict and returned; the per-ticket path that relabels
  `orion-failed`, comments where and why it failed, notifies, and -- with `auto_fix` --
  dispatches the fix agent was never reached. So the culprit stayed `orion-ready`, was
  collected into the next batch, and was convicted again, while its row said
  `fix round 1 of 3` about a loop that did not exist. A convicted member now takes the same
  road a per-branch red build takes: the pull request it cites is the batch's, because that
  is where the failure is, and the branch it fixes is its own, because that is where the
  fault is.

- **A batch resumed after a restart is on screen.** Restarting the watch with a batch in
  CI showed nothing but the log line `ci orion/batch: 3 branch(es), 2m0s elapsed` -- no
  rule, no status line, no chain, no jobs. The region draws the batch it was told about,
  and only a batch assembled by the running process ever told it. A resumed batch now
  appears with every member riding, the elapsed counted from the record's `testing_since`,
  and the jobs as they report.

- **A red batch is isolated, not rebuilt.** When a batch went red its record was cleared,
  so the next pass assembled the same set again -- fresh merge commits, a fresh CI run,
  six more minutes to learn the same verdict -- and did so on every pass, never once
  isolating. Isolation only ever began if the status read after the re-push happened to
  return the previous run's failure, a race. The red verdict is now recorded, and the next
  pass over the same members on the same base assembles them and begins the search at
  once, with the whole set's run already counted. A different set, or a base that moved,
  is tested from scratch as before.

- **A closed ticket with a stale `orion-ready` label is no longer assembled into a batch.**
  The pass query found tickets by label alone, so two tickets closed by hand a day earlier
  were offered to the batch and their old branches merged into the ref -- work that had
  already landed, merged again. The query now excludes Done.

- **A passing verdict is grounded in the workflow runs, not the check rollup.** Right after
  a push the pull request's rollup holds only the checks GitHub has registered so far -- the
  fast analysis jobs report success inside a minute while the slow build jobs do not yet
  exist as checks -- so two successes and nothing running read as "2 check(s) passed". A
  batch was declared green on that, approved, and landed on develop two minutes before its
  run had been queued. A pass now also requires that a workflow run exists for the head
  commit and that none is queued or in progress; until then the verdict is pending and says
  which run it is waiting on.

- **The window's frame no longer strands when an agent runs.** The supervisor wrote to
  the terminal underneath the live region -- the agent subprocess's stderr, the run header,
  `orion:` warnings, fan-out announcements -- and the region erases by the rows it drew, so
  every such write moved the cursor without the count knowing and left the frame's top
  border behind, once per write. Everything the process says now goes through one console
  the region owns; a subprocess's stderr goes to its log and tail while the region is up,
  and to the terminal as before when it is not.

- **An agent can no longer wait forever for something that will never arrive.** A QA
  session re-ran a suite Orion had already run, backgrounded it, and polled for twenty
  minutes -- so the red verdict that should have handed its ticket back to the implementer
  was never produced, and the ticket sat until the operator stopped the watch. Three
  changes: QA is told plainly not to re-run a suite Orion has already run, since the
  result is in its prompt; every stage prompt now states that the run is headless, where a
  backgrounded command is never announced and `ScheduleWakeup` does nothing; and the
  breaker bounds waiting at twelve consecutive polls with nothing else in between. Waiting
  for a genuinely running command is unchanged -- the budget sits well above what a real
  wait costs.

- **The suite stopped failing on macOS for reasons that had nothing to do with the change.**
  `go test ./...` runs packages in parallel up to GOMAXPROCS, and this suite is not
  CPU-bound: sixteen packages spawn real subprocesses, so the default put several of them
  on the runner at once and they starved each other. The tests that lost were the ones with
  a clock in them -- a wall-clock kill waiting for a grandchild to report its pid, a watch
  loop expected to complete two passes at a millisecond interval -- and each failure named
  a different test, which is the signature of contention rather than of a broken assertion.
  macOS failed about two runs in five while Linux passed, because its runner has three
  cores to Linux's four. Package parallelism is now capped at two (`TEST_PARALLEL_PACKAGES`
  to override), on every platform. No assertion was weakened.

- **A killed test run no longer leaves processes behind.** Around twenty test sites spawned
  a long-lived binary -- the built orion binary, `go build`, `go test` -- with a plain
  `exec.Command`, so when the parent `go test` was killed by a timeout, a ctrl-c or a
  cancelled CI job, the child was reparented to init and ran forever. They accumulated
  until the machine was unusable: 619 orphans and a load average of 256 in one evening,
  cleared four times and back each time, with every measurement taken in between worthless.
  Those spawns now go through `internal/testproc`, which puts each child in its own process
  group and kills that group when the test ends -- including when it fails or panics.
  Orion's own agent path was already correct; this was the test path only.

- **The Windows CI leg gates again, and the tests it was hiding pass.** The `checks (windows)`
  step had no `shell:`, so GitHub ran it under PowerShell, which fails a step only on the
  LAST command's exit code -- `go build` succeeded, so `go test`'s failure was discarded and
  the job reported success over 110+ failing tests across 14 packages, for as long as the leg
  has existed. It now runs under bash with `set -euo pipefail`, and three real causes behind
  those failures are fixed: a virtualenv's interpreter is at `Scripts/python.exe` on Windows
  rather than `bin/python`; a `.sh` cannot be exec'd directly there and is invoked through
  bash; and six tests asserting POSIX permission bits now skip on Windows, where every file
  reports 0666 and the assertion tests the operating system rather than the code.

- **A batch culprit's fix agent is given the CI log again.** The fix run looks a failing
  log up by ref, and it was handed the ticket's own branch -- but a culprit failed on
  `orion/batch`, tested alongside its siblings, so its own branch's last run was stale,
  green or absent. Nothing was found, and the agent was handed the sentence "convicted by
  the batch's isolation" instead of a failure: it spent two attempts on one ticket
  reporting, accurately, that it could not see the actual CI log. The log is now read from
  the ref that actually went red, and when no log is reachable the prompt says so rather
  than quietly passing off a conviction as the whole failure.

- **A landed batch cleans up after itself.** Twenty-five branches had accumulated on the
  remote, including `orion/batch-iso-*` refs left over from finished bisections -- pure
  scratch that nothing would ever read again. Two leaks: every ephemeral ref was dropped
  locally but never deleted from the forge, although the search PUSHES each one to test it;
  and a member's branch survived its own landing, because the batch path closed the ticket
  without the prune the per-branch path has always done. Both are fixed, driven by what the
  batch actually landed rather than by ancestry -- a batched branch merges through a merge
  commit, so it is never an ancestor of the work branch and deleting on that signal would
  destroy work.

- A working agent no longer prints nothing between "started" and its verdict.
  Removing the live region left the console silent at default verbosity, so a
  long run and a hung one looked identical. Each ticket now prints a throttled
  progress line -- at most one every 30 seconds -- naming how long it has been
  working and what it last did. `--verbose` still prints the full per-tool
  transcript, and a run that genuinely does nothing still prints nothing.

- **The Windows CI leg is a real gate again.** It had been reporting success while
  discarding its own test results, so failures accumulated unseen for the life of the
  leg.

- **`orion collect` no longer leaks a file handle on every pass.** The batch context
  opened the event log before it could know whether batch integration was enabled, and
  the close was inside the enabled branch -- so with batching off, which is the default,
  every collect pass leaked a descriptor. Harmless on Linux, where a file can be
  unlinked while open; on Windows it made the file undeletable.

- **Windows: a running agent's claim could be stolen out from under it.** The liveness
  check used a POSIX signal that Windows has no equivalent of, so every live process
  read as dead and a watcher was free to take its ticket mid-run.
- **Windows: killing a run left everything it had started still running** -- the agent's
  tests, dev servers and containers survived it -- and made timeouts expire without
  taking effect.
- **Windows: concurrent writes to one file could fail outright** rather than waiting
  their turn.
- **Windows: `orion doctor` never recognised its own hooks**, so a correctly configured
  machine was told none were wired.

- **Windows: a local toolkit path was mangled before cloning.** A repository path was cut
  at its drive letter, so the clone was nested a dozen directories deep under `vendor/`
  and failed.
- **Windows: a timed-out suite could outlive its own deadline**, waiting on a process the
  kill could not reach.

- **Windows: `gh` and `git` calls could outlive the timeout that bounded them.** Anything
  they spawned -- a credential helper, a pager -- held the call open past its deadline.
- **Windows: the agent prompt could not find a virtualenv's interpreter**, because it
  looked only where a POSIX virtualenv keeps one.
- **Repository paths in tracker comments and reports are slash-separated everywhere**, so
  a path written on Windows still reads correctly in Jira.

- **Windows: a release could be tagged by an identity nobody can be reached at.** Git
  invents one from the machine name when none is configured, so the check now asks for a
  configured name and email and refuses without them.

- **A ticket convicted by a batch is no longer put straight back into the next one.**
  Failing it cleared only one of the two queue states it could be in, so a culprit kept
  both "failed" and "ready" and was re-assembled every tick -- each time into a batch
  that could only fail again.

- **The watch log gives every actor its own colour.** Seven actors were sharing four
  colours, so `orion`, QA and the implementer could not be told apart at a glance.
- **A long run keeps its identity columns.** Repeated-identity suppression was written
  for a burst of lines; with a progress line every thirty seconds it blanked the actor
  column for minutes at a time.

- **A very large `max_concurrent_tickets` crashed the watcher at startup.** The result
  channel was sized from that number without a bound, so a big enough value overflowed
  and the allocation was refused. The cap itself is unchanged; only the internal buffer
  is bounded.

### Security

- **CI declares read-only permissions explicitly** rather than inheriting the
  repository default, so widening that default later cannot silently widen what CI can do.

## v0.8.11

### Added

- **QA writes its tests in parallel.** A ticket with fifty cases had one agent writing
  fifty files one after another, in a single session, because the whole case list went
  to a single run. The derived cases are now split across several authors that write at
  the same time — `orion config limits qa.author_agents`, five by default. Cases are
  independent by construction, so the split is by case group rather than by file.
  `limits.max_concurrent_children` is still the hard ceiling, and a fan that cannot run
  falls back to the single-agent path with the same coverage: the tests are the point,
  and the fan is only ever an optimisation.
- **Orion runs the test suite itself, as a process.** Until now it never invoked a test
  runner at all — it described one in the prompt and left the agent to run it, which
  handed the agent three decisions it should not have had: what to run, whether to run
  it, and how to report the result. A stage could go green because an agent ran a
  narrower subset than it claimed. Orion now detects the repository's own suite, runs it
  under a wall clock in its own process group, and reports pass or fail from the exit
  code. Concurrency goes to the runner's own flag (`go test -p`), settable with
  `orion config limits qa.exec_procs`, because the toolchain already parallelises better
  than a fan of agents could.
- **The live region shows the fan while it happens.** A row that said only `qa` while
  five subagents wrote was the display lying by omission. It now carries
  `authoring x5`, then `running the suite`, so a long stage says which part of itself is
  slow.

### Changed

- **Test authoring fans by case group, not by Go package.** Fan-out has been
  package-scoped since ADR 0016, for two reasons that hold for implementation and not
  for tests: builds are not isolated, and signatures are still moving. Test files define
  no APIs each other imports, and QA writes against an implementation that is already
  committed. Nothing compiles until every author has stopped, which is what keeps the
  first hazard out of reach. Implementation fan-out is unchanged.

### Fixed

- **A hung test suite is killed on Orion's own deadline.** `cmd.Wait` returns when the
  output pipes close, not when the child exits, and a grandchild inherits those pipes —
  so a suite whose runner was killed could hold the timeout open for as long as its
  descendants kept running. The whole process group is now signalled the moment the
  deadline fires. Found by a test that expected a 300ms timeout and measured 60 seconds.
  Windows reaps only the direct child, which is a real gap stated in the code rather
  than papered over.

## v0.8.10

### Added

- **A database architect actor, reachable three ways.** Normalised schema
  design, migration review, indexing and query plans now belong to their own
  actor rather than to the implementer, on the argument that gave QA its own
  actor: a schema decision is inherited by the next twenty tickets and is
  expensive to reverse once there is data in it, and an implementer optimising
  for "make this ticket pass" is the wrong incentive for it. Its runs get their
  own row in the cost report.
  - **As an advisor**, when an implementer stops on a data-model question. The
    router now classifies questions as technical, product *or* data, and the
    new advisor reasons only from the schema, the migrations and the ORM model
    definitions — not from `spec.md` or `intent.md`, for the same reason the
    architect is deliberately not handed `intent.md`.
  - **As a pipeline stage**, after the change is committed and *before* QA, when
    the ticket actually touches data. Whether it does is decided for free from
    the paths in the diff and the ticket's own markers — no model call — so a
    ticket with no schema in it pays nothing. Before QA on purpose: a schema
    finding forces a change to the schema, and QA's tests are written against
    the schema as it stands when QA runs. Like QA it reports and never blocks;
    `dba.max_rounds` bounds the findings-fix-review exchange.
  - **As a command**, `orion dba [KEY] ["this query got slow"]`, which works
    with no ticket at all — a performance complaint usually arrives before
    anybody writes one down.
- **`orion routes` now routes data tickets to it.** `database`, `schema`,
  `migration`, `sql`, `index`, `query` and their siblings reach the database
  architect. That is the same word list the pipeline stage triggers on, so a
  planner who learned the marker has learned both.

- **The region names each CI check.** `N in CI` counts tickets waiting; during a batch
  three tickets share one run, so the count says nothing about what the run is doing.
  A row under the batch now names each check and its state —
  `go (ubuntu) ✓  go (macos) ⠹  go (windows) ⠹` — so nine minutes of "still running"
  shows which platform is actually holding it up. The data was already fetched and
  discarded; it costs no extra call.
- **The frozen window is bounded by a labelled frame.** Three zones share the screen
  and exactly one scrolls, but nothing said so. `recent N line(s)` above and
  `scrolls, then gone` below make the boundary visible and explain a line that
  vanishes off the top.
- **An ejected member keeps its row while the batch is testing.** It is out of that CI
  run, not out of the picture, and dropping it left a batch of four naming three
  members with nothing accounting for the fourth.
- **The ticket rows pair two to a line while the batch is testing.** That is the phase
  where a row each says least — one run covers every member — and the space pays for
  the batch block above it. Every other phase keeps one row per ticket, and a terminal
  under 92 columns never pairs.

- Claiming a ticket now assigns it, so a board's assignee column names whoever
  is holding it instead of showing an unassigned ticket moving itself to In
  Progress. The assignee is the account Orion's Jira credentials belong to: a
  bot account where one exists, the operator otherwise — nothing to configure
  and no new account needed. The assignment is best-effort and can never fail a
  run; a tracker that refuses it costs one warning line. Nothing unassigns on
  release, so a finished ticket keeps the record of who did the work and a
  failed one keeps a name to go to.

- `status`, `doctor`, `watch`, `collect`, `work` and `init` now print one
  yellow `update` line when a newer release exists, naming the version gap
  and the upgrade command for how this machine installed Orion (`brew`,
  `scoop`, or the release page when it cannot tell). Until now an installed
  binary that was months old looked exactly like a current one, so a bug that
  had already been fixed and released was still there to be debugged.
  The answer is cached for 24 hours under `~/.orion/state` and refreshed in
  the background, so no command waits on the network and an offline machine
  behaves exactly as before — same output, same exit codes. The notice never
  appears in hook mode, off a terminal, or when `CI` is set, and
  `ORION_NO_UPDATE_CHECK=1` (environment or `~/.orion/config.env`) silences
  it for good.

### Changed

- The batch bar labels its number as a **median**. `/ ~11m` alone reads as an estimate
  of when the run will finish, which is the prediction the bar deliberately refuses to
  make.

- **An agent runs the tests for the packages it touched, not the whole suite.** The
  prompt asked for the full suite before finishing, and named `./scripts/test.sh` as
  "the same script CI runs" — which is exactly why running it again was waste. CI runs
  it on three platforms for every push; running it on the critical path buys a signal
  that is already coming, at model rates, while holding a job slot. One ticket spent
  37 of its 58 minutes on four full-suite runs, two of which hit Go's per-package
  timeout. Package-scoped runs in that same log took 22 to 25 seconds.
- **The test suite builds its git fixture once per package rather than once per test.**
  `internal/work` was creating a repository per test — eight git subprocesses, about a
  second — for 106 tests. Profiling showed 1.8% CPU: the tests were not computing, they
  were waiting on process spawn. 435s to 355s, and it is the package whose length used
  to tip the whole suite past the timeout.

### Fixed

- A failed release gate now says what failed. `scripts/release.sh` ended its
  gate in `go test ./... >/dev/null`, so the one step likely to fail
  non-obviously was the one whose output was discarded, and a release that
  stopped with the promotion already merged reported only `exit status 1`.
  The gate now names which of its four steps failed and prints that step's
  output, and `orion release ship` carries the failing output into the event
  log so it survives the terminal. A successful run stays as quiet as before.
- The release gate runs `go test -count=1`, so a green gate is a green gate
  from now rather than possibly a cached one from an earlier tree.

- **A batch waiting on CI now resumes instead of stalling the queue forever.**
  `resumeBatch` asked `resumable()` before it reached its own testing branch —
  and `resumable()` answers "may this *proof* be used to merge?", so it requires
  a validated status by construction. A batch whose CI was still running failed
  that question, had its record cleared, and was reassembled. Every tick. The
  testing-resume path was unreachable from the day it was written, which
  defeated the whole of OR-251.
- **A batch can be cut when a previous one left its worktree behind.** `MergeInto`
  checks the ref out in a worktree, and a batch that parks to wait for CI returns
  before `DropRef` by design — so the worktree outlives the tick and git refuses
  to force-update the branch it holds: `cannot force update the branch
  'orion/batch' used by worktree at …`. Observed looping every minute overnight,
  landing nothing. `CutRef` releases that worktree and prunes the records first.
- The prune is not tidiness: an operator who deletes the worktree *directory* by
  hand — which is what a person does when this wedges — leaves git still
  believing the branch is checked out, so the fatal survives the cleanup meant
  to fix it.
- `CutRef`'s doc claimed the ref was "created DETACHED from any worktree, so
  nothing has it checked out". `MergeInto` made that false and the comment
  outlived it. It is true again because the code now makes it true.
- **A batch that landed nothing no longer reports a cost in green.** `the batch
  cost 0 CI runs for 2 branchs, in 1s` printed with a tick once a minute all
  night while `develop` never moved. Zero runs in one second is the shape of a
  cycle that did no work. The event note is still emitted either way — a batch
  that landed nothing still happened, and the dashboard counts its runs.
- `plural()` said "2 branchs" in every batch line this repository has ever
  printed.

- Found by the first unattended overnight run of v0.8.9, not by a test. Second
  defect a single live run has exposed, after OR-258 — both of them in the
  batch integration path, and neither reachable from a green suite.

- **A repeated line is no longer swallowed on a long watch.** The console collapses a
  run of identical lines to one plus a count, and it decided "identical" by comparing
  the destination by pointer. A writer allocated where a finished run's writer used to
  live compared equal to the stale one, so the new run's first line was counted as a
  repeat of a line belonging to a run that had already ended — and counted, not
  printed. It was invisible when it happened, and it depended on the allocator, so it
  showed up as a test that passed locally and failed about one run in three on CI.
  It also means a `orion watch` fault line could be silently withheld, which is the
  shape of a release gate that reported only `exit status 1`.

- **The circuit breaker no longer fires in an interactive session.** It bounds an
  unattended agent — loop detection, failure budgets, tool-call and wall-clock
  ceilings — and none of that describes a person at a keyboard, who can stop
  typing. The hook gated only on finding an Orion project root, so every session
  in an Orion repo was breakered, human or agent. The cost was not merely noise:
  a trip commits, so that a run's work survives the run, and an interactive
  session past the ninety-minute ceiling had seven files committed to `develop`
  as an unverified snapshot and every subsequent call refused. A chat session is
  not a run. The breaker now arms only inside a supervised run, and says once
  that it did not: `not a supervised run (ORION_WORKSPACE unset); breaker
  inactive`. Set `ORION_BREAKER_FORCE=1` to arm it anyway.

  `ORION_WORKSPACE` is exported by the supervisor into every agent run and by
  nothing else, and `orion explore` already reads it as the answer to "am I
  supervised?" — so this reuses the existing signal rather than adding a second
  one that could disagree with it.

  Nothing is relaxed inside a run, and the scope change is deliberately limited
  to the breaker: `gate` (dangerous shell commands) and `shield` (an agent
  editing its own guardrails, or weakening the test that defines a fix) guard
  against anyone holding the tool and stay armed everywhere.

- **A ticket interrupted mid-agent is released, and resumes where it stopped.** The
  queue excludes `orion-working` so two watchers cannot claim the same ticket, but
  nothing distinguished "in flight" from "the process holding this is gone" — so a
  run killed mid-agent left the label on forever and no watcher would pick it up
  again. A claim now records its holder's process and a heartbeat, and is released
  only when both say the holder is gone: a live process and a missing record both
  read as running, so a working agent is never robbed of its ticket.
- **Re-claiming a ticket no longer starts it over.** A re-claimed ticket was given
  the next free branch name — `-2`, `-3`, `-4` — and began from nothing. One ticket
  accumulated four worktrees this way, the last holding an hour of uncommitted work
  that the next attempt would have ignored. A resume now reattaches to the branch the
  interrupted run was on. Fresh names are still cut for a genuine retry, which must
  not land on a failed attempt or rewrite a branch someone is reviewing.
- **What a killed run was holding is committed before the resume touches it.** An
  uncommitted change blocks the branch's next rebase, and work that exists only in a
  working tree cannot be read, resumed or dropped by anyone. It is committed as an
  unverified snapshot, in those words: nothing tested it and nothing reviewed it.

- **A missing baseline now says so instead of rendering as blank space.** The live
  region drew fourteen empty columns whenever an actor had no median, with nothing
  beside them explaining why. Drawing no bar is right — inventing a baseline is what
  OR-250 forbids — but drawing nothing silently is not: a blank where every other row
  has a bar reads as a display that was never built, and the batch view was believed
  unimplemented on that evidence months after it shipped. The row now says
  `no baseline yet`, in words, and the bar still draws nothing.

### Security

- **The database architect can never reach a production database, and never
  runs a migration.** It connects only to the explicit non-production DSN in
  `dba.non_prod_dsn` and to nothing else — never one inferred from the
  environment, a compose file, or anything else lying around. With no DSN
  configured it reviews the schema and migrations as text and says in its
  report that is what it did. A DSN naming itself production is refused rather
  than used, and every prompt that can reach a database forbids running a
  migration or any statement that writes: it proposes, and a person applies.

## v0.8.9

### Added

- `orion release ship vX.Y.Z` cuts a release. It refuses on a dirty tree, red
  CI, an empty `main..develop` delta or the wrong branch — naming which —
  prints the exact commit list that would ship, opens the promotion pull
  request, waits for its checks, asks for approval in Slack naming the version
  and everything in it, merges on approval, then tags, builds and publishes.
  Every step, including every refusal, is an attributed event in the log — the
  one exception being a repository with no Orion binding, which still ships, on
  no log, and says so.
- `orion release ship vX.Y.Z --beta` cuts a `vX.Y.Z-beta.N` prerelease from the
  work branch, numbered from the existing tags. It is marked prerelease on
  GitHub and updates **neither the Homebrew tap nor the Scoop bucket**, so
  `brew install navjyotnishant/tap/orion` still installs the latest production
  build and `brew upgrade` can never hand a beta to a stable user.
- `orion release ship vX.Y.Z --dry-run` prints the ship list and the preflight
  result and stops.
- `scripts/release.sh` (and `make release`) take `--beta` / `BETA=1` for the
  same prerelease channel, and now name the channel as well as the branch when
  refusing a mismatch.

- `orion config limits` now reads and writes the two fix-round ceilings by
  qualified name — `orion config limits qa.max_rounds 3` and
  `orion config limits ci.max_fix_attempts 3` — listing each with its effective
  value and whether it came from `orion.json` or the shipped default, alongside
  the nine keys it already covered. Neither is clamped: a value above 5 is
  confirmed rather than refused. Nothing in the `limits` block moved or was
  renamed, and a project whose `orion.json` has no `qa` or `ci` block gets one
  written rather than being told to add it by hand.
- Both keys now appear in `templates/orion.json` and in the `docs/USAGE.md`
  guardrails table, each with the cost of raising it stated where it is set.
  `ci.max_fix_attempts` was already configurable and `qa.max_rounds` had been
  for months; neither was written by `orion init` or mentioned anywhere, so
  both were effectively invisible.

- **`orion explore` takes several questions and asks them all at once.** They
  run as concurrent subagents, capped by `limits.max_concurrent_children`, so
  asking four costs about what asking one costs. The implementer's exploration
  phase now emits questions instead of running greps: roughly 43% of its tool
  calls were reading the repository, and everything it read stayed in its
  context and was re-sent on every subsequent turn.
- The offer in the prompt changed shape with it. Told it may ask "ONE narrow
  question", an agent asks at most one and greps for the rest — a question at a
  time serialises the run behind a subagent it does nothing while waiting for,
  so reading for itself is genuinely faster. **The cheap path has to also be the
  fast one, or it is not taken.**
- One question still behaves exactly as it always did, including printing no
  roster: a single explore is not a fan-out, and noise on the common path costs
  the batch case too, because a reader who learns to skip these lines skips the
  ones that matter.
- One child failing does not discard its siblings' answers, and every question
  is recorded whether it was answered or not. A question that failed is a
  question that was paid for.

- `orion fan <plan.json>` writes several independent Go packages at once, one
  subagent per package. The implementer proposes the assignment; Orion checks it
  deterministically — no package assigned twice, the width within
  `limits.max_concurrent_children`, and no import edge between the packages
  assigned, from `go list` — and any failure runs the work serially instead.
  Most changes will be refused, on purpose: a change that runs down a layer is
  one bucket and is not separable.
- The subagents can read and edit and have no shell, so nothing is built or
  tested until they have all landed and the parent run verifies once. That is
  enforced by the tool list rather than asked for in a prompt.
- A fan-out now reports each child as it lands rather than going quiet until
  the last one finishes.

See `docs/decisions/0016-fan-implementation-by-go-package.md` for why the unit
is the package rather than the file.

- **A queue manager decides what enters the queue, by rule.** Keeping the queue
  correct was a person's job: on 2026-08-30 it meant labelling tickets through
  the Jira API roughly fifteen times, holding four that would have wasted agent
  time, and pulling one out after its run went wrong. None of those decisions
  needed judgement; all of them needed someone awake.
- **Deterministic, and that is the point.** Every rule returns the same answer
  every time it is asked. A queue that orders differently on identical input is
  a bug nobody can reproduce, which is why this is Orion and not an agent — and
  why admitting and evicting belongs to the layer that sequences stages
  (ADR 0001).
- **Supersession is read from both sides of the link.** A ticket is not
  admitted when it declares itself superseded *or* when another ticket in the
  set declares it so. Blocking needs only one side; supersession does not,
  because the link is written once, on the newer ticket, by whoever drafts it —
  so the fact that the older one is dead can live entirely on the newer one's
  record. OR-231 and OR-235 were both written that way.
- **An evictions ledger**, keyed by ticket, carrying the reason, the rule and
  the run. A ticket evicted twice **escalates to a person** instead of being
  retried — the same two-attempt cap the orchestration conventions put on fix
  rounds, and the third attempt at something that has failed the same way twice
  spends money to learn nothing.
- **Nothing rots at the bottom of the queue.** A ticket not admitted for N
  consecutive passes is reported by name. The count measures *neglect*, so it
  clears on admission rather than on completion.
- Decisions are reported **grouped by reason, not by ticket**: six tickets held
  on one missing milestone is one fact, and six lines saying so is how the one
  line that matters gets buried. A pass that changed nothing prints nothing.
- `collect.FixRounds` is exported so the manager can read a ticket's spent
  rounds from another package.

- **A finished run is triaged before anyone is asked to approve it.** Once a
  ticket's checks go green, `orion collect` reads the run against its diff and
  answers one question: is this genuinely done, or does it only look done? On
  2026-08-30 three green pull requests were evidence of nothing, and each was
  caught by a person reading the run rather than the status.
- **Four checks, three of them free.** Did QA actually reach a verdict, or did
  the stage fail and the change go to review unverified? Do the tests QA wrote
  appear in the diff, or only in the worktree it left behind? Do the branch's
  new or changed Go tests still pass at `-count=2`? Those are rules over
  evidence that already exists and cost nothing. Only when they come back clean
  is a model asked the one question no rule expresses — whether the ticket's
  acceptance criteria correspond to anything in the diff.
- **NOT DONE hands the ticket back; it never blocks.** The ticket leaves
  `orion-ci-wait` and becomes visible again with the specific evidence on it,
  the branch is kept, and nobody is asked to approve. Nothing here merges,
  approves or edits a change — it reports, and the answer is DONE or NOT DONE,
  never a score.
- **It runs once per commit, not once per poll.** A ticket waiting on an
  approval does not re-run a test suite or re-ask a model on every tick; a
  branch that somebody pushes to is triaged afresh.
- **A new roster entry, `done-triage`**, so the pass appears in the ticket's own
  cost report rather than as spend nothing accounts for. Absent a `claude` CLI
  the mechanical checks still run and say the intent question went unasked.

- **`ctrl-o` collapses the live region, and expands it again.** OR-246 removed
  the ceiling on `limits.max_concurrent_tickets` and asks above ten instead; at
  ten the pinned rows are taller than many terminals and there was no way to
  shrink them. Collapsing keeps the header and the batch line and drops the
  per-ticket rows, which are the part that grows with the cap. Expanded stays
  the default — you should never have to ask to see what is running.
- The collapsed region **says how many runs it is hiding**, and how to get them
  back. Someone who collapsed it ten minutes ago and returns to a quiet screen
  must not read it as finished.
- The status line names the key that applies right now, and only when there is
  something to apply it to.

- **The batch reports what it cost in minutes, not only in CI runs.** Run
  count is the model ADR 0015 argued in; elapsed is what an operator feels —
  the time between "the agents finished" and "the work is on develop". The
  timer covers assembly, testing and every isolation round, and is recorded
  even when the batch fails, because a batch that died after twenty minutes
  cost twenty minutes.
- **A baseline to read it against, taken from this repository's own history.**
  Every past per-branch landing is already in `events.jsonl` as a push
  followed by a merge; their median is what the old path actually cost here,
  on this machine, with this CI. Not a simulation: an invented number that
  flatters the feature is worse than no number.
- Measured from the FIRST push per branch, not the last, so a rebase and its
  re-run stay in the baseline. Counting from the last would delete the exact
  cost batching claims to remove.
- Batch landings emit no per-key push, so they contribute no samples and
  cannot poison the baseline they are measured against.
- Under three past landings it says **"no baseline yet"** and names the count,
  rather than printing a median of one.
- **A slower batch is reported as slower.** A measurement that only speaks up
  when it flatters the feature is not a measurement.

- `orion config limits qa.verdict_minutes N` sets the floor, alongside
  `qa.max_rounds` and `ci.max_fix_attempts`. Its stated hazard is unusual:
  too LOW is the expensive direction, because a re-ask killed by the clock
  buys nothing at all.

- **`orion-ready`: the integration queue's inbox.** A third state beside
  `orion-working` and `orion-ci-wait`, and deliberately not either of them —
  working means an agent holds the job slot, ci-wait means a machine is
  building, and ready means nothing is running and the next batch takes it.
- **A merge precondition, from ADR 0017.** The base is stamped before the
  batch is tested and re-read before it merges. If it moved in between, the
  green result was proven against a tree that no longer exists, and the batch
  is reassembled and retested rather than merged on a proof that no longer
  applies.
- **An approval gate for the batch**, asked only after checks pass. A "not
  yet" leaves the members unmerged and unblamed, and the next pass asks again.

- **A batch asks once, for the set.** The members were tested together and
  merge together, so approving them one at a time would be four questions with
  one possible answer. Same channel, same reactions, same allowlist and same
  two-pass shape the per-branch path already uses — only the subject differs.
- **The proof is written down** (`batch-state.json`, beside the approval
  requests). Without it, every tick while an approver looked would reassemble
  the same members, force-push a new merge commit, replace the pull request
  they were reading, and buy another CI run to re-prove what was already
  proved. A record is reused only when the status, work branch, member set and
  base commit are all unchanged; anything else reassembles.
- **The base is re-read between approval and merge.** Approval is a
  human-length gap, which is exactly the gap ADR 0017's precondition exists
  for.

- **`orion dashboard`: is the coding queue outrunning the integration queue?**
  Agents scale horizontally; integration is one operation at a time by
  construction. So the number that matters is not how fast agents code, it is
  whether coding produces work faster than integration can absorb it. Nothing
  reported that.
- **READY is the backpressure signal**, and it is shown first among the
  integration numbers as queue depth. It grows before anything else looks
  wrong.
- **A drain estimate**: how long the waiting work would take at the rate this
  repository has actually integrated, using the members-per-batch it has seen
  rather than the cap it is allowed. Queue depth is a fact; the drain estimate
  is what makes it a decision.
- **Completed and merged are counted separately.** The gap between work
  finished and work landed IS the bottleneck moving to integration.
- **CI runs saved is signed.** A batch that cost more runs than the path it
  replaced is reported as having COST them. A metric that can only show a
  saving is advertising.
- Everything is derived from `events.jsonl`. Nothing is estimated, and nothing
  is read from a second source that could later disagree with the log.
- With no integration history it says **"no batch has integrated yet"** rather
  than printing a zero. A measured zero and an absent measurement lead to
  different actions.

- **The queue reads dependencies.** It was a flat label search: it claimed
  whatever Jira returned, in whatever order, and nothing anywhere read issue
  links. A ticket queued behind work that does not exist yet was
  indistinguishable from one that was ready, so an agent could be spawned
  against a codebase missing the very thing it was meant to build on. The cost
  is not one wasted run — it is a wasted run, a merge conflict, and somebody
  working out which of two parallel implementations is the real one.
- **Blocked tickets are skipped, not failed.** They keep their label, stay in
  the queue, and are claimed on a later tick once their blockers land.
  `orion queue` names the specific blockers rather than saying "blocked".
- **An unknown blocker does not block.** A link into another project, or one
  the token cannot see, is treated as satisfied. The alternative is a ticket
  that can never be worked because of a reference nobody can inspect — the
  same failure mode as a required check that never reports.
- **Dependency cycles are detected and refused.** A → B → A is a data error,
  not a scheduling problem: both keys are named and neither is worked.
  Silently picking one would produce an ordering nobody could explain, and a
  different one on the next run.
- **Rank still decides order.** Dependencies decide what is *eligible*; the
  backlog order people curate by dragging tickets decides what goes first
  among those.

### Changed

- `orion release ship` is the only `orion release` subcommand that builds, tags
  or publishes; the rest still only manage Jira milestones. A bare
  `orion release` still names no action, and `publish` and `cut` remain unwired.
- `orion watch` has no code path to any of this, and a test enforces it.

- Both fix-round ceilings now default to **3**, raised from 2. `qa.max_rounds`
  bounds QA's findings-fix-reverify exchange; `ci.max_fix_attempts` bounds the
  fix loop after a red build. A second fix round has demonstrably been
  productive, and stopping at two escalated to a person work that one more
  exchange would have finished — but three raises the **worst case by half** on
  every ticket that fails to converge, and that spend lands on the implementer.
  Set either back to `2` to buy the old ceiling back. The CI loop's early stop
  is unchanged: an identical repeated failure still ends it immediately, so the
  third attempt is only ever reached by a run producing a *different* failure
  each round.

- This reverts the revert. OR-229 landed on 2026-08-31 and was backed out the
  same day as a "correctness fault": answers appeared matched to the wrong
  questions under `-count=2`. **The fault was in the test fixture, not the
  code.** The fake `claude` emitted its `case` arms by ranging a map, so their
  order was Go's randomised map order — and one marker was `worktree`, a word
  that appears in `ExplorePrompt`'s own READ ONLY section and therefore in
  every child's argv. Whichever arm the draw put first answered all three
  questions. `Fan` writes `results[i]` with `i` captured by value and
  `exploreAll` reads `out[i]` from the same index; the pairing was never wrong.
  The fixture now sorts its arms and refuses any marker the prompt template
  contains.

- **It does not reorder, and it does not deprioritise.** Priority then Rank is
  Jira's answer and stays Jira's: Rank is a person expressing an intention by
  dragging a ticket, and silently overruling that is how a queue stops being
  trusted. The manager decides what is *eligible*; Rank decides what goes
  first among those.
- **Unknown is never zero.** Each eviction signal reports whether it could be
  read at all. A cleaned-up worktree says nothing about how many rounds a
  ticket spent, and reading that silence as "none" is how a ticket that has
  already failed twice gets a third run.
- **The capacity limit is applied after the rules**, so a blocked ticket is
  never reported as "no free slot" — true, useless, and it would leave the
  blocker unnamed on every pass.
- The rules run on the query's results rather than as extra JQL, because the
  claim query and the held query must stay exact inverses, and because a
  supersession rule cannot be written in JQL at all: it depends on links
  written on *other* tickets in the same result set.
- Two of the three admission rules already existed and are now named in one
  place rather than reimplemented: `tracker.Ready` decides blocked (OR-95), and
  `Schedules.HoldReason` decides unscheduled (OR-221, which this absorbs).

- **A run that fails now says what happened and what it cost, instead of
  `claude exited 1`.** That line was indistinguishable from a crash, a lost
  network, an expired login and a session that simply grew too large — and each
  of those wants a different response. OR-212 fixed exactly this for the login;
  the other three arrive the same way now.
- **A filled context window is reported as a measurement, not a guess.** Orion
  already records the largest prompt any single turn sent and the window that
  had to hold it. When the peak reaches 90% of the window, the failure names
  both numbers and the remedy: a smaller ticket, or exploring through
  subagents so repository reads stay out of the parent's context.
- **It will not call a failure "context exhaustion" because the run read a lot
  of tokens.** A large `cache_read` is what a long healthy run looks like too —
  cache reads are re-sent context, so they grow with turns whether or not the
  window ever filled. Without the window measurement the failure is reported as
  unclassified, with its cost. A confident wrong sentence is worse than the bare
  exit code it replaced.
- **A lost connection is separated from a crash**, read only from the CLI's own
  result field — never the agent's prose, which is full of the words "connection
  reset" whenever the ticket is about retries.
- **The cost of a failed run is in the line itself.** The OR-224 run that
  prompted this spent $17.23 across 121 turns and produced nothing, and finding
  that out meant reading `events.jsonl`. The reason string now carries turns and
  dollars, so it reaches the terminal, the Jira comment and Slack at once.

- `orion watch` now shows its chatter in a frozen window above the live region
  instead of letting it scroll the terminal. The window holds at least five
  recent lines and grows into whatever room the terminal has left, so a
  talkative tick can no longer push the running-ticket rows or the status line
  off the screen, and a quiet one still shows the last few things that
  happened. Lines that scroll out of the window are gone from the screen only —
  `events.jsonl` and `orion logs` remain the complete record. Press ctrl-r (or
  any key) followed by Enter to drop the cap and let the log print in full for
  the rest of the run. The window grows with the terminal only when the shell
  exports `LINES`, the same limitation `COLUMNS` already has; otherwise it
  stays at five.
- Redirected output is unchanged: off a terminal there is no window and no cap,
  so a piped or captured log stays a complete, greppable record.

Isolation still tests synchronously. A red batch bisecting will hold the tick
for the length of that search. The common path — a green batch waiting on one
build — is what this fixes; the rest wants the same treatment and is a
separate change.

- **A batch now lands the ref it tested.** It used to mark its members
  `passing` and hand them back to the per-branch path to merge one at a time,
  which left every remaining member behind the work branch: each was rebased
  and each rebase bought another CI run. That is the quadratic term batching
  exists to remove, and it survived inside the feature meant to remove it.
  The batch merges once, the work branch moves once, and nothing rebases.
- **No pull request per ticket while `collect.batch_integration` is on.** The
  branch is pushed and the ticket becomes `orion-ready`; the batch's own pull
  request is the single CI run and the single review surface for the set.
  Measured before this change: four tickets bought four `pull_request` runs
  plus the batch's own, and then the cascade on top.
- **The batch opens a pull request for its ref.** Not decoration — without one
  nothing builds it, because `ci.yml` triggers on `pull_request` and a bare
  push to an ephemeral ref matches no trigger; and nothing can read it either,
  because check status is read through `gh pr view`. On 2026-08-31 a batch was
  green on GitHub twice while Orion waited out its full 30-minute deadline and
  then correctly refused to read silence as green.
- **The sound members of a red batch land instead of waiting.** They were
  deferred to a later batch, so one bad branch held good work for a whole
  cycle. The culprit goes back to the coding queue; the rest merge now.

- Latent rather than live: the job needs `TAP_GITHUB_TOKEN`, which that file's
  own header says deliberately does not exist. Fixed now anyway, because a
  guard that depends on a secret staying absent is not a guard, and the day
  someone adds that token is the day nobody re-reads this file looking for a
  channel check.

- Found by building `develop` and running the command, not by a test. Both
  defects are the seam between OR-253 and OR-254 — same release, never run
  together — which is the shape a green suite cannot catch.
- This repository's own four stuck tickets stay stuck, and that is correct: the
  only batch note in its log records `landed=[]`, because that batch was merged
  by hand rather than through `runBatch`. Nothing anywhere records them as
  landed, so nothing can recover it. Future landings are recorded twice over.

### Fixed

- **Concurrent runs of the same stage no longer overwrite each other's log.**
  The path was built from a second-resolution timestamp plus the stage name, so
  every child of a fan-out — `orion fan` runs them all as stage `fan`, `orion
  explore` as `explore` — resolved to one filename and `os.Create` truncated it
  N-1 times. A child's context is gone the moment it exits; its log was all
  that ever existed of it.

- **Any keystroke used to drop the frozen window's cap, permanently.** A stray
  arrow key ended the bounded window for the rest of the run with no way back.
  Now only `ctrl-r` does it, `ctrl-o` toggles the region, and everything else
  is ignored. Every byte of a read is examined, so a fast double-press or a
  paste is not half-swallowed.

- **A batch waiting on CI no longer silences the whole watcher.** The check
  poll ran inside the watch tick with a thirty-minute deadline, so while a
  batch built, nothing else printed — on 2026-08-31 the console reported
  nothing for the whole of a batch's CI while three agents carried on working.
  That is the OR-128 silent-hang shape arriving through a door OR-128 could
  not have known about, and it breaks the tick's own stated contract: *"a tick
  that blocked on the agent could never start a second."*
- **The checks are read once per tick.** A build still running is a third
  answer beside green and red, so the batch is recorded and resumed on the
  next pass. Every other ticket keeps being reported throughout.
- **The deadline moved into the batch record**, which is where something that
  spans ticks belongs. Same thirty minutes, same refusal to read silence as
  green; the only change is that nobody sits and waits for it.
- **A pending build is not a CI run.** Counting one per tick would have
  inflated the number the whole design is justified by, once a minute, for as
  long as the build took.

- **The QA verdict re-ask no longer has a five-minute cap compiled into it.**
  When QA ends without a verdict and without findings, Orion resumes its
  session and asks once for one. That re-ask was bounded at five minutes
  regardless of the run it resumed, and on OR-248 it killed a re-ask against a
  session that had been working for thirty. The change reached a pull request
  with no QA opinion at all — neither a verdict nor a fix round, just an
  unverified branch and a person sent to read it.
- **The budget now scales with the run it resumes**, floored at five minutes.
  A thirty-minute session gets six; a four-minute one still gets five. The
  cheap case stays cheap, which was the whole point of the original cap: one
  line asked of a session that has already done the work.
- **A killed re-ask says so**, and names the lever. "Gave no verdict, even
  when asked for one" reads as QA refusing to answer; the truth was that it
  never got the chance, and the fix is a number rather than a diff to read.

- **The ephemeral ref is deleted after landing**, remotely as well as locally.
  It used to be dropped as soon as testing finished, which cannot stand now
  that the tested ref is the thing that merges.
- **A branch already contained in the work branch is not offered to a batch.**
  It contributed nothing and widened the set that has to be bisected when
  something else in the batch failed.

- **The release workflow can no longer push a beta to the Homebrew tap.**
  `scripts/release.sh` had three independent guards keeping a prerelease away
  from installers; `.github/workflows/release.yml` had none of them — a
  free-text tag input, no beta concept anywhere in the file, and an
  unconditional push of the rendered formula to the tap and the Scoop bucket.
  Dispatching it with `v0.9.0-beta.1` would have handed a prerelease to every
  stable user's next `brew upgrade`, and semver would have kept offering it,
  because `v1.2.3-beta.4` sorts *below* `v1.2.3`.
- The workflow now derives the channel from the tag, marks a beta as a
  prerelease on GitHub, and skips package publishing entirely on that channel.
  A tag matching neither shape stops the run rather than being guessed at.
- **One definition of the shapes**, in `scripts/tag-channel.sh`, used by both
  callers. They ask different questions — the script is *told* its channel and
  checks the tag agrees, the workflow is given only a tag and must derive one —
  but two copies of "is this a beta" drift, and the direction they drift in is
  a prerelease reaching the tap.
- The dispatched tag reaches the shell through the environment rather than
  being interpolated into a script body, and a test keeps it that way.

- **A refused release now leaves a record.** The event log opened *after* the
  preflight, so every refusal `orion release ship` exists to make — dirty tree,
  red checks, empty delta, wrong branch, wrong channel for the tag — wrote no
  event at all. The command's most frequent outcome was its least recorded one,
  and "why didn't it ship last night?" is exactly the question asked once the
  terminal has scrolled away.
- One event per guard, not one for the set. Which guard fired is the whole
  content of the record; a count is not something anyone opens a log to learn.
- A dry run stays out of the log deliberately. It is a question, not a decision
  not to ship, and an event for it would read like the latter.
- The one remaining gap is now stated instead of implied: a repository with no
  Orion binding still ships, on no log, and says so. The OR-116 changelog entry
  claimed "every step is an attributed event" while the code did not keep that;
  it now says what is true.

- **Ctrl-C during the cut now says where it stopped.** The interrupt handlers
  covered the two waits — CI and the Slack approval — and both said something
  useful. The cut itself had none, so an interrupt during the cross-compile and
  upload killed the process on Go's default handling: the release branch
  promoted, nothing tagged, and not one word about it. That is the longest step
  in the command by a wide margin and the one the operator is actually sitting
  and watching.
- The handler now spans the whole irreversible section and routes through the
  same `shipStopped` a failure does, so an interrupt gets the same sentence,
  the same resume command, and the same warning that re-running
  `orion release ship` would refuse because nothing is left to promote. The
  state the two leave behind is identical, so two wordings would have been two
  descriptions of one situation.
- The two existing interrupt paths are unchanged.

- **`orion dashboard` no longer reports tickets as waiting that landed days
  ago.** The batch lands the ref rather than merging ticket by ticket — that is
  the whole of OR-253 and why the rebase cascade is gone — so no member ever
  emitted the merge event the per-branch path emits, and last-write-wins left
  every batched ticket at `push` forever. `queue depth` is read off that list,
  so it contradicted `orion queue` outright. Worse than a wrong number: READY
  growing while integration holds steady is the backpressure signal the
  dashboard exists to give, and it was pinned high on any repository using the
  feature it was built to measure.
- A batch landing now emits a merge per member. Emitted at the source rather
  than patched in the dashboard, because the dashboard is not the only reader
  of the log and the next thing to count merges would have inherited the hole.
  The dashboard also reads the note's `landed=[…]` as a second source, so a log
  written before this still retires its members.
- **The batch note is written and parsed in one place.** It lived in two that
  agreed by coincidence: the reader scanned for `"%d run(s) in %fm"`, which
  parses `3m0s` by luck and fails outright on `45s` or `1h2m0s`. Run against
  this repository's own log it matched nothing, so the integration section said
  "no batch has integrated yet" and **CI runs saved** — the number the whole
  batching design is justified by — was permanently absent. The builder and the
  parser now sit together in `internal/events` and are tested against each
  other, so a change to the wording breaks a test rather than a dashboard.
- **Members are counted whatever the project key looks like.** The old parser
  counted occurrences of the literal `"OR-"`, so on any other tracker every
  batch was measured as having no members and the runs-saved figure divided by
  nothing.
- An unmeasured baseline prints as `unknown` rather than `0s`, which read as a
  measurement that found zero.
- `orion dashboard` appears in the usage text. It was dispatched in `main.go`
  and named nowhere, so nothing told an operator it existed.

- **A ticket waiting for the integration queue is no longer re-claimed.**
  `orion-ready` was missing from the claim query's exclusions, so a ticket
  that had finished and was waiting to be batched could be picked up and
  worked a second time.

## v0.8.8

### Added

- An environmental fault now HOLDS the tickets it stopped instead of failing
  them. When a run dies without taking a turn — an expired `claude` login, a
  quota wall whose reset time the provider never stated, an unreachable tracker
  or forge, a missing nj-agents — the ticket keeps its queue label, goes back to
  To Do, and its empty worktree and branch are removed so the retry starts
  clean. A worktree with anything in it is kept and reported, as prune already
  does. A run that spent a turn and failed is still `orion-failed`, unchanged.
- Orion asks about the fault in Slack: one message per fault naming the exact
  remedy and which tickets are waiting, not one message per ticket. A ✅ from
  someone on `slack.merge_approvers` is treated as a claim, not a fact — the
  matching `orion doctor` check is re-run before anything is released, and a fix
  that did not take is reported in the same thread. One automatic retry per
  ticket per fault; a second identical fault escalates instead of asking again.
- `orion reset --held [fault]` re-checks the environment and releases held
  tickets immediately, for an operator who has just fixed something and does not
  want to wait for the next tick.

- **`orion config collect`** shows how work lands and sets the switches that
  decide it, so `batch_integration` and `auto_rebase` are reachable by command
  rather than by hand-editing `orion.json`. `auto_rebase` never had one.
- Turning a switch ON prints what it costs before you walk away from it. A
  toggle whose consequences are unstated is one people flip to see what
  happens.
- It writes the `collect` block if the file has none, which every config
  written before this does. Telling an operator to add one by hand would be
  the command instructing them to do the exact thing it exists to replace.

- **Batch integration is wired into `orion collect`.** With
  `collect.batch_integration` on, a pass assembles its ready branches into one
  ephemeral ref, tests that once, and reports each member as landed, ejected,
  a culprit, or deferred. Off, the flag is a config read and the per-branch
  path is untouched.
- **An empty check rollup no longer reads as green.** The existing per-branch
  path treats "no checks are configured on this repository" as PASSING, which
  is right for a repository without CI and catastrophic for a ref whose checks
  have not started: every member of a batch would land on no evidence at all.
  The batch tester waits instead, and gives up with a reason rather than
  reading silence as success.
- The batch does not merge. A green batch reports its members as passing and
  the existing approval and merge path acts on that, so the only irreversible
  step in the package stays where it already lives.

- `orion watch` now draws a batch as a batch. The pinned region gains a batch
  mode that follows a set from assembly through CI to isolation, so a batch
  waiting up to thirty minutes on one CI run is no longer silent. Assembly
  shows membership, testing shows one bar for the shared run, and a red batch
  draws the bisection tree with the run count — the tree because it is the only
  rendering that explains why finding a culprit cost four runs rather than one.
- An ejected branch is now visually and verbally distinct from a culprit. An
  ejection is yellow, carries its own glyph, and says the branch returns to the
  queue; a culprit is red. They read alike in the first cut, which sends an
  operator to debug a branch with nothing wrong with it.
- The runs-per-branch figure — the entire argument for batching — is a headline
  rather than the last line printed.

### Changed

- `orion watch` no longer exits when the CLI is logged out. It holds the queue —
  claiming nothing, still reconciling work already in flight — and resumes on the
  first tick whose check finds the environment healthy. It still stops outright
  in the one case where nothing could release it: a fault it failed to record.

- The pinned region survives on a batch alone. It previously drew nothing
  without a running row, and by the time a batch assembles its members' agents
  have finished, so the whole CI wait rendered blank.
- Off a TTY a batch is one plain line per tick naming the phase, the member
  counts and the run number. No bar, no spinner, no cursor control, the same
  contract the per-run rows already met.

- **`limits.max_concurrent_tickets` has no ceiling.** It was capped at five,
  refused above that, and clamped on read. Orion was overruling a number the
  operator chose for a machine it cannot measure.
- **Above ten it asks instead.** `orion config limits max_concurrent_tickets 12`
  states what the number costs and waits for a yes: conflicts grow with the
  SQUARE of it, every run holds a worktree off one shared clone that git
  serialises on, one rate limit is shared by all of them, a budget checkpoint
  is only read between runs so those already in flight sail past it together,
  and approvals do not parallelise — N tickets finishing is N approvals
  waiting on one person.
- An unanswered prompt is a no, including when stdin is closed. Defaulting to
  yes would let a script set a number nobody chose.
- **A configured value is now honoured on read.** The old clamp produced a
  file saying forty while the watcher ran five, with nothing in either place
  explaining the gap. Zero still means the shipped default and never
  unlimited.

### Removed

- `collect.batch_size`. A batch can only hold branches that finished, and no
  more can finish than `limits.max_concurrent_tickets` allowed to run, so a
  second number could only ever disagree with the first about the same thing.

### Fixed

- A bare `502`, `503` or `504` from the tracker or the forge is now classified
  as unreachable. The connectivity table required the reason phrase, so a
  gateway that returned an empty body left only the status code and the outage
  was reported as a ticket failure. Bounded so it cannot fire on an issue key,
  a token count or a duration.
- Two tickets meeting the same fault in the same tick can no longer both decide
  they created the hold, which posted the same Slack message twice, nor race on
  a shared temp file and lose one of the two writes entirely.

## v0.8.7

### Added

- `orion queue` marks each labelled ticket the watcher will not claim as
  `held`, with the reason on the line below it, and `orion watch` reports the
  same reason on the tick that would otherwise start nothing. A ticket that
  silently never runs is indistinguishable from a broken watcher.

- `orion queue add` and `orion queue remove` put tickets into the queue and take
  them out, so the most frequent write an operator performs no longer means
  editing the ORION label by hand in the Jira UI, one ticket at a time. Both take
  keys and inclusive ranges — `orion queue add OR-100 OR-140..OR-145` — parsed by
  the same code as `orion release add`, and both print the whole plan before
  writing anything. Re-running is a no-op that says so.
- `orion queue add --reset` requeues a failed ticket in one command: it clears
  `orion-failed` and returns the status to To Do together, rather than leaving a
  ticket that has one but not the other and so never runs.

- **`internal/reconcile`: compare what the repository says against what the
  tracker says.** The tracker records intent, not evidence, and the two drift
  apart silently. On 2026-08-30 OR-211 read In Progress for hours while its
  finished work sat committed but never pushed: `develop` never received it,
  the milestone counted it as included, and nothing noticed. A release cut on
  the tracker's word would have shipped a changelog claiming a fix that was
  not in the binary.
- Three disagreements are detected: a ticket reported finished with no commit
  on the integration branch, work on the branch whose ticket is still open,
  and finished work on no milestone, which can appear in no release's notes.
- Every finding carries the evidence that produced it, so a claim can be
  checked without re-deriving it. Agreement produces silence, and a clean
  report records how many tickets were compared so it can be told apart from
  one where nothing was examined.
- It reports and never edits the tracker. A reconciler that silently rewrites
  status is a second source of drift rather than a cure for the first.

- A claimed ticket now says WHICH actor holds it. Beside the `orion-working`
  claim lock, Orion sets a second label naming the stage --
  `orion-stage-implementer`, `orion-stage-qa`, `orion-stage-frontend` and so
  on -- so implementation, QA and a fix round are distinguishable from the
  Jira board rather than all reading as "somebody is on this". It moves at
  every stage handoff and is cleared whenever the claim is, including on the
  failed, requeued and stale-lock paths.

  The stage label carries the actor's **id, not its display name**. A Jira
  label is persisted data with no render step, so a name written into one
  would be frozen at the moment it was written and drift from a roster that
  `orion config agents` lets you rename at will. The run log keeps showing
  your configured name; the tracker shows the durable role.

  Nothing reads the new label for control flow: `orion-working` is unchanged
  and still matched exactly, so claiming behaves precisely as before. A
  ticket awaiting CI carries no stage label -- `orion-ci-wait` already says
  a pull request is open and no agent is running.

- **Batch integration (`internal/collect/batch.go`), the core of OR-236.**
  Branches that are ready are merged into ONE ephemeral ref and tested once,
  instead of each being rebased onto `develop` and tested on its own pull
  request. Four green branches now cost one CI run rather than four.
- Agent branches are never rewritten, so the design removes the rebase, the
  force-push and the landing queue rather than tuning them.
- **A branch that conflicts is ejected at assembly, before CI runs.** The ref
  only ever holds branches that combined cleanly, so a red result is always a
  real defect and never a merge problem. The ejected branch is reported with
  the conflict and offered again; the fix has to land on the real branch,
  because a resolution recorded in an ephemeral ref is thrown away.
- **A red batch is bisected rather than abandoned.** Both halves are examined
  at each split, not only the failing one, because a batch can hold more than
  one culprit and stopping at the first would land the second. Members that
  are sound are reported as deferred and offered again rather than blamed.
- Every cycle records how many CI runs it actually consumed, so the saving is
  measured rather than assumed.

- **Not yet wired into `orion watch`.** The assembly, ejection and isolation logic
  and its tests are in place; connecting it to the live landing path changes
  how work merges, and that step is taken with someone watching.

- `collect.batch_integration` in `orion.json` turns it on, and
  `collect.batch_size` caps a batch (default: the concurrency limit). **Off by
  default.** `auto_rebase` is safe to default on because it decides nothing --
  git has already said the merge is clean. This decides what lands and in what
  order, and a mistake mis-merges or strands every branch at once rather than
  one at a time.
- The repository side assembles into the SHARED SANDBOX CLONE, never a job
  worktree, and holds the repo lock for every operation. `RepoDir()` silently
  resolves to a per-job worktree when a run sets one, so using it would have
  built batches inside a running agent's checkout.

### Changed

- A ticket is now claimable only when it carries BOTH the queue label and an
  open `fixVersion`. The label means "ready to be worked"; a `fixVersion` means
  "scheduled to ship in a named release", and neither implies the other. Work
  that is ready but unscheduled is invisible to every part of Orion that
  reconciles by version — `orion release close`, its release-note verify, and
  `orion release status` — so its changelog fragment becomes an orphan and the
  release it accidentally rides in cannot account for it. The condition is in
  the queue's JQL, so an unschedulable ticket never enters the candidate set
  and cannot be claimed in a race.
- A `fixVersion` that is already released or archived does NOT make a ticket
  claimable. It is scheduled for a train that has left, and working it would
  file a changelog fragment against a milestone that has already been collated
  and dated.
- **Projects that do not use releases are unaffected.** Enforcement is
  detected, not configured: a project with at least one open milestone is
  gated, and a project with none — because it defines no versions, or because
  every version it has is closed — is queried exactly as before.

- `orion queue add` refuses a ticket that carries no fixVersion, naming the
  missing version, and neither verb will touch a ticket labelled `orion-working`
  or `orion-ci-wait` — those mean an agent or CI owns it right now, and
  relabelling under a running job corrupts the claim. `orion queue remove` only
  unqueues: it leaves status and fixVersion exactly as they were.

- **A failed snapshot commit no longer reverts the worktree.** When a run ends
  holding uncommitted work, Orion still tries to commit it; if that commit
  fails, the work is now KEPT exactly where the agent left it rather than
  discarded. A dirty worktree is a visible, recoverable problem — it blocks the
  next rebase, loudly, with every file still in it. A revert is an invisible,
  irrecoverable one.
- The report on a failed commit now names every kept file, untracked ones
  included, states the reason the commit failed, and prints `orion settle
  <KEY>` as the way out. It is marked failed and unresolved, so it cannot be
  mistaken for a clean finish. Anything the run committed itself is untouched,
  as before.

### Removed

- `workspace.RevertTracked`, which had no other caller. Nothing in Orion
  discards an agent's uncommitted work to tidy a worktree any more.

### Fixed

- A tripped breaker now says so on the surface you are actually watching. When a
  breaker parks a run's worktree, the landing pass names the breaker, the ticket
  and the exact `orion reset --session <id>` command in the watch log, instead of
  reporting only that the worktree "has uncommitted changes" — a downstream
  symptom whose cause and remedy lived in the agent's own session and in
  `plans/BLOCKED.md` under `ORION_HOME`, neither of which an operator reads.
- The "still behind and still yours" reminder is now said at most once every 15
  minutes while nothing changes, rather than on every poll. A branch that is
  pushed is still announced afresh immediately, and the hand-over itself is
  unchanged; only the reminder that nothing has happened is throttled.
- A run that ends holding uncommitted work after a breaker trip now prints the
  recovery command with it.

- **Work destroyed by the revert fallback.** The fallback was added on the
  assumption that the commit would normally succeed; OR-241 established that
  `CommitAll` had never worked in this repository, so it fired on every run.
  On OR-116 it destroyed a finished test file — the `add` had failed before
  staging, so no blob was written and the file was unrecoverable.

## v0.8.6

### Added

- `docs/decisions/0015` records who gates a merge once CI runs on an ephemeral
  merge ref instead of on each branch's pull request (OR-236). Orion is the
  authority on landing, and `develop` keeps a post-merge check on push as the
  backstop — a workflow trigger rather than branch protection, so it works on
  the free private repositories where protection returns 403. The record also
  binds OR-236 to two guards: an empty check rollup must stop reading as
  passing, and `auto_merge.require_checks` must actually be read (OR-237).

- `orion watch` now pins a live run display to the bottom of the terminal,
  redrawn about four times a second: one row per running ticket with a braille
  spinner, its stage, elapsed time, a bar measured against that actor's median
  run duration in this project, a sparkline of tool calls per 10s over the last
  two minutes, and the tool-call count. The bar is a reference, never a
  prediction — past the median it fills, stops, and the row says `running long`
  with the median beside it. A run that has made no tool call for a minute
  reports `quiet Xm`, so a stalled run and a busy one can never look the same.
  Off a terminal there is no cursor control at all: one plain line per run per
  tick, so a redirected log stays a log. `NO_COLOR` keeps the layout and drops
  the escape codes and glyphs; `TERM=dumb` gets the plain form.

### Changed

- `orion explore` now takes several questions in one call —
  `orion explore "<question>" "<question>" "<question>"` — and answers them
  concurrently, one subagent each, capped by `limits.max_concurrent_children`.
  A single question behaves exactly as before. One question failing no longer
  costs you the answers to the others; the batch only sends you back to reading
  for yourself when every question failed.
- The implementer's prompt now opens with an exploration phase: work out what
  you do not know and ask all of it before reading, rather than one question at
  a time. Asked one at a time the agent waits out each round trip and greps
  instead, which is the context cost the command exists to remove.
- A fan-out now announces its roster before dispatch and marks each child as it
  lands, with the verdict and the running count, so several concurrent
  subagents are legible while they run rather than a silent gap ending in a
  wall of output.

### Fixed

- A watcher tick with nothing in flight and nothing awaiting CI now prints
  nothing. It used to print `checking for tickets awaiting CI...` followed by
  `nothing is waiting on CI.` every sixty seconds, which read as an idle system
  while agents were working. `orion collect` typed by hand still reports both.
- The warning that a run inherited your Claude Code configuration because a
  curated config directory cannot authenticate on this platform now prints once
  per process instead of once per run. It is a property of the machine, not of
  the ticket.

## v0.8.5

### Fixed

- `orion release verify` no longer reports commits that are already on `main` as
  unattributed. The attribution check now inspects only the promotion range
  (`main..develop`) and asks whether a commit names a ticket at all, rather than
  a ticket from the version being promoted — so work correctly keyed to an
  earlier milestone is no longer counted as carrying no ticket key. A clean
  promotion verifies with zero warnings instead of a warning larger than the
  promotion itself.

- **`CommitAll` had never succeeded in a repository that gitignores a path the
  caller also excludes**, which is Orion's own. `git add` treats an explicit
  pathspec naming an ignored path as an error and refuses the whole
  invocation, so `:(exclude).orion/state` against a `.gitignore` holding
  `.orion/` exited 1 on every call, with a dirty tree or a clean one.
- That made two v0.8.3 fixes inert on arrival. OR-233 settles a worktree a
  stopped run left behind, and OR-234 commits the tests QA writes; both call
  `CommitAll` and neither could ever have worked here. OR-211 lost a run to
  the same error earlier, reported as a mystery.
- The exclusion was redundant from the start: `add -A -- .` never stages an
  ignored file. Orion now skips any exclusion git already ignores, so the
  commit happens and the ignored directory still stays out of it.
- The warning also fired when the worktree was clean, telling operators their
  work had not reached the pull request when nothing had been left behind. A
  control that reports loss where there is none is as harmful as one that
  stays quiet where there is.

## v0.8.4

### Fixed

- **Every supervised run on macOS failed to authenticate on v0.8.3.** OR-213
  gave each run a curated `CLAUDE_CONFIG_DIR`, and on macOS such a directory
  can never log in: the CLI wants `.claude.json` INSIDE the config directory
  while the operator's own lives at `~/.claude.json`, outside `~/.claude/`,
  and the Keychain credentials are not reached for a non-default directory.
  Supplying `.claude.json` is necessary and still not sufficient. The result
  was `claude is not authenticated` on every ticket while the operator's own
  CLI worked normally, and re-authenticating could not fix it.
- Orion no longer builds a curated directory on macOS. It inherits the
  operator's configuration there and **says so on every run**, because a run
  that is not capability-curated must not be silent about it: the whole
  plugin surface is in scope, which is what OR-213 exists to prevent. Linux
  and CI are unchanged and stay curated.
- `linkCredentials` no longer returns silently when it finds nothing to carry
  over. That silence is why this shipped: the code assumed an absent
  `.credentials.json` meant the platform kept credentials elsewhere and all
  was well, and said nothing when the assumption was wrong.

## v0.8.3

### Added

- `orion doctor` checks that the Claude CLI is AUTHENTICATED, not merely
  present. A logged-out CLI passed the old check and failed every run; it is now
  a FAIL naming the account and the command that repairs it.

- `delegation.inherit_operator_config` in `orion.json`: stages or actors whose
  runs get your own Claude Code configuration, plugins and MCP servers
  included. Empty by default; the choice is recorded in the event log.
- The `run-start` event now states what the run was given — the tool count and
  the MCP servers, read off the CLI's own init frame. Until now this could
  only be discovered by reading a raw transcript.

- `orion release add <version> <KEY>...` attaches tickets to a milestone from
  the command line. It takes bare keys, comma- or space-separated, and
  INCLUSIVE ranges — `orion release add v0.8.3 OR-100 OR-133 OR-140..OR-145` —
  which is the point: attaching one work block of thirty-six consecutive
  tickets previously meant a click each in the Jira UI or a scripted REST loop,
  and a milestone that is expensive to maintain drifts. `release` could already
  create a version and report on one; this is what puts something in it.
- The command resolves every key before it writes any, and prints what it will
  add, what it will MOVE (a ticket already on another milestone is leaving that
  one, which is a different sentence from an add and is reported as such),
  what is already on the target, and what names no ticket at all. A range can
  quietly include a ticket you did not picture, so the plan is readable before
  it is applied. Re-running is a no-op that says so, the same property
  `release create` has.
- Adding to an already-released version is refused unless `--force` is given:
  it rewrites a history that has already shipped. A version that does not exist
  is refused naming the versions that do, and a range whose ends are reversed
  or that spans two projects is refused with the reason rather than silently
  expanding to nothing.
- `--project` is optional and inferred from the ticket keys, which say it
  unambiguously; passing one that contradicts the keys is refused rather than
  silently preferred.

- `orion settle <KEY>` unsticks a ticket's worktree without you ever needing
  its path. It finds the worktree for the ticket, reports what is holding the
  branch, and commits it as an unverified snapshot so `orion collect` can
  rebase again. Nothing is verified and nothing is pushed. Use `--dry-run` to
  see what it would do first.

  It refuses rather than guesses when the worktree is mid-merge, mid-rebase,
  holding unmerged paths, or on a detached HEAD, and prints the command that
  resolves each. Until now the only recovery from a worktree left dirty by a
  killed process or a full disk was to `cd` into a hashed path under
  `ORION_HOME` and run git against an agent's branch by hand.

### Changed

- `orion watch` and `orion work` now print the run rather than the agents'
  tool-call transcript. At concurrency 4 the console was about 60% "read",
  "ran" and "edited" lines, and the ten lines that decide whether a person
  has to act were scattered through them. The default now prints stage
  boundaries, outcomes, escalations, failures and anything awaiting a person
  — on the order of 15 lines for a ticket instead of roughly 200. Add
  `--verbose` for the full stream as before. **Nothing is removed from the
  record**: the event log, the per-run log and `orion logs` are complete at
  either level, so triage and history are unaffected.
- A run of lines from the same actor on the same ticket states the name and
  model once instead of on every line. The columns stay where they are, so
  the layout is unchanged and the eye tracks the ticket key and the verb,
  which are what vary. Any change of ticket, actor or model — and every
  stage boundary — states the identity again.
- Consecutive identical lines collapse to one line with a count
  (`edited cmd/orion/aiops_test.go (x4)`) rather than repeating.
- A long absolute path in a message prints as its file name
  (`ran cat bxpkzaome.output`, not the full sandbox temp path, whose only
  useful token was the part the line clipped away). The full path is in the
  event log.

- A branch is now rebased onto its integration branch immediately before its
  first push, so it arrives on the remote current and the first CI run is the
  one that counts. An agent run takes ten to forty minutes; at concurrency 4
  something else usually merges inside that window, so the branch used to be
  pushed at a base that had already moved, CI ran on it, and the landing pass
  then rebased it and force-pushed — a second full CI run on a second commit,
  for a reason that was avoidable. This narrows that window rather than
  closing it: the base can still move between the rebase and the push, and
  the landing queue still handles what gets through.
- The pre-push rebase never refuses the push. If the branch does not replay
  cleanly, the original is pushed and the pull request opens anyway, with the
  conflict reported and the exact commands to resolve it — finished work
  hidden behind an unresolved conflict leaves you with nothing to look at. A
  worktree carrying `.orion-manual-lock` is never rewritten, and an
  unreachable remote degrades to pushing as it stands rather than failing the
  run. A branch that is already current costs one extra fetch and prints
  nothing.

- `slack.merge_approvers` accepts a Slack user ID, a username, a display name
  or an email address, and each is resolved to the member ID a mention needs.
  A name lookup needs the `users:read` scope and an email needs
  `users:read.email`; without them the request still sends and still names
  the person in plain text, and the run reports which approver could not be
  mentioned. A user ID needs no scope at all, so it is the form that always
  works. Each approver is resolved at most once per run.
- Only the approval request mentions anybody. A merged notice, a CI report
  and a cost report do not, and no message ever uses `@channel` or `@here` —
  a room tagged for everything is a room that gets muted, and in a channel
  created per project a broadcast reaches people with no standing to approve.

### Fixed

- `orion release status` and `orion release verify` no longer report every
  ticket in an already-shipped release as undocumented. Collation writes the
  fragments into `CHANGELOG.md` and deletes them, so `.changelog.d/` is the one
  place a released version's notes are guaranteed not to be — and that was the
  only place the check looked. Re-running `orion release verify` on a version
  that is tagged, published and marked released reported it unsafe to promote,
  and every past release stayed blocked from then on. The check now asks
  whether the change is documented rather than whether a file exists: a
  `## <version>` section in `CHANGELOG.md` counts, and so does an uncollated
  fragment. Which state applies is read from `CHANGELOG.md` itself, so the
  check needs no tracker call and answers the same offline. A version still
  awaiting collation behaves exactly as before, and a done ticket the collated
  section does not name is still reported — as a warning rather than a blocker,
  because a note folded into another ticket's bullet reads identically and a
  published release cannot be corrected by refusing it.

- An expired Claude login is reported as what it is. A run that dies because
  the CLI is not signed in now says `claude is not authenticated: <the CLI's own
  reason>. Run: claude, sign in, then restart the watcher.` instead of
  `claude exited 1`, which was indistinguishable from a crash, a bad prompt or a
  sandbox denial.
- Tickets are no longer labelled `orion-failed` for it. Nothing was attempted --
  no turn, no token, no branch work -- so the claim is released and the ticket
  goes back to the queue, the way the quota back-off already behaves.
- The watcher stops instead of draining the queue. Every subsequent ticket would
  fail identically until a human signs in, so continuing to claim them turned one
  fixable problem into a queue of released tickets.

- The Slack approval request now @-mentions the people on
  `slack.merge_approvers`, so Slack actually notifies them. It named them in
  plain text before — "Only navjyot can approve" — and Slack raises a
  notification for the member-ID form `<@U012ABCDEF>` and for nothing else,
  so a bare username was styled like any other word and reached nobody. The
  merge then waited on a person who was never told, and the ticket sat in
  `ci-wait` repeating "nobody has approved it yet".

- A run that ends with uncommitted tracked changes in its worktree now
  commits them as an unverified snapshot, whatever ended the run and whatever
  the breaker says. The cleanup used to fire only when a breaker trip was
  still flagged, and that flag is erased by several unrelated things — an
  unverified-edits trip clears itself when a verify passes, `orion reset`
  clears it by hand, and every agent session writes its own state file. On
  OR-217 the flag had already self-cleared, so 163 lines of staged work were
  left behind, `orion collect` refused to rebase the branch on every poll for
  over fifteen minutes, and two healthy branches starved behind it in the
  landing queue. A trip, where one is still on record, now decides only the
  commit message and what is reported — not whether the cleanup happens.

- The QA stage now commits the tests it wrote before it reports its verdict.
  The QA prompt already asked for that commit and the agent did not reliably
  make it — two consecutive tickets left 163 and 110 lines of test code
  sitting in the worktree, once staged and once not — and everything after the
  stage reads commits rather than the worktree. So an uncommitted test made the
  red-before-green check report "QA did not add or change a test file", a false
  negative on precisely the run where a test *was* written; it left a dirty
  worktree, which is what `collect`'s rebase refuses; and the branch was pushed
  without it, so CI could go green on a pull request whose own evidence was
  still on disk. The commit happens on every exit from the stage, including
  when findings are still open at the round ceiling: a red pull request is the
  correct outcome for a change QA found a defect in, and must not be avoided by
  leaving the failing test behind.

### Security

- A supervised run no longer inherits the operator's Claude Code
  configuration. Every agent Orion launches now gets a config directory Orion
  builds (`$ORION_HOME/agent-config`, holding the nj-agents skills and agents
  and nothing else) and is started with `--strict-mcp-config`, so it has no
  MCP servers. Runs previously loaded `~/.claude` in full: on a measured run
  that was 179 tools, 148 of them MCP tools with write access to the
  operator's own authenticated accounts — `createJiraIssue`, `editJiraIssue`,
  `createConfluencePage` — none of which Orion's breaker, sandbox or approval
  path could see.

## v0.8.2

### Added

- `orion plan <KEY>` takes a tracker project that has already been provisioned
  and sets up the design phase around it. It reads the project's name and
  description back from the tracker rather than re-deriving them from the
  original idea, provisions the workspace as its first action — so nothing
  downstream has to write anywhere but an isolated tree — and only then
  announces the four stages it would run, who runs each, and what the chain is
  expected to cost. It stops there: this release dispatches nothing.
- The workspace, the git repo and the tracker project now share one name.
  `orion plan` derives a single canonical slug from the project's finalised
  name and uses it for the workspace directory and the repo; the Jira key keeps
  its own derivation, because Jira's key charset could not hold the slug
  anyway. A person moving between the tracker, the filesystem and GitHub sees
  the same name in all three instead of needing a mapping between them.
- `orion plan --dry-run` prints the project, the workspace it would create, the
  roster and the cost shape, and creates nothing — not a workspace, not a
  branch, not an agent call.
- The per-run cost estimate is measured from the runs actually recorded in the
  rolling seven-day window. With no history it says there is nothing to
  estimate from rather than showing a figure derived from no data.

- `orion aiops <KEY>` reads a **finished** run's event log and reports what is
  worth filing, as a triage report plus draft tickets. It proposes; it never
  creates anything. Most detection is rules over typed events — an exhausted
  fix loop, QA finishing without a verdict, a rate-limit status this build does
  not recognise, a question the log never answers, a run that failed and stayed
  failed — so it costs nothing and cannot invent a problem. A subagent is
  started only for the leftovers no rule recognised, and not at all when there
  are none. Findings already tracked by an open ticket are not proposed again,
  and the report says plainly when the dedupe was incomplete. Pass `--no-agent`
  for rules only.
- A new agent in the roster, the AIOps engineer, configurable through
  `orion config agents` like every other actor.

- `orion collect` now holds a landing queue. When several branches are behind
  their base at once — which is what one merge into a strict base does to every
  other open pull request — exactly one is rebased per pass and the rest hold,
  costing them no force-push, no CI re-run and none of their rebase allowance.
  The turn goes to whichever branch has been behind longest, so waiting earns a
  branch its place instead of costing it one.

- A way to wait. The ticket prompt now states how to sit out a long command —
  run the suite in the foreground with a generous timeout, do not background it
  and re-read its output file — because the two runs lost to that reached for
  polling only because nothing had ever told them another option existed.
- Work the run was holding when a breaker tripped is committed on the spot, as
  a `wip:` snapshot marked unverified, before the agent gets another turn.
  Modified files and new ones both, since a run's new test files are exactly
  what `git commit -a` would leave behind. `plans/BLOCKED.md` is excluded: it is
  the account of the trip, not part of the change.

- `orion release close <version>` marks a milestone released, which was the one
  step of a release that had to be done outside Orion — by hand, against the
  Jira REST API, with the operator's own credentials. It is named `close` rather
  than `publish` because `publish`, `cut` and `ship` stay reserved for the verb
  that would cut a binary. Safe to re-run: an already-released version is
  reported, not an error.
- The milestone is dated from the matching tag's commit rather than from the day
  you got round to closing it, so a version closed the morning after it shipped
  is not stamped with a day on which nothing was released. `--date YYYY-MM-DD`
  overrides.
- Closing a milestone that still holds unfinished tickets is refused, and the
  tickets are named. `--force` closes it anyway and says which ones it went past.

### Changed

- `orion new "<idea>"` is now the interactive front half of the flow: it
  interviews you about the idea — who it is for, what is wrong today, what
  success looks like, what is out of scope, what constrains it — has you
  finalise the project name, and creates the tracker project carrying that
  elaborated description. `orion plan <KEY>` then reads that description back
  as the statement of the work. A question you leave blank is recorded in the
  description as unstated rather than dropped, so a later stage can tell
  "nobody decided this" from "this does not apply".
- Creating the project goes through the same describe-then-confirm gate
  `orion init` uses, which says that a Jira project cannot be deleted without
  admin rights before it asks.
- A tracker project's description is now the one you wrote. It was previously
  the fixed string "Provisioned by Orion.", which is what `orion plan` was
  designing from.

- A tracker project now maps to exactly one workspace. A second `orion plan` on
  the same key refuses and names the existing workspace, rather than reusing it
  (which would carry a failed attempt's state into a fresh one) or creating a
  suffixed twin (which would mean two filesystem names for one project). To
  start over, remove the workspace explicitly with `orion rm <id>`. This changes
  nothing for `orion new "<idea>"`: two identical ideas still get two
  workspaces, because two typings of an idea are honestly two attempts.
- An unacknowledged budget checkpoint stops `orion plan` before it hands off to
  any dispatch, and says so. The workspace is still provisioned first, since
  creating a directory spends nothing — so acknowledging the checkpoint and
  re-running picks up where it stopped.

- The identical-repeat breaker no longer counts a wait as a loop. A read of a
  file a background command of yours is writing — even while it is still empty
  — an ask for a background task's output, and a read that returns something
  different from last time are all exempt. Re-reading a file nothing is writing
  still trips, unchanged: the fix makes the correct behaviour available rather
  than weakening the trip.
- A run that ends with its breaker tripped and uncommitted work now says so on
  the **ticket**, not only in the run output and the operator's channel, naming
  how many files it was holding and what became of them. Two tickets that ended
  this way read as ordinary failures until someone opened the worktree.
- Orion commits a tripped run's leftover changes instead of reverting them, and
  reverts only if the commit fails. Unverified work on a branch can be read,
  resumed or dropped by a person; reverted work cannot be anything.

- `orion watch` now ticks every 1 minute by default instead of every 2. The
  tick is what Orion waits on for anything it notices rather than causes — a
  green CI run, a merged PR, an approval, a newly queued ticket — so those now
  wait an average of 30 seconds rather than a minute. A tick is one tracker
  query and one PR status check: it starts no agent and spends no tokens, so
  this costs cheap polling and nothing else. `--interval S` is unchanged, and
  `--interval 0` (or a negative value) still means the default.

- The cost report is rendered as one visually bounded block, with an opening
  and closing rule that names it in plain words, so it can be told apart from
  the line-by-line output around it in a concurrent log. Both rules degrade to
  ASCII under `NO_COLOR` and on a non-UTF-8 locale, and carry no colour at all
  — the same report text goes to the terminal and to the tracker comment.

### Removed

- `orion new` no longer provisions a workspace, and no longer runs the intent
  conversation. `orion plan <KEY>` provisions the one workspace a tracker
  project gets, and now creates its Slack channel too. Recorded as
  `docs/decisions/0013`.
- `orion new --from`, `--template` and `--container` shaped that workspace and
  are now refused rather than silently ignored. Cloning an existing repository
  into a workspace has no front door until `orion plan` grows one; adopting
  Orion inside a checkout with `orion init` is unaffected.

### Fixed

- A QA run that verified everything and said so in prose is no longer sent
  back to the developer as findings. The `QA CLEAN` sentinel still decides a
  pass, unchanged — but a closing message that names neither the sentinel nor
  a failure is now an unknown verdict rather than a defect report, and Orion
  asks QA once more for a verdict line instead of dispatching a fix round on
  a verdict it does not have. The re-ask is one short turn on QA's own model
  and resumes its session, so it cannot damage a clean branch.
- When the re-ask still produces no verdict, the run escalates to a person and
  says the verdict was never obtained. It previously reported findings — the
  implementer was told to fix a defect nobody had described, and the cheapest
  way to satisfy that instruction is to weaken the nearest assertion, which
  makes CI greener rather than redder.

- Concurrent tickets no longer starve the longest-open branches. Rebasing every
  behind branch on the same pass grew with the square of the queue depth, and at
  `max_concurrent_tickets = 2` that was already enough to exhaust
  `collect.auto_rebase`'s two-rebase allowance on the two branches that had been
  open longest and hand them to a person. `maxAutoRebases` is unchanged and
  still bounds a runaway loop; a branch whose checks are green and whose base
  has not moved still lands immediately.
- A branch handed to a person is announced once rather than on every poll. The
  staleness warning and its three rebase commands were reprinted in full every
  couple of minutes for branches nobody had touched; now later polls say only
  that the branch is still behind and still theirs, and speak up again as soon
  as it moves.

- The locale-sensitive UI tests now state the locale instead of inheriting it.
  Three of them set only `LANG`, but `utf8Locale` reads `LC_ALL`, then
  `LC_CTYPE`, then `LANG` in POSIX precedence order and returns on the first
  one that is SET, so on a runner exporting a higher-precedence variable the
  test's own setting never reached the decision. They passed by luck rather
  than by construction. The identical mistake in the stage renderer's
  ASCII-degradation test failed only on macOS, where the runner exports
  `LC_CTYPE`, after passing locally and on Linux. All four now set the whole
  locale, so the test decides the result rather than the machine it runs on.

- The cost report no longer loses the usage of long runs. With
  `--output-format stream-json` the runner keeps emitting background-task
  frames after its own result frame, and the parser took the last JSON object
  on the stream rather than the result — so any run long enough to spawn
  background work reported zero turns and zero cost while short runs reported
  correctly. The report for OR-168 said $1.03 across six runs; the single
  implementer run it had dropped cost $13.40 on its own. The same
  mis-selection was silently taking a **subagent's** session id as the run's,
  which would have resumed the wrong conversation.
- A run that never started is now counted apart from a run that failed. An
  expired login makes the runner exit in seconds having opened no session and
  spent nothing; it used to appear as an ordinary failed run, inflating both
  the run count and the failure count, and it tripped the missing-usage floor
  warning over a run whose true cost is known to be zero.

- Pruning a worktree no longer refuses over `plans/BLOCKED.md`. The breaker
  writes that note when it trips, and it is untracked by design, so the
  deletion guard counted it as the operator's uncommitted work and kept the
  checkout of every tripped run — forever, since a file that is never tracked
  can never be committed away. Orion now recognises its own artefacts by name
  (`plans/BLOCKED.md` and `.orion/`) and does not count them as a reason to
  keep a merged worktree. Anything else untracked, and any change to a tracked
  file, still keeps the worktree exactly as before.
- `orion sandbox` reports uncommitted work using the same rule the deletion
  guard applies, so a worktree it lists as clean is one prune can actually
  remove. Both now ask git for untracked FILES rather than directories: a
  wholly untracked `plans/` was reported as a single `?? plans/` entry, which
  can be neither recognised as Orion's own nor safely ignored.
- Worktrees already stranded by this are swept with `orion sandbox prune`
  (`--dry-run` first to see the verdicts); the fix prevents new ones but
  removes nothing on its own.

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
