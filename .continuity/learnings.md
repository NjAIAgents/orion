# Learnings

Append-only, oldest first. Discoveries worth not re-making, plus the
established conventions of this project.

## Learning: Continuity's error log is unwritable on the one path that needs it

```
captured_at: 2026-09-22T16:53:13Z
category: learning
```

`tests/test_migrate.py:227`
(`test_write_denied_during_migration_fails_gracefully`) is the single failure
in an otherwise green suite (289/290 as of 2026-09-22). It is a real gap, not
a root-user artifact — the `skipIf(os.geteuid() == 0)` guard does not fire
here (uid 501).

The test chmods `.continuity/` to `0o555`, then asserts `write-failed` lands
in `errors.log`. Migration correctly refuses to write and returns `False`, so
the fail-open behaviour is right. But the same directory permission that
blocks the migration also blocks appending to `errors.log`, so the diagnostic
is lost: the failure logger is locked out by the exact condition it exists to
report.

Consequence: a read-only `.continuity/` fails open silently, leaving no trace
for anyone debugging why context stopped loading. Any fix has to put the
record somewhere the denied permission does not reach — stderr, or a path
outside the store — rather than making the log write "try harder".
