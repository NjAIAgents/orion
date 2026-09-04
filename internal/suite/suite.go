// Package suite runs a repository's own test suite as a process Orion owns,
// rather than as an instruction in an agent's prompt (OR-306).
//
// WHY THIS EXISTS. Until now Orion never invoked a test runner. It described
// one in prose -- supervisor.prompts.go's testEnv() tells the agent which
// command to use -- and left the agent to run it. That gives the agent three
// decisions it should not have: WHAT to run, WHETHER to run it, and HOW to
// report the result. A stage could go green because an agent ran a narrower
// subset than it claimed, or narrated a pass it never saw. A process can do
// none of those things: it exits 0 or it does not.
//
// It is also the cheaper arrangement. A test run carries one bit of
// interesting information plus text, so spending a model call on it buys
// nothing, and the runner parallelises better than agents would -- `go test`
// already runs packages concurrently, which is a thing no fan of subagents
// can improve on.
//
// WHAT THIS DELIBERATELY DOES NOT DO. It does not detect every ecosystem.
// nj-agents' /review-tests-build already does that across Node, Python, Go,
// Rust, JVM, Make and just, and reimplementing it here would trade that reach
// for ownership -- the same bad trade this project declined for /pm-plan. So
// detection here covers the shapes Orion can be certain about, and anything
// else returns NotFound so the caller keeps the delegated path. Degrading to
// the old behaviour is always allowed; degrading SILENTLY is not.
package suite

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"time"
)

// Result is what one suite run produced.
type Result struct {
	// Cmd is what was actually executed, for the report. An operator reading
	// "tests failed" needs to know which command failed.
	Cmd string
	// Passed is the exit-code verdict, and only that. A runner that exits 0
	// passed; anything else did not.
	Passed bool
	// Output is combined stdout and stderr, capped. The tail is what matters
	// -- Go prints its FAIL lines at the end -- so the cap keeps the tail.
	Output string
	// TimedOut distinguishes a hung suite from a failing one. They call for
	// different responses and reporting them the same way is how a flake gets
	// filed as a defect.
	TimedOut bool
	// Err is set when the runner could not be executed at all, which is
	// neither a pass nor a fail: it means the verdict is unknown, and an
	// unknown verdict must never read as a pass.
	Err error
}

// ErrNotFound is returned by Detect when this package cannot be certain what
// this repository's suite is. The caller falls back to the delegated path.
var ErrNotFound = errNotFound{}

type errNotFound struct{}

func (errNotFound) Error() string { return "no test command this package is certain of" }

// maxOutput caps what a failing run hands back.
//
// The TAIL is kept, not the head: `go test` prints its FAIL lines last, so a
// head-capped log is the part nobody needs. 64KB because this text reaches a
// fix-loop prompt, and a megabyte of passing output would crowd out the
// failure it was collected for.
const maxOutput = 64 << 10

func statOK(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// shellScript is how a .sh is invoked (OR-334).
//
// A shell script is not an executable on Windows: exec'ing one directly
// fails with `fork/exec ...`, which is what every suite test hit there --
// invisibly, because the Windows CI leg reported success over its own
// failures. POSIX can exec it directly (the shebang does the work), and
// Windows runners carry Git Bash, so naming bash explicitly is the one form
// that works on both.
func shellScript(path string) []string { return ScriptCommand(path) }

// ScriptCommand is the argv that runs a shell script on this platform.
//
// Exported because internal/work's red-before-green check runs the same
// scripts/test.sh and had its own direct exec of it, which meant OR-334's
// Windows fix reached one caller and not the other: the second one failed
// with "%1 is not a valid Win32 application" for as long as the Windows leg
// went unread (OR-341). One spelling, one place.
//
// Windows will not exec a file because it starts with `#!` -- it dispatches
// on the extension -- so the interpreter has to be named. Git Bash is what
// the CI job's own steps use.
func ScriptCommand(path string) []string {
	if runtime.GOOS == "windows" {
		return []string{"bash", path}
	}
	return []string{path}
}

// Detect returns the command that runs this repository's suite, or
// ErrNotFound.
//
// Deliberately narrow. It recognises the two shapes Orion can be sure of: a
// scripts/test.sh, which is this project's own contract and the one redgreen
// already relies on, and a go.mod with no such script. Everything else is
// somebody else's job -- see the package comment.
//
// procs is the concurrency the run may use, and it is passed to the RUNNER's
// own flag rather than used to spawn processes here. `go test -p N` bounds
// how many packages compile and run at once, which is the toolchain doing
// natively what Orion would otherwise reimplement worse.
func Detect(dir string, procs int) ([]string, error) {
	if script := filepath.Join(dir, "scripts", "test.sh"); statOK(script) {
		// No concurrency flag: the script owns its own invocation, and
		// second-guessing it here would fight the repository's own choice.
		return shellScript(script), nil
	}
	if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
		args := []string{"go", "test", "./...", "-count=1"}
		if procs > 0 {
			args = append(args, "-p", strconv.Itoa(procs))
		}
		return args, nil
	}
	return nil, ErrNotFound
}

