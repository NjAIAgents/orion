# OR-143 -- blocked by the session's loop breaker

The breaker tripped ("Read repeated 4 times") partway through implementing
OR-143 and is now refusing every tool call in this session, including the
Bash/build path the breaker message itself says should still work. Per the
breaker's own instruction ("do not retry, do not work around it"), stopping
here rather than hammering the tool again.

Resume with: `orion reset --session b3acb7d2-5687-4e79-88d2-9e27bea93cfe`

## What OR-143 is

Level 3 parallelism: fan CI-log triage out to a subagent inside the fix loop,
so the raw failing-CI log is read once by a cheap subagent and only its short
report reaches the fix run's prompt, instead of the whole log riding along on
every turn of that run. This is the same work as OR-137's log-triage child --
done together per the ticket, not twice.

## What is already applied to the working tree (verify with `git diff`)

1. `internal/events/events.go` -- added
   `ActorLogTriage = "log-triage" // reads a failing CI log, reports what broke and why`
   next to the other Actor* constants.

2. `internal/actors/actors.go` -- added the roster entry (in `defaults()`):

   ```go
   // Log triage reads a failing CI log and reports what broke, so the fix
   // run that follows carries that report instead of the raw log riding
   // along on every turn (OR-143). Haiku: the reading is mechanical --
   // find the failure in a wall of output -- not the judgement the fix
   // itself takes, and it runs on every red build, so it is the one
   // actor for which the model is a real cost decision besides the
   // developer.
   events.ActorLogTriage: {ID: events.ActorLogTriage, Name: "Milo", Designation: "log triage", Model: "haiku"},
   ```

   (Reformatted the surrounding map literal's alignment for `ActorCI` /
   `ActorHuman` since the new key is longer -- gofmt will want to redo this
   alignment anyway; run `gofmt -w internal/actors/actors.go` after any
   further edits.)

3. `internal/supervisor/prompts.go` -- added `LogTriagePrompt(branch, log string) string`,
   inserted directly above `TicketPrompt`. It asks a subagent to report which
   check failed, the specific error with file:line, and a one-two sentence
   root-cause read, explicitly "read only, do not edit/run/commit".

4. `cmd/orion/fix.go` -- reordered `fixRun` so `cfg`/`jobWS` are built before
   `detail` is computed, and changed the `detail` assignment to:

   ```go
   detail := failure
   if full := failingLog(dir, branch); strings.TrimSpace(full) != "" {
       detail = triageLog(&jobWS, key, branch, full)
   }
   ```

   **This edit is in place but `triageLog` and `triageOptions` do not exist
   yet in fix.go -- the package currently fails to compile.** The next step,
   blocked by the breaker, was inserting this block into `cmd/orion/fix.go`
   right before the `oneLine` function:

   ```go
   // Bounds for the log-triage subagent, deliberately far tighter than the fix
   // run's own cfg.Limits: this is a mechanical read-and-report, not the work of
   // fixing anything, and a run that gets stuck hunting is a run that has
   // stopped being cheap.
   const (
       triageMaxMinutes = 5
       triageMaxTurns   = 10
   )

   // triageOptions is what the log-triage subagent runs with, separated from
   // triageLog so the actor, model and prompt it is configured with can be
   // asserted without spawning a process -- the same reason fixOptions is split
   // from fixRun.
   func triageOptions(key, branch, log string) supervisor.Options {
       return supervisor.Options{
           Stage:      "log-triage",
           Prompt:     supervisor.LogTriagePrompt(branch, log),
           MaxMinutes: triageMaxMinutes,
           MaxTurns:   triageMaxTurns,
           // Its own actor and its own model, pinned cheap rather than
           // inherited from the fix run: this is a mechanical read, not the
           // implementer's judgement, and pinning it is what makes the split
           // a cost win instead of a second expensive run (OR-143). Attributed
           // to the same ticket key so its spend shows up in that ticket's cost
           // report rather than hiding inside the fix run's total.
           Actor: events.ActorLogTriage, Key: key,
           Model:  actors.Model(events.ActorLogTriage),
           Effort: actors.Effort(events.ActorLogTriage),
       }
   }

   // triageLog reduces a failing job's raw log to a short report of what broke
   // and why, through a subagent that reads the log in its own context and
   // returns only its answer -- the log itself never reaches the fix run.
   //
   // Falls back to the raw log on any failure of the subagent itself: the fix
   // run still needs something to react to, and a raw log the agent has to work
   // harder to read loses less than a triage step that silently produced
   // nothing.
   //
   // The report is logged the same way OR-129 made the fix loop report its own
   // closing summary: what a subagent returns is all the parent session ever
   // sees of it, so if that answer is not written down anywhere, it is gone the
   // moment this function returns.
   func triageLog(jobWS *workspace.Workspace, key, branch, log string) string {
       res, err := supervisor.Run(jobWS, triageOptions(key, branch, log))
       if err != nil || res == nil || strings.TrimSpace(res.Final) == "" {
           return log
       }
       report := strings.TrimSpace(res.Final)

       if l, openErr := events.Open(events.Path(jobWS.Dir), events.Event{}); openErr == nil {
           defer func() { _ = l.Close() }()
           l.Emit(events.Event{Kind: events.KindNote, Actor: events.ActorLogTriage, Key: key,
               Msg: "triaged the failing log: " + oneLine(report)})
       }
       return report
   }
   ```

   `cmd/orion/fix.go` already imports everything this needs (`actors`,
   `events`, `supervisor`, `workspace`, `strings`) -- no import changes
   required for this piece.

