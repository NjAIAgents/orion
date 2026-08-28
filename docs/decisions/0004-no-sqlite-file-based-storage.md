# 0004: No SQLite — config, event logs and the audit trail stay as files

- Status: Accepted
- Date: 2026-08-28
- Tickets: OR-138 (file-locking rationale, folded in here)

## Context

Introducing SQLite for config, event logs and the audit trail was
considered, as an alternative to plain files plus cross-process locking.

Separately, and the reason locking exists at all: several Orion processes
share one `ORION_HOME` — the supported workflow is `orion watch A` and
`orion watch B` running concurrently, one per project repository. The
shared mutable state under `ORION_HOME` (`usage.json`, `repos.json`) was
load-modify-save with an atomic rename but no lock, and both used a fixed
temp path. Two processes therefore raced twice over: on the temp file
itself, and on the read-modify-write, where last-rename-wins silently
dropped one process's update — measured at one run recorded out of twelve
concurrent writers (OR-138).

## Decision

No SQLite. Config, event logs and the audit trail stay as plain files.
Cross-process safety for the shared state under `ORION_HOME` comes from
`internal/procsafe`'s directory-based advisory lock (`os.MkdirAll`, not
`flock(2)`), lifted out of `internal/state`'s hook-path lock so budget and
the repo registry share one implementation instead of growing a second one.

Reasons against SQLite:

- **Hand-editable, greppable, diffable.** A plain file can be `cat`'d,
  `grep`'d and diffed directly; a SQLite file cannot.
- **`orion.json`'s `_comment_*` keys and `~/.orion/agents.json`'s design
  depend on it.** Config is meant to carry inline explanations next to the
  values they explain — there's no equivalent of a comment in a database
  row.
- **It would be Orion's first real dependency.** `mattn/go-sqlite3` needs
  cgo, which breaks the `CGO_ENABLED=0` six-target cross-compile the
  release Makefile depends on — Orion ships as one static binary, no cgo,
  empty `go.sum`, builds offline. `modernc.org/sqlite` avoids cgo but is
  large, working against the same goal from the other direction.
- **Transactional integrity is not tamper-evidence.** SQLite guarantees a
  write completed correctly; it says nothing about whether a later actor
  rewrote history undetected. Tamper-evidence — not transactional integrity
  — is the property the audit trail actually needs.

Why `mkdir` rather than `flock(2)` for the lock itself: `os.MkdirAll` is
atomic on every platform Orion ships to, while `flock` is not available on
Windows without cgo or syscall build tags — the same cross-compile
constraint above. A lock that cannot be taken degrades to an unserialized
write that reports it (`ErrLockTimeout`), and never blocks a watcher or
returns a nil release function: a crash must not wedge every later
invocation.

## Consequences

- Any future shared mutable state under `ORION_HOME` goes through
  `internal/procsafe` rather than growing its own locking scheme.
- A feature that seems to want relational queries over Orion's own data
  (e.g. ad hoc audit queries) should reach for grep/jq-friendly file
  formats, or build an in-process index from the files at read time — not
  a database — unless this decision is revisited.
- No-cgo, offline-buildable, six-target cross-compile remains a hard
  constraint on any future dependency choice, not just this one.