// Run executes argv in dir under a wall clock, and returns what happened.
//
// The process gets its own group so a timeout kills the whole tree. A test
// runner spawns compilers, helper binaries and sometimes servers; killing
// only the direct child leaves those running, which is the orphan problem
// OR-141 already fixed for agent runs and which applies identically here.
func Run(dir string, argv []string, timeout time.Duration) Result {
	if len(argv) == 0 {
		return Result{Err: ErrNotFound}
	}
	res := Result{Cmd: shellish(argv)}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = dir
	var buf bytes.Buffer
	cmd.Stdout, cmd.Stderr = &buf, &buf
	setProcessGroup(cmd)

	if startErr := cmd.Start(); startErr != nil {
		res.Err = startErr
		return res
	}

	// THE GROUP IS KILLED WHEN THE DEADLINE FIRES, NOT AFTER Wait RETURNS.
	//
	// Getting this wrong is subtle and was caught by a test rather than by
	// reading: cmd.Wait() does not return when the direct child exits, it
	// returns when the output pipes close, and a grandchild inherits those
	// pipes. So a script whose child is killed while its own `sleep 60`
	// keeps running holds Wait open for the full sixty seconds -- the
	// deadline elapses and Run keeps waiting anyway.
	//
	// CommandContext's own kill has the same shape of problem: it signals
	// only the process it started. Watching the deadline here and killing
	// the whole GROUP is what makes the timeout real.
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	var runErr error
	select {
	case runErr = <-done:
		// Finished on its own. Still sweep the group: a suite that leaves a
		// server running behind it is a suite that leaks one per run.
		killGroup(cmd)
	case <-ctx.Done():
		killGroup(cmd)
		// Collect the exit rather than abandoning the goroutine, so the
		// output written before the kill is in the buffer when it is read.
		<-done
	}

	res.Output = tail(buf.String(), maxOutput)
	switch {
	case ctx.Err() == context.DeadlineExceeded:
		res.TimedOut = true
	case runErr == nil:
		res.Passed = true
	default:
		// An ExitError is a verdict: the suite ran and something failed.
		// Anything else means the command could not be run, which is not a
		// verdict at all and must not be reported as one.
		if _, isExit := runErr.(*exec.ExitError); !isExit {
			res.Err = runErr
		}
	}
	return res
}

// tail keeps the last n bytes, cut at a line boundary so the output does not
// begin mid-word.
func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	s = s[len(s)-n:]
	if i := indexByte(s, '\n'); i >= 0 && i+1 < len(s) {
		s = s[i+1:]
	}
	return "[earlier output trimmed]\n" + s
}

func indexByte(s string, b byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == b {
			return i
		}
	}
	return -1
}

// shellish renders argv for a human to read in a report. It is NOT a shell
// escape and nothing here is ever handed to a shell: Run execs argv directly,
// which is what keeps a repository path with a space in it from becoming a
// command injection.
func shellish(argv []string) string {
	out := ""
	for i, a := range argv {
		if i > 0 {
			out += " "
		}
		out += a
	}
	return out
}
