### Added

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
