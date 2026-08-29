package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/orion-sdlc/orion/internal/actors"
	"github.com/orion-sdlc/orion/internal/collect"
	"github.com/orion-sdlc/orion/internal/config"
	"github.com/orion-sdlc/orion/internal/events"
	"github.com/orion-sdlc/orion/internal/match"
	"github.com/orion-sdlc/orion/internal/supervisor"
	"github.com/orion-sdlc/orion/internal/work"
	"github.com/orion-sdlc/orion/internal/workspace"
)

// fixOptions is everything the ci-fix run is configured with except the
// activity callback, which is a closure over the console writer.
//
// Separated from fixRun so what the run is actually configured with can be
// asserted without standing up a workspace and a worktree.
func fixOptions(key, branch, detail string, cfg config.Config) supervisor.Options {
	return supervisor.Options{
		Stage:      "ci-fix",
		Prompt:     supervisor.FixPrompt(key, branch, detail),
		MaxMinutes: cfg.Limits.MaxSessionMinutes,
		MaxTurns:   cfg.Limits.MaxToolCalls,
		// Attributed to the devops engineer and to the ticket, so the fix
		// loop's spend lands in the ticket's cost report rather than
		// disappearing: a re-entry is another run for the same ticket, and
		// three of them is where a cheap ticket becomes an expensive one.
		//
		// Attributed AND run as the devops engineer: the roster's model and
		// effort go to the CLI too, or the cost report names an actor that
		// never ran (OR-133). Empty means the CLI's own setting, so an
		// operator who configures nothing gets what they configured.
		Actor: events.ActorDevOps, Key: key,
		Model:  actors.Model(events.ActorDevOps),
		Effort: actors.Effort(events.ActorDevOps),
	}
}

// fixActivity is the ci-fix run's activity callback, separated out so a test
// can drive it directly without standing up a workspace, a worktree or the
// supervisor.
//
// Delegates to work.ActivityLogger -- the SAME logger every other supervised
// run uses -- rather than a second implementation. A hand-rolled OnActivity
// here once printed unattributed console lines and emitted nothing to the
// event log at all (OR-176): the roster knew who was running, but nothing
// downstream of the callback did.
func fixActivity(log *events.Log, w io.Writer, key string) func(supervisor.Activity) {
	return work.ActivityLogger(log, w, key, events.ActorDevOps)
}

// fixRun sends a CI failure back to an agent on the branch that caused it.
//
// The worktree is reused rather than recreated. The branch already has the
// agent's commits and its decision records; a fresh checkout would discard
// the context that explains why the code is shaped the way it is, and the
// fix would be attempted by something that had never seen the reasoning.
//
// Returns whether anything was actually pushed. Exit 0 with no new commit
// means the agent either could not see what to change, or saw it and was
// refused by the sandbox -- denied distinguishes the two (OR-174), since
// they call for different remedies: a person thinking about the first, a
// person applying a diff for the second, and no further attempt fixes
// either from inside the sandbox.
//
// Takes the event log so this run's activity is attributed and recorded the
// same way every other supervised run's is (OR-176).
func fixRun(ws *workspace.Workspace, key, branch, failure string, log *events.Log) (bool, string, *collect.PolicyDenial, error) {
	w := os.Stdout

	jobs, err := workspace.ListWorktrees(ws)
	if err != nil {
		return false, "", nil, err
	}
	var dir string
	for _, j := range jobs {
		if j.Branch == branch {
			dir = j.Path
			break
		}
	}
	if dir == "" {
		return false, "", nil, fmt.Errorf("no worktree for %s.\n"+
			"  It was pruned, so there is nowhere to apply a fix. "+
			"Re-queue the ticket to start again", branch)
	}

	// The full log, not the summary. "test (failure)" names which check went
	// red and nothing about why; an agent handed only that has to re-run the
	// suite locally to discover what a log already says.
	detail := failure
	if full := failingLog(dir, branch); strings.TrimSpace(full) != "" {
		detail = full
	}

	before, err := headOf(dir)
	if err != nil {
		return false, "", nil, err
	}

	cfg := config.Load(ws.RepoDir())
	jobWS := *ws
	jobWS.RepoPath = dir

	// Caught live, off the same activity stream the console line below is
	// built from -- not guessed at afterwards from the agent's prose. Shield
	// (internal/hook.Shield) blocks every Edit/Write/MultiEdit/NotebookEdit
	// that targets cfg.Paths.Protected unconditionally, so a tool call seen
	// here against that list is proof the sandbox refused it, regardless of
	// whether the agent said so in its closing message (OR-174).
	var deniedEdit *collect.PolicyDenial
	o := fixOptions(key, branch, detail, cfg)
	// The shared logger draws the console line and writes the event (OR-176);
	// the denial watch rides the same stream rather than a second one, so the
	// two cannot observe different activity (OR-174).
	say := fixActivity(log, w, key)
	o.OnActivity = func(a supervisor.Activity) {
		say(a)
		if deniedEdit == nil && isEditTool(a.Tool) {
			if rule := matchedRule(cfg.Paths.Protected, a.Detail); rule != "" {
				deniedEdit = &collect.PolicyDenial{Tool: a.Tool, Path: a.Detail, Rule: rule}
			}
		}
	}
	res, err := supervisor.Run(&jobWS, o)
	if err != nil {
		return false, "", nil, err
	}
	if res.ExitCode != 0 {
		return false, "", nil, fmt.Errorf("the fix run exited %d: %s", res.ExitCode, res.Reason)
	}

	// The agent's own closing message, not re-derived: it already said what
	// it changed and why, in its last turn. Reduced to one line so the
	// watch/work console gains a summary instead of a wall of prose --
	// oneLine keeps whatever the agent front-loaded, which is normally the
	// answer, not the reasoning that led to it (OR-129).
	summary := oneLine(res.Final)

	after, err := headOf(dir)
	if err != nil {
		return false, "", nil, err
	}
	if after == before {
		if deniedEdit != nil {
			// The full closing message, not the one-line summary: this is
			// the hand-off a human applies, and OR-172's agent had already
			// written the exact fix in prose here -- worth keeping whole.
			deniedEdit.HandOff = strings.TrimSpace(res.Final)
			return false, summary, deniedEdit, nil
		}
		return false, summary, nil, nil
	}

	if err := pushBranch(dir, branch); err != nil {
		return false, "", nil, fmt.Errorf("the fix was committed but not pushed: %w", err)
	}
	return true, summary, nil, nil
}

