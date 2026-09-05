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

// fixWatch is the ci-fix run's activity callback: the shared logger, plus
// OR-174's watch for an edit the sandbox refused.
//
// It exists as a named type rather than a closure at the call site because
// TestNoHandRolledOnActivity bans an inline OnActivity outright, and that ban
// is the point of OR-176 -- a second, hand-rolled callback is exactly how the
// fix loop came to print unattributed lines and log nothing. Composing here
// keeps both behaviours on ONE activity stream, so the console line and the
// denial can never disagree about what the agent did.
type fixWatch struct {
	say       func(supervisor.Activity)
	protected []string
	denied    *collect.PolicyDenial
}

func newFixWatch(log *events.Log, w io.Writer, key string, protected []string) *fixWatch {
	return &fixWatch{say: fixActivity(log, w, key), protected: protected}
}

// record draws the line, writes the event, and notices a refused edit.
//
// First denial wins: a run refused twice was refused for the same reason, and
// the first is the one whose context the summary explains.
func (f *fixWatch) record(a supervisor.Activity) {
	f.say(a)
	if f.denied != nil || !isEditTool(a.Tool) {
		return
	}
	if rule := matchedRule(f.protected, a.Detail); rule != "" {
		f.denied = &collect.PolicyDenial{Tool: a.Tool, Path: a.Detail, Rule: rule}
	}
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
func fixRun(ws *workspace.Workspace, key, branch, failedOn, failure string, log *events.Log) (bool, string, *collect.PolicyDenial, error) {
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

	before, err := headOf(dir)
	if err != nil {
		return false, "", nil, err
	}

	cfg := config.Load(ws.RepoDir())
	jobWS := *ws
	jobWS.RepoPath = dir

	// The full log, not the summary: "test (failure)" names which check went
	// red and nothing about why, and an agent handed only that has to re-run
	// the suite locally to discover what a log already says.
	//
	// But the log is TRIAGED before it is handed over, not embedded whole. A
	// raw CI log runs to thousands of lines, and everything in the fix run's
	// prompt rides along on every one of its turns; a subagent reads it once
	// in its own context and returns only its answer (OR-143).
	// THE LOG COMES FROM WHERE THE FAILURE HAPPENED (OR-336).
	//
	// Usually that is the ticket's own branch. For a batch culprit it is the
	// batch ref: the members were tested together, so the member's own
	// branch has a stale or green run and searching it found nothing -- the
	// fix agent was then handed the conviction sentence with no log, and
	// spent two attempts reporting that it could not see one.
	//
	// When no log is reachable the prompt SAYS SO rather than quietly
	// degrading to the bare failure line. An agent told the log is missing
	// can say what it needs; one handed a conviction cannot tell the
	// difference between "no log" and "this is the whole failure".
	failedRef := failedOn
	if failedRef == "" {
		failedRef = branch
	}
	detail := failure
	if full := failingLog(dir, failedRef); strings.TrimSpace(full) != "" {
		detail = triageLog(&jobWS, key, failedRef, full)
	} else {
		detail = failure + "\n\n(No CI log could be read for " + failedRef +
			". What is above is all Orion has: if it does not name the failing\n" +
			"test, say that you cannot see the failure rather than guessing at one.)"
	}

	// Caught live, off the same activity stream the console line below is
	// built from -- not guessed at afterwards from the agent's prose. Shield
	// (internal/hook.Shield) blocks every Edit/Write/MultiEdit/NotebookEdit
	// that targets cfg.Paths.Protected unconditionally, so a tool call seen
	// here against that list is proof the sandbox refused it, regardless of
	// whether the agent said so in its closing message (OR-174).
	watch := newFixWatch(log, w, key, cfg.Paths.Protected)
	o := fixOptions(key, branch, detail, cfg)
	o.OnActivity = watch.record
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
		if watch.denied != nil {
			// The full closing message, not the one-line summary: this is
			// the hand-off a human applies, and OR-172's agent had already
			// written the exact fix in prose here -- worth keeping whole.
			watch.denied.HandOff = strings.TrimSpace(res.Final)
			return false, summary, watch.denied, nil
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
// Bounds for the log-triage subagent, deliberately far tighter than the fix
// run's own cfg.Limits: this is a mechanical read-and-report, not the work of
// fixing anything, and a run that gets stuck hunting has stopped being cheap.
const (
	triageMaxMinutes = 5
	triageMaxTurns   = 10
)

// triageOptions is what the log-triage subagent runs with, separated from
// triageLog so the actor, model and prompt it is configured with can be
// asserted without spawning a process -- the same reason fixOptions is split
// from fixRun.
func triageOptions(key, branch, log string) supervisor.Options {
	return supervisor.Options{
		Stage:      "log-triage",
		Prompt:     supervisor.LogTriagePrompt(branch, log),
		MaxMinutes: triageMaxMinutes,
		MaxTurns:   triageMaxTurns,
		// Its own actor and its own model, pinned cheap rather than inherited
		// from the fix run: this is a mechanical read, not the implementer's
		// judgement, and pinning it is what makes the split a cost win instead
		// of a second expensive run. Attributed to the same ticket key so its
		// spend shows up as its own row in that ticket's cost report rather
		// than hiding inside the fix run's total (OR-143).
		Actor: events.ActorLogTriage, Key: key,
		Model:  actors.Model(events.ActorLogTriage),
		Effort: actors.Effort(events.ActorLogTriage),
	}
}

// triageLog reduces a failing job's raw log to a short report of what broke
// and why, through a subagent that reads the log in its own context and
// returns only its answer -- the log itself never reaches the fix run.
//
// Falls back to the raw log on any failure of the subagent itself. The fix run
// still needs something to react to, and a raw log an agent has to work harder
// to read loses less than a triage step that silently produced nothing.
//
// The report is written to the event log for the same reason OR-129 made the
// fix loop record its own closing summary: what a subagent returns is all the
// parent session ever sees of it, so an answer that is not written down is
// gone the moment this function returns.
func triageLog(jobWS *workspace.Workspace, key, branch, rawLog string) string {
	res, err := supervisor.Run(jobWS, triageOptions(key, branch, rawLog))
	if err != nil || res == nil || strings.TrimSpace(res.Final) == "" {
		return rawLog
	}
	report := strings.TrimSpace(res.Final)

	if l, openErr := events.Open(events.Path(jobWS.Dir), events.Event{}); openErr == nil {
		defer func() { _ = l.Close() }()
		l.Emit(events.Event{Kind: events.KindNote, Actor: events.ActorLogTriage, Key: key,
			Msg: "triaged the failing log: " + oneLine(report)})
	}
	return report
}

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
