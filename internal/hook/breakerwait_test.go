package hook

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/orion-sdlc/orion/internal/config"
	"github.com/orion-sdlc/orion/internal/state"
)

// OR-207. Two tickets finished their implementation, had it green, and both
// ended orion-failed with every line uncommitted -- because the only way to
// wait for a nine-minute suite was to re-read its output file, and from the
// breaker's side that is the same action as looping.
//
// These tests pin both halves of the answer: waiting must be possible, and a
// genuine no-progress loop must still trip. Weakening the second to buy the
// first is the failure this fix must not become.

// post drives one PostToolUse call.
func post(store *state.Store, cfg config.Config, session string, in Input) Decision {
	in.HookEventName = "PostToolUse"
	in.SessionID = session
	return Breaker(in, cfg, store)
}

// TestWaitingOnABackgroundCommandIsNotALoop is OR-191's run exactly: the
// suite is launched in the background, its output file is still EMPTY, and
// the agent re-reads that same empty path while it waits. Unchanged content
// proves nothing here -- empty is what an unfinished run's log looks like.
func TestWaitingOnABackgroundCommandIsNotALoop(t *testing.T) {
	cfg := testCfg() // MaxRepeatIdentical = 3
	cfg.Limits.MaxToolCalls = 1000
	store := state.New(t.TempDir())

	post(store, cfg, "s", Input{
		ToolName:     "Bash",
		ToolInput:    json.RawMessage(`{"command":"./scripts/test.sh > /tmp/suite.log 2>&1 &"}`),
		ToolResponse: json.RawMessage(`{"stdout":"","is_error":false}`),
	})

	var d Decision
	for i := 0; i < 8; i++ {
		d = post(store, cfg, "s", Input{
			ToolName:     "Read",
			ToolInput:    json.RawMessage(`{"file_path":"/tmp/suite.log"}`),
			ToolResponse: json.RawMessage(`{"file":{"content":""}}`),
		})
	}
	if d.Blocked() {
		t.Fatalf("waiting for a background command must not trip the loop breaker: %s", d.Msg)
	}
}

// The harness, not the agent, names the output file when a call is
// backgrounded with run_in_background. That is the path OR-189 polled, so
// recognising it is what makes the exemption reach the real case.
func TestTheHarnessesOwnBackgroundOutputFileIsRecognised(t *testing.T) {
	cfg := testCfg()
	cfg.Limits.MaxToolCalls = 1000
	store := state.New(t.TempDir())

	post(store, cfg, "s", Input{
		ToolName:  "Bash",
		ToolInput: json.RawMessage(`{"command":"./scripts/test.sh","run_in_background":true}`),
		ToolResponse: json.RawMessage(
			`{"stdout":"Running in background. Output: /tmp/claude/tasks/ab12.output","is_error":false}`),
	})

	var d Decision
	for i := 0; i < 8; i++ {
		d = post(store, cfg, "s", Input{
			ToolName:     "Read",
			ToolInput:    json.RawMessage(`{"file_path":"/tmp/claude/tasks/ab12.output"}`),
			ToolResponse: json.RawMessage(`{"file":{"content":""}}`),
		})
	}
	if d.Blocked() {
		t.Fatalf("polling the harness's own background output file is a wait, not a loop: %s", d.Msg)
	}
}

// A read that returns something DIFFERENT observed progress, whatever its
// input was. This is OR-189's case: the log was filling up as it was read.
func TestAReadThatReturnsSomethingNewIsNotARepeat(t *testing.T) {
	cfg := testCfg()
	cfg.Limits.MaxToolCalls = 1000
	store := state.New(t.TempDir())

	var d Decision
	for i := 0; i < 8; i++ {
		d = post(store, cfg, "s", Input{
			ToolName:  "Read",
			ToolInput: json.RawMessage(`{"file_path":"/tmp/growing.log"}`),
			ToolResponse: json.RawMessage(
				fmt.Sprintf(`{"file":{"content":"ok %d"}}`, i)),
		})
	}
	if d.Blocked() {
		t.Fatalf("a file that changes between reads is not a no-progress loop: %s", d.Msg)
	}
}

