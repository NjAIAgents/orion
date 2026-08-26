#!/usr/bin/env bash
#
# The single entry point for running Orion's checks.
#
# One script, called by CI and by hand, so "it passed locally" and "it passed
# in CI" mean the same thing. A CI workflow that inlines its own commands
# drifts from what developers run, and the drift is only discovered when a
# green local run fails on the runner -- or worse, the reverse.
#
#   ./scripts/test.sh              build, vet, gofmt, tests, race, coverage
#   ./scripts/test.sh --quick      skip the race detector (much faster)
#   ./scripts/test.sh --cover-only print the coverage table and stop
#
# Exits non-zero on the first failure, so CI stops at the real cause rather
# than reporting five downstream symptoms.

set -euo pipefail

cd "$(dirname "$0")/.."

QUICK=0
COVER_ONLY=0
for arg in "$@"; do
  case "$arg" in
    --quick)      QUICK=1 ;;
    --cover-only) COVER_ONLY=1 ;;
    *) echo "unknown option: $arg" >&2; exit 64 ;;
  esac
done

# Coverage floor. Not a target to game -- a ratchet, so a change that guts a
# tested package fails loudly instead of quietly lowering the bar. Raise it
# when real coverage rises; never lower it to make a build pass.
# Measured over ./internal/... only. cmd/orion is 1600 lines of flag parsing
# and printing that is exercised end to end by hand; including it would drag
# the number down by fifteen points and tell you nothing about whether the
# logic is tested. The floor should track the code where a bug costs money.
MIN_COVERAGE=${MIN_COVERAGE:-65}
COVER_PKGS=${COVER_PKGS:-./internal/...}

step() { printf '\n\033[1m==> %s\033[0m\n' "$1"; }

if [ "$COVER_ONLY" = 0 ]; then
  step "gofmt"
  # gofmt -l prints files that WOULD change. Empty output is the pass
  # condition; a non-empty list is a failure even though gofmt exits 0.
  unformatted=$(gofmt -l . | grep -v '^vendor/' || true)
  if [ -n "$unformatted" ]; then
    echo "these files are not gofmt'd:" >&2
    echo "$unformatted" >&2
    echo >&2
    echo "fix with: gofmt -w $unformatted" >&2
    exit 1
  fi
  echo "clean"

  step "build"
  go build ./...

  step "vet"
  go vet ./...
fi

step "tests"
# Everything, including cmd, so a compile error or a broken CLI test fails
# the build even though cmd is excluded from the coverage floor.
go test ./...

step "coverage"
go test $COVER_PKGS -coverprofile=coverage.out -covermode=atomic

if [ "$QUICK" = 0 ]; then
  step "race detector"
  # Orion writes the event log and the ring buffer from more than one
  # goroutine, and a data race there corrupts the record of what happened --
  # which is exactly the thing you need when something has gone wrong.
  go test -race ./internal/events/ ./internal/supervisor/ ./internal/state/
fi

step "coverage by package"
go tool cover -func=coverage.out | sed 's|github.com/orion-sdlc/orion/||'

total=$(go tool cover -func=coverage.out | awk '/^total:/ {gsub(/%/,"",$3); print $3}')
printf '\ntotal coverage: %s%% (floor %s%%)\n' "$total" "$MIN_COVERAGE"

# Compare without bc: it is not installed everywhere, and a check that
# silently no-ops on a runner missing a tool is worse than no check.
if awk -v t="$total" -v m="$MIN_COVERAGE" 'BEGIN { exit (t >= m) ? 0 : 1 }'; then
  echo "coverage floor met"
else
  echo "coverage $total% is below the $MIN_COVERAGE% floor" >&2
  exit 1
fi
