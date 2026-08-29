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

// The scanner the scan workflow installs, pinned.
//
// Installed with `go install`, which resolves through the module proxy and
// checks the module against the public checksum database before building it.
// That is the verification: a step that curls a release tarball and runs it
// has proved nothing about what it just executed, and a security control that
// runs an unverified binary is theatre.
const (
	gitleaksModule  = "github.com/zricethezav/gitleaks/v8"
	gitleaksVersion = "v8.30.1"

	// The scan itself, shared with the test that plants a secret against it,
	// so the test cannot pass against a command CI does not run.
	//
	// Full history rather than the diff, because a diff-only scan misses
	// everything committed before the scanner existed -- which is most of
	// what is actually leaked.
	//
	// --verbose then --redact, and the pair is the point. Without --verbose,
	// gitleaks prints "leaks found: 1" and nothing else, which fails the
	// build without telling anyone where to look. With it, every finding is
	// printed -- file, line, commit -- and --redact replaces the secret
	// itself with REDACTED, because CI logs are as readable as the
	// repository and a scanner that prints its find has published the thing
	// it was hired to catch.
	//
	// --exit-code 1 because CI has no implementer to negotiate with.
	scanCommand = "gitleaks git . --verbose --redact=100 --no-banner --exit-code 1"
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
	ScanPath      string
	ScanCreated   bool
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

// Ensure writes the test script, the workflow and the secret scan if they are
// missing.
func Ensure(dir string) (Result, error) {
	res := Result{Stack: Detect(dir)}
	if err := ensureScript(dir, &res); err != nil {
		return res, err
	}
	if err := ensureFlow(dir, &res); err != nil {
		return res, err
	}
	return res, ensureScan(dir, &res)
}

func ensureScript(dir string, res *Result) error {
	script := filepath.Join(dir, "scripts", "test.sh")
	res.ScriptPath = "scripts/test.sh"
	switch _, err := os.Stat(script); {
	case err == nil:
		res.Notes = append(res.Notes, "scripts/test.sh already exists and was left alone")
	default:
		if err := os.MkdirAll(filepath.Dir(script), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(script, []byte(scriptFor(res.Stack)), 0o755); err != nil {
			return err
		}
		res.ScriptCreated = true
		if res.Stack == StackUnknown {
			res.Notes = append(res.Notes,
				"the toolchain could not be identified, so scripts/test.sh is a stub that FAILS until you fill it in -- "+
					"a script that exits 0 without running anything is worse than none, because CI would then be green by construction")
		}
	}
	return nil
}

func ensureFlow(dir string, res *Result) error {
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
			return nil
		}
		if err := os.MkdirAll(filepath.Dir(flow), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(flow, []byte(workflowFor(res.Stack)), 0o644); err != nil {
			return err
		}
		res.FlowCreated = true
	}
	return nil
}

// ensureScan writes the secret-scanning workflow.
//
// Deliberately NOT a job inside orion-ci.yml, and deliberately not subject to
// the "another workflow already exists, so add none" rule above. That rule
// exists to avoid running the same test suite twice; an existing test
// workflow does not scan for secrets, so skipping the scan because one is
// present would leave every already-configured repository -- Orion's own
// included -- unscanned. What DOES skip it is the project already having a
// scanner, which is the thing not to duplicate (A5).
func ensureScan(dir string, res *Result) error {
	scan := filepath.Join(dir, ".github", "workflows", "orion-secret-scan.yml")
	res.ScanPath = ".github/workflows/orion-secret-scan.yml"

	if _, err := os.Stat(scan); err == nil {
		res.Notes = append(res.Notes, "the secret-scan workflow already exists and was left alone")
		return nil
	}
	if found := existingScanner(dir); found != "" {
		res.Notes = append(res.Notes, fmt.Sprintf(
			"%s is already wired up here, so no second scanner was added", found))
		res.ScanPath = ""
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(scan), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(scan, []byte(scanWorkflow()), 0o644); err != nil {
		return err
	}
	res.ScanCreated = true
	return nil
}

// existingScanner reports a secret scanner the project already runs, by name,
// or "" when it runs none.
//
// Looks at what the repository says about itself rather than asking: a
// workflow that mentions a scanner runs one, and a scanner's config file is
// only there because someone put it there. Detection is deliberately
// generous -- a false positive costs a repository the scan Orion would have
// added, which is a note the adoption summary prints, while a false negative
// costs it a second scanner reporting the same findings under a different
// check name.
func existingScanner(dir string) string {
	scanners := []string{"gitleaks", "trufflehog", "detect-secrets"}

	// Config and baseline files, which name their scanner by existing.
	for name, scanner := range map[string]string{
		".gitleaks.toml":    "gitleaks",
		"gitleaks.toml":     "gitleaks",
		".secrets.baseline": "detect-secrets",
		".trufflehog.yaml":  "trufflehog",
		".trufflehog.yml":   "trufflehog",
	} {
		if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
			return scanner
		}
	}

	// Anything that runs one: a workflow, or a pre-commit hook config.
	files, _ := filepath.Glob(filepath.Join(dir, ".github", "workflows", "*.y*ml"))
	for _, name := range []string{".pre-commit-config.yaml", ".pre-commit-config.yml"} {
		files = append(files, filepath.Join(dir, name))
	}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		body := strings.ToLower(string(b))
		for _, s := range scanners {
			if strings.Contains(body, s) {
				return s
			}
		}
	}
	return ""
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
#
# Also look in the MAIN worktree. Orion runs agents in a git worktree, which
# is a separate directory that shares history but not ignored files -- so
# .venv is simply absent there, and the agent asked to "run the suite before
# finishing" hits a bare "No module named pytest" with nothing telling it
# that the environment, not the code, is what is missing.
PY=python3
if [ -x .venv/bin/python ]; then
  PY=.venv/bin/python
elif command -v git >/dev/null 2>&1; then
  main_root=$(dirname "$(git rev-parse --git-common-dir 2>/dev/null || echo .)")
  [ -x "$main_root/.venv/bin/python" ] && PY="$main_root/.venv/bin/python"
fi

# Bootstrap when there is nothing to run with.
#
# A fresh clone -- and every Orion sandbox is one -- has no virtualenv,
# because it is not in version control. Refusing at that point would mean the
# single entry point only works on a machine somebody had already prepared by
# hand, which is most of the value gone: CI installs its own dependencies, so
# a script that cannot do the same is not running what CI runs.
#
# Only when this project actually declares dependencies. Building an empty
# venv for a repository that never asked for one is a surprise, not a help.
if ! "$PY" -c "import pytest" >/dev/null 2>&1; then
  if [ -f pyproject.toml ] || [ -f requirements.txt ]; then
    echo "==> no environment found; creating .venv"
    python3 -m venv .venv
    PY=.venv/bin/python
    "$PY" -m pip install --quiet --upgrade pip
    [ -f requirements.txt ] && "$PY" -m pip install --quiet -r requirements.txt
    if [ -f pyproject.toml ]; then
      "$PY" -m pip install --quiet -e ".[dev]" || "$PY" -m pip install --quiet -e . || true
    fi
  fi
fi

if ! "$PY" -c "import pytest" >/dev/null 2>&1; then
  echo "pytest is still not available to $PY after bootstrapping." >&2
  echo "Install this project's dependencies by hand and try again:" >&2
  echo "  python3 -m venv .venv && .venv/bin/pip install -e '.[dev]'" >&2
  exit 1
fi

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

# One run per branch or PR. A force-push mid-review otherwise leaves an older
# run still reporting, and Orion would act on a verdict for code that is gone.
#
# github.ref alone is not enough: push uses refs/heads/<branch>, pull_request
# uses refs/pull/<n>/merge, so the two never land in the same group and a
# push whose branch also has an open PR runs the whole suite twice on the
# same SHA. Keying on the PR number when one exists, falling back to the ref
# otherwise, collapses the pair (OR-172).
concurrency:
  group: tests-${{ github.workflow }}-${{ github.event.pull_request.number || github.ref }}
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

// scanWorkflow is the secret scan, as its own workflow and its own check.
//
// A separate check context on purpose: it composes with `orion protect`,
// which discovers contexts from real check runs, so once this has reported
// once the scan can be made required without either feature knowing about
// the other.
func scanWorkflow() string {
	return `# Written by orion init.
#
# A hard gate, unlike the agent loop: CI has no implementer to talk to, and a
# secret already pushed to a public repository is not a finding to negotiate.
# A hit fails the build.
name: secret scan

on:
  pull_request:
  push:
    branches: [main, develop]

# See the "tests" workflow's concurrency comment: github.ref alone cannot
# collapse a push and its pull_request into one group, so this scan would
# otherwise run twice on the same SHA whenever the branch has an open PR
# (OR-172).
concurrency:
  group: secret-scan-${{ github.workflow }}-${{ github.event.pull_request.number || github.ref }}
  cancel-in-progress: true

jobs:
  scan:
    name: secret scan
    runs-on: ubuntu-latest
    steps:
      # fetch-depth: 0 -- the whole history, not just the diff. A diff-only
      # scan misses everything committed before the scanner existed, which is
      # most of what is actually leaked. It is cheap: 89 commits in 373ms.
      - uses: actions/checkout@v4
        with:
          fetch-depth: 0

      - uses: actions/setup-go@v5
        with:
          go-version: stable

      # Pinned, and verified: go install resolves through the module proxy
      # and checks the module against the public checksum database before
      # building it. A step that pipes an unverified binary off the internet
      # and runs it is theatre.
      - name: install gitleaks
        run: go install ` + gitleaksModule + `@` + gitleaksVersion + `

      # --verbose prints each finding -- file, line, commit -- because a
      # build that fails saying only "leaks found: 1" tells nobody where to
      # look. --redact replaces the secret itself with REDACTED: these logs
      # are readable by whoever can read the repository, and a scanner that
      # prints its find has published the thing it was hired to catch.
      - name: scan
        run: ` + scanCommand + `
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
	if r.ScanCreated {
		out = append(out, "created "+r.ScanPath+" (gitleaks "+gitleaksVersion+", full history, redacted)")
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