// Asking a background task for its output has no other use than waiting.
func TestPollingABackgroundTaskIsNotALoop(t *testing.T) {
	cfg := testCfg()
	cfg.Limits.MaxToolCalls = 1000
	store := state.New(t.TempDir())

	var d Decision
	for i := 0; i < 8; i++ {
		d = post(store, cfg, "s", Input{
			ToolName:     "BashOutput",
			ToolInput:    json.RawMessage(`{"bash_id":"bash_1"}`),
			ToolResponse: json.RawMessage(`{"stdout":"","is_error":false}`),
		})
	}
	if d.Blocked() {
		t.Fatalf("asking a background task whether it has finished is the wait: %s", d.Msg)
	}
}

// TaskOutput is the other tool that only ever means "has this finished yet".
func TestPollingATaskOutputIsNotALoop(t *testing.T) {
	cfg := testCfg()
	cfg.Limits.MaxToolCalls = 1000
	store := state.New(t.TempDir())

	var d Decision
	for i := 0; i < 8; i++ {
		d = post(store, cfg, "s", Input{
			ToolName:     "TaskOutput",
			ToolInput:    json.RawMessage(`{"task_id":"bg1"}`),
			ToolResponse: json.RawMessage(`{"status":"running"}`),
		})
	}
	if d.Blocked() {
		t.Fatalf("asking TaskOutput whether a background task has finished is the wait: %s", d.Msg)
	}
}

// The half that must NOT move. Re-reading a file nothing is writing is the
// loop this breaker was built for, and OR-189's own note concedes the point.
func TestRereadingAFileNothingIsWritingStillTrips(t *testing.T) {
	cfg := testCfg() // MaxRepeatIdentical = 3
	cfg.Limits.MaxToolCalls = 1000
	store := state.New(t.TempDir())

	// A background command ran, but it writes somewhere else entirely.
	post(store, cfg, "s", Input{
		ToolName:     "Bash",
		ToolInput:    json.RawMessage(`{"command":"./scripts/test.sh > /tmp/suite.log 2>&1 &"}`),
		ToolResponse: json.RawMessage(`{"stdout":"","is_error":false}`),
	})

	var d Decision
	for i := 0; i < cfg.Limits.MaxRepeatIdentical+1; i++ {
		d = post(store, cfg, "s", Input{
			ToolName:     "Read",
			ToolInput:    json.RawMessage(`{"file_path":"internal/work/work.go"}`),
			ToolResponse: json.RawMessage(`{"file":{"content":"package work"}}`),
		})
	}
	if !d.Blocked() {
		t.Fatal("re-reading an unchanged file nothing is writing is still a loop and must trip")
	}
	if got := store.Read("s").Tripped; got != "breaker/loop" {
		t.Errorf("tripped = %q, want breaker/loop", got)
	}
}

// A background command that redirects to a RELATIVE path and a later read
// that resolves the same file by its ABSOLUTE path must still be recognised
// as the same awaited file -- the agent names it one way when it redirects
// and the tool that reads it back may report the other.
func TestARelativeAndAbsolutePathToTheSameFileAreRecognisedAsTheSameAwaitedFile(t *testing.T) {
	cfg := testCfg()
	cfg.Limits.MaxToolCalls = 1000
	store := state.New(t.TempDir())

	post(store, cfg, "s", Input{
		ToolName:     "Bash",
		ToolInput:    json.RawMessage(`{"command":"./scripts/test.sh > suite.log 2>&1 &"}`),
		ToolResponse: json.RawMessage(`{"stdout":"","is_error":false}`),
	})

	var d Decision
	for i := 0; i < 8; i++ {
		d = post(store, cfg, "s", Input{
			ToolName:     "Read",
			ToolInput:    json.RawMessage(`{"file_path":"/repo/worktree/suite.log"}`),
			ToolResponse: json.RawMessage(`{"file":{"content":""}}`),
		})
	}
	if d.Blocked() {
		t.Fatalf("an absolute read of a relatively-redirected file must still be recognised as a wait: %s", d.Msg)
	}
}

// A backgrounded command still counts for everything else: the exemption is
// scoped to the identical-repeat counter for the file being waited on, not a
// general amnesty for the session that launched it.
func TestBackgroundingDoesNotExemptOtherRepeatedCalls(t *testing.T) {
	cfg := testCfg()
	cfg.Limits.MaxToolCalls = 1000
	store := state.New(t.TempDir())

	post(store, cfg, "s", Input{
		ToolName:     "Bash",
		ToolInput:    json.RawMessage(`{"command":"./scripts/test.sh > /tmp/suite.log 2>&1 &"}`),
		ToolResponse: json.RawMessage(`{"stdout":"","is_error":false}`),
	})
	var d Decision
	for i := 0; i < cfg.Limits.MaxRepeatIdentical+1; i++ {
		d = post(store, cfg, "s", Input{
			ToolName:     "Bash",
			ToolInput:    json.RawMessage(`{"command":"kill -USR1 4242"}`),
			ToolResponse: json.RawMessage(`{"stdout":"","is_error":false}`),
		})
	}
	if !d.Blocked() {
		t.Fatal("an identical Bash call is a loop whether or not something runs in the background")
	}
}

