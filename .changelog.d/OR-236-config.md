### Added

- **`orion config collect`** shows how work lands and sets the switches that
  decide it, so `batch_integration` and `auto_rebase` are reachable by command
  rather than by hand-editing `orion.json`. `auto_rebase` never had one.
- Turning a switch ON prints what it costs before you walk away from it. A
  toggle whose consequences are unstated is one people flip to see what
  happens.
- It writes the `collect` block if the file has none, which every config
  written before this does. Telling an operator to add one by hand would be
  the command instructing them to do the exact thing it exists to replace.

### Removed

- `collect.batch_size`. A batch can only hold branches that finished, and no
  more can finish than `limits.max_concurrent_tickets` allowed to run, so a
  second number could only ever disagree with the first about the same thing.
