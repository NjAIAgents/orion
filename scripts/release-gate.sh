#!/usr/bin/env bash
# Run the release gate -- build, vet, gofmt, tests -- from the repository root.
# Quiet on success. On failure: which step, and what it printed.
#
# WHY THIS IS ITS OWN SCRIPT (OR-259). The gate is the last thing standing
# between a merged promotion and a tag, and it is the only part of a release
# that can fail for a reason nobody can reconstruct afterwards. Inside
# release.sh it sits behind a preflight that wants gh, a network and a clean
# tree, so the one wording that matters most was the one no test could reach.
# As its own script it runs against a deliberately broken tree in a second.
#
# WHAT WENT WRONG BEFORE. The gate ended in `go test ./... >/dev/null`, so the
# slow step -- the only one likely to fail non-obviously -- was the single
# step whose output was thrown away, and `set -e` then exited with a bare
# status. The v0.8.9 release stopped here with the promotion already merged,
# and the entire operator-facing account of it was "exit status 1". By the
# time anyone looked, main was green again: transient, unreproducible and
# undiagnosable, because the evidence was discarded at the moment it existed.
#
# Success stays as quiet as it was. A gate that prints a thousand lines of
# passing test output has hidden its failures as effectively as one that
# discards them.

set -euo pipefail

# How much of a failing step's output to show. Enough to reach the FAIL lines
# at the end of a Go test run, short enough that the line naming the step is
# still on screen above it.
TAIL_LINES=${ORION_GATE_TAIL:-40}

# gate NAME cmd...: run one step with its output captured, and on failure say
# which step it was and show what it printed.
#
# Naming the step is not decoration. "exit status 1" out of a four-command
# gate leaves the operator four guesses -- a flaky test, a parallel-test
# collision, a stale build cache, an environment difference -- and they call
# for different responses. The three cheap steps used to name themselves; the
# expensive one did not.
gate() {
  local name="$1"; shift
  local out
  if ! out="$("$@" 2>&1)"; then
    echo "    FAILED: $name" >&2
    printf '%s\n' "$out" | tail -n "$TAIL_LINES" >&2
    exit 1
  fi
}

# gofmt exits 0 whether or not anything would change; the file list IS the
# failure. So it needs a wrapper rather than being handed to gate bare.
gofmt_clean() {
  local unformatted
  unformatted="$(gofmt -l .)" || return 1
  [ -z "$unformatted" ] || {
    echo "gofmt would change:"
    echo "$unformatted"
    return 1
  }
}

gate "go build ./..." go build ./...
gate "go vet ./..."   go vet ./...
gate "gofmt -l ."     gofmt_clean
# -count=1 on the release gate specifically. Everywhere else a cached pass is
# a feature; here it means a green gate can be a green gate from an hour ago,
# and afterwards a cached pass and a fresh failure look identical. A release
# is the wrong place to be reading a cache.
# -timeout, because go's per-package default is 600s and internal/work takes
# about 645s. A package that exceeds it is KILLED and reported as FAIL with a
# goroutine dump and no test named, so the gate blamed whichever tests were
# mid-flight -- twice, on v0.8.10, while every one of them passed given time.
# Measured, not guessed: 600s default FAILs at 600.4s; -timeout 20m passes at
# 645.5s with zero failures.
#
# The number is generous on purpose. It is a CEILING that catches a genuinely
# hung test, not a target: shortening the suite is OR-264's job, and a limit
# tuned close to the current runtime would fail again the next time a test is
# added.
gate "go test -count=1 ./..." go test -count=1 -timeout 20m ./...

echo "    all green"