// `cmd && other` is a conjunction the agent waits for, not a backgrounding.
func TestATrailingConjunctionIsNotBackgrounding(t *testing.T) {
	in := Input{ToolName: "Bash", ToolInput: json.RawMessage(`{"command":"go build ./... &&"}`)}
	if in.Background() {
		t.Error("a trailing && is a conjunction, not a background launch")
	}
	bg := Input{ToolName: "Bash", ToolInput: json.RawMessage(`{"command":"./scripts/test.sh &"}`)}
	if !bg.Background() {
		t.Error("a trailing & is a background launch")
	}
}

// OR-207: a tripped run must not still be holding the work it finished.
//
// Committing is the FIRST thing the allowance does, not something the agent
// has to think to spend its remaining budget on -- both lost runs knew their
// work was complete and uncommitted, said so in BLOCKED.md, and nothing acted
// on it.
func TestATerminalTripCommitsTheWorkItWasHolding(t *testing.T) {
	root := gitRepo(t)
	cfg := testCfg()
	cfg.Root = root
	store := state.New(t.TempDir())

	writeFile(t, filepath.Join(root, "impl.go"), "package x\n")
	writeFile(t, filepath.Join(root, "impl_test.go"), "package x\n") // a NEW file, never added

	var d Decision
	for i := 0; i < cfg.Limits.MaxRepeatIdentical; i++ {
		d = post(store, cfg, "s", Input{
			ToolName:  "Bash",
			ToolInput: json.RawMessage(`{"command":"kill -USR1 4242"}`),
		})
	}
	if !d.Blocked() {
		t.Fatalf("the repeated call should have tripped: %s", d.Msg)
	}

	committed := gitOut(t, root, "show", "--name-only", "--format=%s", "HEAD")
	for _, want := range []string{"impl.go", "impl_test.go", "wip: snapshot"} {
		if !strings.Contains(committed, want) {
			t.Errorf("the trip snapshot is missing %q:\n%s", want, committed)
		}
	}
	// The breaker's own account of the trip is for whoever opens the
	// worktree; it is not part of the change and must not ride the branch.
	if strings.Contains(committed, "BLOCKED.md") {
		t.Errorf("plans/BLOCKED.md was committed to the branch:\n%s", committed)
	}
	if dirty := gitOut(t, root, "status", "--porcelain", "--untracked-files=no"); strings.TrimSpace(dirty) != "" {
		t.Errorf("the worktree still holds uncommitted tracked changes:\n%s", dirty)
	}

	// And the agent is TOLD, in the block message, what became of its work.
	snap := store.Read("s").TripSnapshot
	if !strings.Contains(snap, "committed 2 uncommitted file(s)") {
		t.Errorf("trip snapshot = %q", snap)
	}
	pre := Breaker(Input{HookEventName: "PreToolUse", SessionID: "s", ToolName: "Bash",
		ToolInput: json.RawMessage(`{"command":"go test ./..."}`)}, cfg, store)
	if !strings.Contains(pre.Msg, snap) {
		t.Errorf("the block message does not say what happened to the work:\n%s", pre.Msg)
	}
}

