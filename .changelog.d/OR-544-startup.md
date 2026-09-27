### Fixed

- **`orion watch` starts with a five-line banner** instead of ten, and nothing
  in it wraps mid-word.
- **A local build is no longer offered the release it was built after.** A
  build from `develop` is versioned `v0.11.0+dev.<sha>` (build metadata), not
  `v0.11.0-dev+<sha>`, which semver sorted *before* v0.11.0.
- **The watch board reads correctly under a red batch.** A failed check turns
  the batch red and its CI step shows ✗; "went red" prints as a failure, not a
  warning; an ejected ticket leaves the member list and shows as "ejected, next
  batch: KEY (conflict in FILE)".
- **Blocked tickets are grouped by blocker on the board** ("12 on LTA-30 · 10 on
  LTA-137"), queue counts add up (waiting · in integration · landed · failed),
  long key lists in the scroll shorten to "first … last (N tickets)", and the
  board's rules span the terminal.
