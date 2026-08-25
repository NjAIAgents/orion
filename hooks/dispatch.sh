#!/usr/bin/env bash
# Single entry point for every Orion hook.
#
# Why a shim rather than calling the binary directly: the plugin ships as
# a zip and cannot contain platform binaries, so the binary is installed
# separately. This script is the seam. It also means every hook wiring in
# hooks.json is one line, and swapping the implementation is a change here
# rather than in five places.
#
# Exit code contract (Claude Code):
#   0  allow      2  BLOCK, stderr goes to the model     other  non-blocking
#
# A missing binary must NOT block the session. Blocking every tool call
# because a guardrail is not installed would make Orion the outage. It
# warns loudly on exit 1 instead, and `orion doctor` is the place that
# fails hard.
set -uo pipefail

HOOK="${1:-}"
if [[ -z "$HOOK" ]]; then
  echo "orion: dispatch.sh needs a hook name" >&2
  exit 1
fi

BIN="${ORION_BIN:-}"
if [[ -z "$BIN" ]]; then
  BIN="$(command -v orion 2>/dev/null || true)"
fi
if [[ -z "$BIN" ]]; then
  for candidate in "$HOME/.local/bin/orion" "/usr/local/bin/orion" "$HOME/go/bin/orion"; do
    [[ -x "$candidate" ]] && BIN="$candidate" && break
  done
fi

if [[ -z "$BIN" ]]; then
  cat >&2 <<'MSG'
orion: binary not found. GUARDRAILS ARE NOT RUNNING for this session.

  The plugin ships the workflow; the enforcement lives in a separate binary.

    git clone https://github.com/orion-sdlc/orion && cd orion && make install

  Or set ORION_BIN to its path. Verify with: orion doctor
MSG
  exit 1
fi

exec "$BIN" hook "$HOOK"
