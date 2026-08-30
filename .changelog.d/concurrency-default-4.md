### Changed

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
