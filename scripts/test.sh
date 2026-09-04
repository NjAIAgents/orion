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
# The import-path fragment that decides which packages the floor is measured
# over. A filter applied to the profile rather than a package list handed to
# `go test`, because the tests and the coverage pass are now the SAME run
# (see "tests" below) and that run has to include cmd either way.
COVER_MATCH=${COVER_MATCH:-/internal/}

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
#
# One run, not two. This used to be a plain `go test ./...` followed by a
# second `go test ./internal/...` for the profile, which ran every internal
# package's tests twice for a number the first pass had already computed
# nothing from (OR-202).
#
# No -coverpkg: without it each package instruments only its own statements
# and is credited only to its own tests, which is exactly what the separate
# ./internal/... pass measured. Adding -coverpkg=./internal/... would credit
# cmd's tests to internal packages and move the number three points -- a
# different measurement, not the same one made cheaper.
# -timeout for the same reason release-gate.sh carries one: internal/work runs
# close to go's 600s per-package default, and a package that crosses it is
# killed and reported as FAIL with no test named. This script has been passing
# on luck rather than margin -- coverage instrumentation happens to change the
# timing -- and "it passed locally" has to mean the same thing here as it does
# on the release gate.
# -p bounds how many PACKAGES run at once (OR-332).
#
# Go defaults it to GOMAXPROCS, and this suite is not CPU-bound: sixteen
# packages spawn real subprocesses -- git, gh, claude, the agent's own shell
# -- so the default puts several process-heavy packages on the runner at the
# same time and they starve each other. The tests that lose are the ones with
# a clock in them: a wall-clock kill that waits for a grandchild to report its
# pid, a watch loop expected to complete two passes at a millisecond interval.
#
# That is why macOS failed about two runs in five while Linux passed: GitHub's
# macOS runner has three cores to Linux's four and is slower per core, so the
# same default oversubscribes it further. Each failure named a DIFFERENT test,
# which is the signature of contention rather than of a broken assertion.
#
# Two, not one: the suite still overlaps the many packages that are pure
# computation, and a serial run costs several minutes on every leg. Raising
# this is a decision about the slowest runner, not about this laptop.
: "${TEST_PARALLEL_PACKAGES:=2}"
go test ./... -p "$TEST_PARALLEL_PACKAGES" -timeout 20m \
  -coverprofile=coverage.raw.out -covermode=atomic

step "coverage"
# Drop the packages the floor deliberately excludes. The profile keeps its
# mode line (first line) or `go tool cover` cannot read it.
{
  head -1 coverage.raw.out
  grep "$COVER_MATCH" coverage.raw.out || true
} > coverage.out

if [ "$QUICK" = 0 ]; then
  step "race detector"
  # Orion writes the event log and the ring buffer from more than one
  # goroutine, and a data race there corrupts the record of what happened --
  # which is exactly the thing you need when something has gone wrong.
  # internal/state also shares a state dir across parallel worktree
  # sessions, and internal/procsafe (OR-138) is the lock those sessions and
  # any other same-ORION_HOME process rely on to not tear each other's writes.
  #
  # internal/watch, internal/workspace and internal/budget joined the list
  # with OR-184: the watcher now works several tickets at once, so its job
  # pool, the mutex serialising git against the one shared clone, and the
  # budget reservation that admits a run are all live under concurrency. A
  # data race in any of them is either a corrupted repository or spend that
  # nothing accounted for.
  #
  # -race needs cgo, so this step cannot run with CGO_ENABLED=0. CI runs it
  # on real hosts, where that's a non-issue -- don't "fix" it to match the
  # release build (Makefile), which stays CGO_ENABLED=0 on purpose for its
  # six-target cross-compile that has no local C toolchain to link against.
  go test -race ./internal/events/ ./internal/supervisor/ ./internal/state/ \
    ./internal/procsafe/ ./internal/watch/ ./internal/workspace/ ./internal/budget/
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
