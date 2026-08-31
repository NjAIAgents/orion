# 0015: Orion is the gate under a merge ref, and develop keeps a post-merge check

- Status: Accepted (by Navjyot, 2026-08-30; recorded as Accepted before review, see OR-237)
- Date: 2026-08-30
- Tickets: OR-237 (this decision), OR-236 (the change it unblocks)
- Related: [0001](0001-precedence-rule-orion-owns-orchestration.md) (Orion owns
  sequencing across stages), [0011](0011-orion-owns-the-landing-queue.md)
  (Orion owns the landing queue; GitHub's merge queue is not adopted)

## Context

OR-236 proposes running CI once on an ephemeral **merge ref** — several
branches merged together — instead of once per branch's pull request. The
motive is the one [0011](0011-orion-owns-the-landing-queue.md) left standing:
CI duration (OR-202) is the multiplier on every landing cycle, and the queue
removed the quadratic term without touching that multiplier.

The objection is that this moves "may this land" out of GitHub branch
protection and into Orion, trading a server-side guarantee that survives a bug
in Orion for one that does not. That objection deserves an ADR. It also
overstates what is there today, and understates what OR-236 breaks.

**What actually gates `develop` today.** Not branch protection, on the
repositories Orion runs on. `internal/collect/staleness.go` records the 403
verbatim: protection is unavailable for private repositories on the free plan,
"which is every repository Orion currently runs on". `orion protect` applies a
real ruleset where the plan allows it and deliberately names only checks the
repository has been *observed* to run — but where it cannot apply, nothing
server-side decides what lands. The authority OR-237 is worried about moving is
already Orion's on the current fleet. This ADR records that rather than
introduces it.

**What Orion reads instead.** `cmd/orion/collect.go` asks gh for the branch's
`statusCheckRollup` and requires every reported check to be green. Two
properties of that path matter here:

- An empty rollup is `VerdictPassing`, with the detail
  "no checks are configured on this repository". That is right for a
  repository without CI and catastrophic for a pull request whose CI has
  deliberately moved somewhere else. Under a merge ref, a member pull request
  has no checks of its own, so today's code would read every one of them as
  green on no evidence at all.
- `auto_merge.require_checks` is declared in `internal/config/config.go`,
  documented in `templates/orion.json`, and read by nothing. The named-check
  contract people assume protects `develop` is not implemented; the all-green
  rollup is what protects it. Removing the rollup therefore removes the whole
  gate, not one of two.

**The near miss of 2026-08-30** (OR-234, observed on OR-217) is evidence about
a different thing, and is worth being exact about: CI went green on a pull
request that did not contain the failing test sitting on disk beside it. That
was a failure of what the branch *carried*, not of where CI *ran*. A merge ref
neither causes it nor fixes it. What it does do is remove the per-branch
signal, so the same class of defect has one fewer place to become visible.

## Decision

**The hybrid.** Orion gates the batch; GitHub still runs the checks on
`develop` after the merge.

- **Orion is the authority on "may this land."** Deciding what lands, and in
  what order, is sequencing across stages, which is Orion's per
  [0001](0001-precedence-rule-orion-owns-orchestration.md) and already Orion's
  in practice per [0011](0011-orion-owns-the-landing-queue.md).
- **`develop` runs the same checks on push, after the merge.** This is a
  workflow trigger, not branch protection: it costs no plan, needs no admin
  rights, and works on the free private repositories where protection returns
  403. That is exactly why it can be relied on where protection cannot. It
  **detects** a bad land rather than preventing one, and the window is one CI
  run wide.
- **An empty check rollup stops reading as passing** once a merge ref is in
  play. OR-236 must make `collect.go` distinguish "this repository has no CI"
  from "this branch's CI ran on a ref Orion has not read yet"; only the first
  is passing. Without this guard the change is not a reduction in defence
  depth, it is the removal of the gate, and it would look green while doing it.
- **`auto_merge.require_checks` becomes load-bearing** and names the checks
  that must be green on the ref CI actually ran. A declared-and-unread field is
  survivable today because the all-green rollup covers for it; under a merge
  ref there is no rollup to cover for it.
- **`vcs.protected_branches` keeps its meaning and its job.** It is Orion's own
  local gate — `internal/hook/gate.go` refuses a direct push to a protected
  branch — and it is what `orion protect` applies where the plan allows. What
  it stops being is the thing that decides whether *tested* code lands.
- **The approval message says where validation happened.** `slackmsg.go`'s
  "%s is ready to merge — approve?" must name the merge ref and the sibling
  branches that shared it. A human approving a change validated in combination
  with three others is approving something different from what that message
  currently describes, and the honest sentence is cheap.

**Not adopted: pushing the merge ref as a real branch with its own protected
pull request.** It buys prevention only where protection is available, which is
nowhere Orion currently runs; it adds a second pull request per batch for a
human to reason about; and re-serialising the thing OR-236 exists to
parallelise gives back the saving that motivated it.

## Consequences

- **A bug in Orion can land unverified work.** This is a real reduction in
  prevention, accepted deliberately. What the post-merge check buys is that the
  bad land is *detected* rather than silent, within one CI run, by a mechanism
  that does not share Orion's failure modes.
- **A red `develop` needs a response path, or it is a detector nobody reads.**
  OR-236 must define what happens when the post-merge check fails — who is
  told and what stops. This ADR requires the path to exist; it does not pick
  the mechanism.
- **`orion protect` must not require a check that only the merge ref reports.**
  `cmd/orion/reposettings.go` already states the failure mode: a required check
  that never reports blocks every pull request forever, with no timeout and no
  recovery without an admin in the settings UI. Under a merge ref that stops
  being a theoretical misconfiguration and becomes the obvious one, since the
  checks a member pull request runs and the checks that gate it are no longer
  the same set.
- **A red merge ref does not say which branch broke it.** Attribution across a
  batch is a cost OR-236 has to carry, not one this decision resolves.
- **Reversible in one direction only.** Going back to per-branch CI is a
  configuration change; the post-merge check on `develop` is worth keeping
  either way, because it is the only gate on the current fleet that does not
  depend on Orion being correct.