// The two things a snapshot commit must pick up are DIFFERENT git states: a
// MODIFICATION to a file already on the branch, distinct from a brand new
// file nothing has ever tracked. This pins the modified-tracked-file half
// on its own, so a change that only handled new files (the case
// TestATerminalTripCommitsTheWorkItWasHolding already exercises) would fail
// it.
func TestATerminalTripCommitsModifiedTrackedFiles(t *testing.T) {
	root := gitRepo(t)
	writeFile(t, filepath.Join(root, "impl.go"), "package x\n")
	gitOut(t, root, "add", "impl.go")
	gitOut(t, root, "commit", "-q", "-m", "feat: initial")

	cfg := testCfg()
	cfg.Root = root
	store := state.New(t.TempDir())

	// A modification to the file already on the branch -- not a new file.
	writeFile(t, filepath.Join(root, "impl.go"), "package x\n\nfunc F() {}\n")

	var d Decision
	for i := 0; i < cfg.Limits.MaxRepeatIdentical; i++ {
		d = post(store, cfg, "s", Input{
			ToolName:  "Bash",
			ToolInput: json.RawMessage(`{"command":"kill -USR1 4242"}`),
		})
	}
	if !d.Blocked() {
		t.Fatalf("the repeated call should have tripped: %s", d.Msg)
	}

	committed := gitOut(t, root, "show", "--name-only", "--format=%s", "HEAD")
	if !strings.Contains(committed, "impl.go") || !strings.Contains(committed, "wip: snapshot") {
		t.Errorf("the modified tracked file was not part of the snapshot commit:\n%s", committed)
	}
	diff := gitOut(t, root, "show", "HEAD", "--", "impl.go")
	if !strings.Contains(diff, "func F()") {
		t.Errorf("the actual modification did not reach the snapshot commit:\n%s", diff)
	}
	if dirty := gitOut(t, root, "status", "--porcelain", "--untracked-files=no"); strings.TrimSpace(dirty) != "" {
		t.Errorf("the worktree still holds uncommitted tracked changes:\n%s", dirty)
	}
}

// The commit message a trip snapshot leaves on the branch must say, in
// plain words, that the work is unverified -- a reader of `git log` who has
// not seen this ticket must not mistake it for reviewed work.
func TestTheTripSnapshotCommitMessageStatesTheWorkIsUnverified(t *testing.T) {
	root := gitRepo(t)
	cfg := testCfg()
	cfg.Root = root
	store := state.New(t.TempDir())

	writeFile(t, filepath.Join(root, "impl.go"), "package x\n")

	for i := 0; i < cfg.Limits.MaxRepeatIdentical; i++ {
		post(store, cfg, "s", Input{
			ToolName:  "Bash",
			ToolInput: json.RawMessage(`{"command":"kill -USR1 4242"}`),
		})
	}

	msg := strings.ToLower(gitOut(t, root, "log", "-1", "--format=%B"))
	if !strings.Contains(msg, "unverified") &&
		!strings.Contains(msg, "nothing here has been verified") {
		t.Errorf("the snapshot commit message does not say the work is unverified:\n%s", msg)
	}
}

// An unverified-edits trip is the one with a designed way out: a passing
// verify clears it and the run continues. Snapshotting there would put a
// "wip:" commit in the middle of a pull request that goes on to succeed.
func TestAnUnverifiedEditsTripDoesNotSnapshot(t *testing.T) {
	root := gitRepo(t)
	cfg := testCfg() // MaxEditsWithoutVerify = 4
	cfg.Root = root
	store := state.New(t.TempDir())

	writeFile(t, filepath.Join(root, "impl.go"), "package x\n")
	for i := 0; i < cfg.Limits.MaxEditsWithoutVerify; i++ {
		post(store, cfg, "s", Input{
			ToolName:  "Edit",
			ToolInput: json.RawMessage(fmt.Sprintf(`{"file_path":"f%d.go"}`, i)),
		})
	}
	if store.Read("s").Tripped != "breaker/unverified-edits" {
		t.Fatalf("expected an unverified-edits trip, got %q", store.Read("s").Tripped)
	}
	if log := gitOut(t, root, "log", "--oneline"); strings.Contains(log, "wip: snapshot") {
		t.Errorf("a clearable trip must not commit anything:\n%s", log)
	}
}

// A trip in a directory that is not a git worktree must report that, not
// commit into whatever repository happens to be above it.
func TestATripOutsideAWorktreeCommitsNothing(t *testing.T) {
	cfg := testCfg()
	cfg.Root = t.TempDir() // a plain directory
	store := state.New(t.TempDir())

	for i := 0; i < cfg.Limits.MaxRepeatIdentical; i++ {
		post(store, cfg, "s", Input{
			ToolName:  "Bash",
			ToolInput: json.RawMessage(`{"command":"kill -USR1 4242"}`),
		})
	}
	if snap := store.Read("s").TripSnapshot; snap != "" && !strings.Contains(snap, "could NOT commit") {
		t.Errorf("trip snapshot = %q, want silence or an honest failure", snap)
	}
}

func gitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"},
		{"config", "user.email", "orion@example.com"},
		{"config", "user.name", "Orion"},
		{"commit", "-q", "--allow-empty", "-m", "root"},
	} {
		gitOut(t, dir, args...)
	}
	return dir
}

func gitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}
