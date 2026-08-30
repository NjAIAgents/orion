
## breaker/unverified-edits tripped

- when: 2026-08-30T19:19:41Z
- session: `c7542f25-e779-4d0b-b967-27ecada8a19b`
- detail: 25 edits with no passing verification

Written by the breaker at the moment it tripped, so this record exists even
if the session ends on the very next call.

The agent should add what it was attempting, what is done and what remains.
It may still run `git status`, `git diff`, `git checkout -- <path>`,
`git restore`, `git add` and `git commit` to leave the worktree reportable.

Resume after review with `orion reset --session c7542f25-e779-4d0b-b967-27ecada8a19b`.

### What was being attempted (OR-148)

`orion new` becomes the interactive front half: elaborate the idea, finalise
the project name, create the Jira project through
`adopt.RemotePlan.Describe()`'s existing describe-then-confirm gate.

The ticket's open question was settled and recorded as
`docs/decisions/0012-new-keeps-the-workspace.md`: `new` creates the tracker
project **and** keeps provisioning the workspace, because the tracker is
detect-never-require everywhere else in Orion and a `new` that wrote nothing
locally would hold the only interactive exchange in the system and, on an
unconfigured machine, have nowhere to put the answer.

### What is done

Complete and green before the trip (`./scripts/test.sh` exited 0, including
the race detector, coverage 76.2% against a 65% floor):

- `internal/discovery/interview.go` — the interview: four questions grounded
  in what `NeedsDiscovery` reports missing, then the project name. A blank
  answer becomes an open question in the rendered description rather than
  being dropped, so the existing `Assess` gate still sees it.
  Tests in `internal/discovery/interview_test.go`.
- `internal/tracker` — `CreateProject` and `Provision` carry a description;
  an empty one falls back to the old "Provisioned by Orion." marker.
  Tests in `internal/tracker/createproject_test.go` and `tracker_test.go`.
- `internal/workspace` — `NewOptions.Name`/`.Description`, `Task.Name`/
  `.Description`; the canonical slug now derives from the finalised name
  (decision 0009). Tests in `internal/workspace/lifecycle_test.go`.
- `cmd/orion/newcmd.go` — `runNew`, `elaborate`, `provisionTracker`,
  `newProjectPlan`, `createProjectFromPlan`. The Jira key is resolved BEFORE
  the plan is described, so the key confirmed is the key created.
  Tests in `cmd/orion/newcmd_test.go`.
- `cmd/orion/main.go` — old `runNew` removed; `orion provision` refuses to
  create a second tracker project for a workspace that already has a
  binding; usage text updated.
- Docs: `docs/decisions/0012-*`, amendment to `0006-*`, README/USAGE,
  `commands/start.md`, `commands/provision.md`, `.changelog.d/OR-148.md`.

### What remains — one edit

The build is RED on exactly one line. `runNew` was changed to
`intake, asked := elaborate(idea, rest)` so the "N questions went unanswered"
notice is not printed after `--skip-discovery`; the matching change to
`elaborate`'s signature was refused by the breaker mid-way.

In `cmd/orion/newcmd.go`, change `elaborate` to return `(discovery.Intake, bool)`:

- signature: `func elaborate(idea string, rest []string) (discovery.Intake, bool)`
- the `--skip-discovery` case returns `flat, false`
- the non-terminal case returns `flat, false`
- the final line returns
  `discovery.Interview(os.Stdin, os.Stdout, idea, proposed, needs), needs`

Then `./scripts/test.sh`. Nothing else is outstanding.