// isEditTool reports whether a tool call can modify a file -- the same set
// Shield is wired to in PreToolUse (see writeSettings's "Edit|Write|
// MultiEdit|NotebookEdit" matcher).
func isEditTool(tool string) bool {
	switch tool {
	case "Edit", "Write", "MultiEdit", "NotebookEdit":
		return true
	default:
		return false
	}
}

// matchedRule returns the first protected-path pattern that matches path,
// or "" when none does.
func matchedRule(patterns []string, path string) string {
	for _, p := range patterns {
		if match.Match(p, path) {
			return p
		}
	}
	return ""
}

// oneLine reduces a closing message to something that fits a console line.
//
// Takes the first non-blank line rather than truncating the whole message
// mid-sentence -- an agent's summary is normally front-loaded ("Fixed the
// off-by-one in X"), so the first line is usually the whole answer and the
// rest is the reasoning that got there.
func oneLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		if t := strings.TrimSpace(line); t != "" {
			const max = 160
			if len(t) > max {
				return t[:max] + "…"
			}
			return t
		}
	}
	return ""
}

func headOf(dir string) (string, error) {
	out, err := exec.Command("git", "-C", dir, "rev-parse", "HEAD").Output()
	if err != nil {
		return "", fmt.Errorf("reading HEAD in %s: %w", dir, err)
	}
	return strings.TrimSpace(string(out)), nil
}

// failingLog fetches the output of the failed job.
//
// --log-failed rather than the whole run: a green job's output is thousands
// of lines of noise that would push the actual error out of the agent's
// context, and paying to read a passing test suite is paying for nothing.
func failingLog(dir, branch string) string {
	if _, err := exec.LookPath("gh"); err != nil {
		return ""
	}
	// Bounded by ghTimeout (OR-128): a hung gh here used to stall the fix
	// loop, and with it the whole watcher, with nothing visible to explain
	// why.
	list, listCancel := ghCommand(dir, "run", "list", "--branch", branch,
		"--limit", "1", "--json", "databaseId,conclusion", "--jq", ".[0].databaseId")
	defer listCancel()
	idOut, err := list.Output()
	id := strings.TrimSpace(string(idOut))
	if err != nil || id == "" || id == "null" {
		return ""
	}

	view, viewCancel := ghCommand(dir, "run", "view", id, "--log-failed")
	defer viewCancel()
	out, err := view.CombinedOutput()
	if err != nil {
		return ""
	}
	return clipLog(string(out))
}

// clipLog bounds what is sent to the model.
//
// Keeps the TAIL. A test runner prints its failures and summary at the end,
// so truncating from the front would drop precisely the part that says what
// went wrong and keep the part that says the dependencies installed.
func clipLog(s string) string {
	const max = 12000
	if len(s) <= max {
		return s
	}
	cut := s[len(s)-max:]
	if i := strings.IndexByte(cut, '\n'); i >= 0 {
		cut = cut[i+1:]
	}
	return "… (earlier output omitted)\n" + cut
}
