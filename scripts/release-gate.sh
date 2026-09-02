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

# THE TESTS ARE VERIFIED, NOT RE-RUN.
#
# This gate used to run the whole suite a third time. CI already runs it on
# ubuntu, macos and windows for every push, so a local pass added exactly one
# thing those three cannot give -- proof it works on THIS machine -- and one
# thing nobody wanted: this machine's flakiness.
#
# v0.8.10 is the evidence. The gate failed four times and not once on a real
# defect:
#
#   1. internal/work takes ~645s; go's per-package default is 600s, so the
#      package was killed and reported FAIL with no test named.
#   2. scripts/test.sh had the same gap, hidden because -coverprofile shifts
#      the timing under the limit.
#   3. A test asserted on a message that ui.Say clips to COLUMNS, so it
#      passed on a wide terminal and failed on a 100-column one.
#   4. A process-tree test waited 2s for a grandchild to write its pid file,
#      which is not enough while internal/work is spawning hundreds of git
#      processes beside it.
#
# Every one of those is an artefact of running the suite HERE, and each cost
# a twenty-minute round trip to find. CI reported success on the same commit
# throughout.
#
# So the question this step asks changed from "do the tests pass on my
# laptop" to "did CI pass on the exact commit being tagged" -- which is the
# stronger claim, because it is three platforms rather than one.
#
# The build, vet and gofmt steps above stay. They are seconds, they catch a
# dirty or half-merged tree, and they are the part a local gate is actually
# good for.
ci_green_for_head() {
  local sha state
  sha="$(git rev-parse HEAD)"
  # A release is cut from a branch CI builds on push. No answer at all is a
  # refusal, not a pass: an unbuilt commit is exactly what this gate exists
  # to stop, and "gh is missing" must never read as "CI was green".
  if ! command -v gh >/dev/null 2>&1; then
    echo "gh is not installed, so CI's verdict for $sha cannot be read."
    echo "Install gh, or run the suite by hand and re-run with ORION_SKIP_CI_CHECK=1."
    return 1
  fi
  state="$(gh run list --commit "$sha" --json conclusion,name \
    --jq '[.[] | select(.name == "ci")] | first | .conclusion // ""' 2>/dev/null || true)"
  case "$state" in
    success) return 0 ;;
    "")
      echo "CI has reported nothing for $sha."
      echo "It may still be running -- check: gh run list --commit $sha"
      return 1
      ;;
    *)
      echo "CI reported '$state' for $sha; a release needs a green build."
      return 1
      ;;
  esac
}

if [ "${ORION_SKIP_CI_CHECK:-}" = "1" ]; then
  echo "    WARNING: ORION_SKIP_CI_CHECK=1 -- shipping without a CI verdict"
else
  gate "CI green for HEAD" ci_green_for_head
fi

echo "    all green"
