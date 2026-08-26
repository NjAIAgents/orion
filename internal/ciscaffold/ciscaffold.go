// Package ciscaffold gives a repository one standard way to run its tests.
//
// The contract is a single file: scripts/test.sh. CI runs it, a person runs
// it, and the agent is told to run it before finishing. That one indirection
// is the whole design -- when the three can diverge, they do, and the way it
// shows up is a pull request that is green in CI and broken on the machine
// that has to ship it.
//
// It also makes the CI verdict mean something. Without a workflow, GitHub
// reports no checks at all, and Orion has no honest choice but to treat an
// unverified branch as passing, because the alternative is every ticket
// waiting forever for a verdict nobody will ever produce. Scaffolding CI at
// adoption is what turns "no checks configured" from the normal case into a
// deliberate one.
//
// Nothing here overwrites. A repository that already has a test script or a
// workflow has one for reasons this package cannot see.
package ciscaffold

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Stack is the toolchain detected in a repository. Detection is by marker
// file rather than by asking, because adoption should not stop to
// interrogate someone about a repository that can describe itself.
type Stack string

const (
	StackGo      Stack = "go"
	StackPython  Stack = "python"
	StackNode    Stack = "node"
	StackUnknown Stack = "unknown"
)

// Result reports what was created, so the caller can say so rather than
// leaving files to be discovered later in a diff.
type Result struct {
	Stack         Stack
	ScriptPath    string
	ScriptCreated bool
	FlowPath      string
	FlowCreated   bool
	Notes         []string
}

// Detect identifies the toolchain from marker files.
//
// Go is checked first: a Go repository with a package.json for tooling is
// still a Go repository, and running npm test in it would prove nothing.
func Detect(dir string) Stack {
	exists := func(name string) bool {
		_, err := os.Stat(filepath.Join(dir, name))
		return err == nil
	}
	switch {
	case exists("go.mod"):
		return StackGo
	case exists("pyproject.toml"), exists("setup.py"), exists("requirements.txt"):
		return StackPython
	case exists("package.json"):
		return StackNode
	}
	return StackUnknown
}

// Ensure writes the test script and the workflow if they are missing.
func Ensure(dir string) (Result, error) {
	res := Result{Stack: Detect(dir)}

	script := filepath.Join(dir, "scripts", "test.sh")
	res.ScriptPath = "scripts/test.sh"
	switch _, err := os.Stat(script); {
	case err == nil:
		res.Notes = append(res.Notes, "scripts/test.sh already exists and was left alone")
	default:
		if err := os.MkdirAll(filepath.Dir(script), 0o755); err != nil {
			return res, err
		}
		if err := os.WriteFile(script, []byte(scriptFor(res.Stack)), 0o755); err != nil {
			return res, err
		}
		res.ScriptCreated = true
		if res.Stack == StackUnknown {
			res.Notes = append(res.Notes,
				"the toolchain could not be identified, so scripts/test.sh is a stub that FAILS until you fill it in -- "+
					"a script that exits 0 without running anything is worse than none, because CI would then be green by construction")
		}
	}

	flow := filepath.Join(dir, ".github", "workflows", "orion-ci.yml")
	res.FlowPath = ".github/workflows/orion-ci.yml"
	switch _, err := os.Stat(flow); {
	case err == nil:
		res.Notes = append(res.Notes, "the workflow already exists and was left alone")
	default:
		if existing, _ := filepath.Glob(filepath.Join(dir, ".github", "workflows", "*.y*ml")); len(existing) > 0 {
			// Another workflow is already running something. Adding a second
			// one that runs the same suite doubles the CI bill and makes the
			// checks list ambiguous about which verdict matters.
			res.Notes = append(res.Notes, fmt.Sprintf(
				"%d workflow(s) already exist, so none was added; point one of them at scripts/test.sh",
				len(existing)))
			res.FlowPath = ""
			return res, nil
		}
		if err := os.MkdirAll(filepath.Dir(flow), 0o755); err != nil {
			return res, err
		}
		if err := os.WriteFile(flow, []byte(workflowFor(res.Stack)), 0o644); err != nil {
			return res, err
		}
		res.FlowCreated = true
	}
	return res, nil
}

const header = `#!/usr/bin/env bash
# The single entry point for this repository's tests.
#
# CI runs this file, you run this file, and the agent is told to run this
# file before it finishes. Keeping them identical is the point: when the
# three can diverge they do, and it surfaces as a pull request that is green
# in CI and broken on the machine that has to ship it.
#
# Add to it rather than around it. A check that is not reachable from here
# does not gate anything.
set -euo pipefail
cd "$(dirname "$0")/.."
`