## What is still undone after that

- `gofmt -w` the touched files (alignment in actors.go and events.go will
  have drifted from the longer `ActorLogTriage` identifier).
- `go build ./...` and `go vet ./...` to confirm everything above actually
  compiles together -- untested because the breaker blocked it.
- Tests (none written yet):
  - `internal/actors/actors_test.go`: a test that `events.ActorLogTriage` is
    in the roster (Name/Designation non-empty), defaults to model `"haiku"`,
    and is configurable (not in `fixed`).
  - A test for `LogTriagePrompt` (e.g. in `internal/supervisor/testenv_test.go`
    or a new file) asserting it carries the branch name and the log text and
    says "read only" / "do not edit".
  - `cmd/orion/fix_test.go`:
    - a pure test of `triageOptions` (no process spawn) asserting `Actor ==
      events.ActorLogTriage`, `Key == key`, `Model ==
      actors.Model(events.ActorLogTriage)`, and that the prompt contains the
      branch and the log text -- mirrors the existing `fixOptions` split
      rationale.
    - an integration test using the `fakeClaude`-on-PATH pattern from
      `internal/supervisor/supervisor_test.go` (a stub `claude` script on a
      temp-dir PATH, a workspace built the same way `supervisor_test.go`'s
      `ws()` helper does: `.orion/logs`, `.orion/state`, `repo` dirs under
      `t.TempDir()`, with `ORION_HOME` set to that temp dir) to check:
        - success path: `triageLog` returns the subagent's `result` text, and
          `events.Read(events.Path(jobWS.Dir))` contains a `KindNote` event
          with `Actor == events.ActorLogTriage` naming the report.
        - fallback path: a `claude` stub that exits non-zero (or never emits
          a `result` line) makes `triageLog` return the original raw log
          unchanged.
- `.changelog.d/OR-143.md` -- not written yet. Draft:

  ```md
  ### Added
  - The CI fix loop now triages a failing job's log through a cheap subagent
    before handing it to the fix run, so the raw log no longer rides along on
    every turn of the fix -- only a short report of what broke and why. The
    triage run is attributed to its own "log triage" actor (haiku by default,
    configurable like any other agent) and its cost shows up as its own row in
    the ticket's cost report.
  ```

- `./scripts/test.sh` has not been run at all this session.
- No commit has been made yet.

## Everything else already investigated (do not re-derive)

- The right hook point for this is `cmd/orion/fix.go`'s `fixRun`, specifically
  where `detail` is built from `failingLog(dir, branch)` before being handed
  to `fixOptions` / `supervisor.FixPrompt`.
- `supervisor.Run` already IS the subagent primitive: it spawns `claude -p`
  as its own process with its own context, and when `Options.Actor`/`Key` are
  set it already records cost per-actor into the ticket's event log via
  `recordTicketCost` (`internal/supervisor/supervisor.go`) -- so pinning a
  distinct `Actor` for the triage call gets per-actor cost attribution for
  free through the existing `internal/cost` aggregation, no new plumbing
  needed there.
- The roster (`internal/actors/actors.go`) is the single place actor names
  may appear (enforced by
  `TestNoDefaultNameAppearsOutsideTheRegistry`) -- do not put "Milo" anywhere
  else, including prompts.
- Scope: only the CI-log-triage candidate from OR-143 is being implemented.
  The ticket also mentions "repository exploration during planning" and "QA
  deriving test cases" as candidates for the same subagent pattern, but only
  log-triage is explicitly tied to already-filed work (OR-137 child 4) that
  this ticket says to do together; the other two are left alone as future,
  separately-scoped work.