func scriptFor(s Stack) string {
	switch s {
	case StackGo:
		return header + `
echo "==> gofmt"
unformatted=$(gofmt -l . | grep -v '^vendor/' || true)
if [ -n "$unformatted" ]; then
  echo "these files are not gofmt'd:"; echo "$unformatted"; exit 1
fi

echo "==> vet"
go vet ./...

echo "==> test"
go test ./... -count=1

echo "==> race"
go test ./... -race -count=1
`
	case StackPython:
		return header + `
# Prefer the project's virtualenv when there is one, so a local run uses the
# same interpreter and pinned versions as CI rather than whatever python3
# happens to be first on PATH.
PY=python3
[ -x .venv/bin/python ] && PY=.venv/bin/python

echo "==> tests"
"$PY" -m pytest -q

# Lint and types are best-effort: a repository that has not adopted them
# should not fail its whole suite for their absence. Once installed, they
# gate like everything else.
if "$PY" -m ruff --version >/dev/null 2>&1; then
  echo "==> ruff"
  "$PY" -m ruff check .
fi

# DELIBERATELY OFF, because each of these fails on a codebase that has not
# already adopted it -- and a gate whose first act is to go red for reasons
# unrelated to the change under review teaches everyone that its complaints
# can be ignored. After that, a real failure looks exactly like the noise.
#
# Turn each on as its own commit, once the repository is clean for it:
#
#   "$PY" -m ruff format .          # then uncomment the check below
#   "$PY" -m ruff format --check .
#
#   "$PY" -m mypy .                 # fix what it finds, then uncomment
#   "$PY" -m mypy .
`
	case StackNode:
		return header + `
echo "==> install"
if [ -f package-lock.json ]; then npm ci; else npm install; fi

echo "==> test"
npm test

if npm run | grep -qE '^  lint'; then
  echo "==> lint"
  npm run lint
fi
`
	}
	return header + `
# Orion could not identify this repository's toolchain, so this script FAILS
# on purpose until you fill it in.
#
# A stub that exits 0 would be worse than no script at all: CI would report
# green having run nothing, and every pull request would carry a check that
# proves only that the check exists.
echo "scripts/test.sh has not been filled in yet." >&2
echo "Add this repository's test command above, then delete this block." >&2
exit 1
`
}

func workflowFor(s Stack) string {
	var setup string
	switch s {
	case StackGo:
		setup = `      - uses: actions/setup-go@v5
        with:
          go-version: stable
`
	case StackPython:
		setup = `      - uses: actions/setup-python@v5
        with:
          python-version: '3.x'
      - name: install
        run: |
          python -m pip install --upgrade pip
          if [ -f requirements.txt ]; then pip install -r requirements.txt; fi
          if [ -f pyproject.toml ]; then pip install -e ".[dev]" || pip install -e . || true; fi
`
	case StackNode:
		setup = `      - uses: actions/setup-node@v4
        with:
          node-version: lts/*
`
	}

	return `# Written by orion init. Runs the same script you run by hand.
#
# The job name matters: it is what Orion reads to decide whether a branch is
# safe to merge, and what appears in the pull request's checks list.
name: tests

on:
  pull_request:
  push:
    branches: [main, develop]

# One run per branch. A force-push mid-review otherwise leaves an older run
# still reporting, and Orion would act on a verdict for code that is gone.
concurrency:
  group: tests-${{ github.ref }}
  cancel-in-progress: true

jobs:
  test:
    name: test
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
` + setup + `      - name: run the suite
        run: ./scripts/test.sh
`
}

// Describe renders what Ensure did, for the adoption summary.
func Describe(r Result) []string {
	var out []string
	if r.ScriptCreated {
		out = append(out, "created "+r.ScriptPath+" ("+string(r.Stack)+")")
	}
	if r.FlowCreated {
		out = append(out, "created "+r.FlowPath)
	}
	out = append(out, r.Notes...)
	return out
}

// NeedsAttention reports whether the scaffold left something a person must
// finish before CI means anything.
func NeedsAttention(r Result) bool {
	return r.ScriptCreated && r.Stack == StackUnknown
}

func init() {
	// Guard against a typo in the templates silently producing a script that
	// cannot run. Cheap, and the failure it prevents is invisible until CI.
	for _, s := range []Stack{StackGo, StackPython, StackNode, StackUnknown} {
		if !strings.HasPrefix(scriptFor(s), "#!") {
			panic("ciscaffold: test script template lost its shebang")
		}
	}
}
