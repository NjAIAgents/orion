// Package supervisor runs `claude -p` inside a workspace and enforces
// the limits a hook structurally cannot.
//
// A hook only fires when the agent calls a tool. That leaves three real
// failure modes uncovered:
//
//	wall clock   an agent thinking in circles without calling tools
//	wedged proc  a child that stops responding entirely
//	quota        a provider limit, which is not the agent's fault at all
//
// A parent process sees all three. This is the argument for Orion being a
// binary rather than a set of hook scripts: the supervisor is the only
// layer that can kill a run, and the only one that can wait out a quota
// reset and resume.
package supervisor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/orion-sdlc/orion/internal/budget"
	"github.com/orion-sdlc/orion/internal/config"
	"github.com/orion-sdlc/orion/internal/cost"
	"github.com/orion-sdlc/orion/internal/discovery"
	"github.com/orion-sdlc/orion/internal/events"
	"github.com/orion-sdlc/orion/internal/notify"
	"github.com/orion-sdlc/orion/internal/quota"
	"github.com/orion-sdlc/orion/internal/workspace"
)

type Options struct {
	Stage      string
	Prompt     string
	MaxMinutes int
	MaxTurns   int
	// SkipBudgetCheck bypasses the weekly checkpoint for this run only. It
	// exists for the acknowledge-and-continue path, never as a default.
	SkipBudgetCheck bool
	DryRun          bool
	// Resume continues an existing session rather than starting one. The
	// Prompt is then the next message in that conversation.
	Resume string
	// Model overrides the default: sonnet for advisors, opus for the
	// implementer, haiku for routing. Empty uses whatever the CLI is
	// configured with.
	Model string
	// NoWait skips quota waiting entirely and fails fast instead. For CI,
	// where sleeping a runner for forty minutes costs real money.
	NoWait bool
	// OnActivity is called for each observable thing the agent does, as it
	// does it. Optional: nil means the stream is still logged, just not
	// narrated. Callers use it to write the live event log.
	//
	// Called from the process's output goroutine, so an implementation that
	// blocks holds up the agent it is reporting on.
	OnActivity func(Activity)
	// Actor and Key attribute what this run spends. Both set means the
	// supervisor writes a usage line to the workspace's event log for every
	// attempt it finishes, keyed by the ticket and the persisted actor id, so
	// the ticket's total cost can be aggregated when it closes.
	//
	// Recorded HERE rather than by each caller because this is the only layer
	// that sees every run: the implementation run, the resumed one, each
	// fix-loop re-entry, and the attempts that died on a wall clock or a quota
	// wall before their caller ever got a result back. A run that died still
	// spent tokens, and a caller that returns early on failure would not
	// record them.
	//
	// Empty Actor disables it, which is right for a stage run driven by hand:
	// it belongs to no ticket, so there is nothing to attribute it to.
	Actor string
	Key   string
}

type Result struct {
	// Limit is what this run reported about the account's plan limits.
	Limit RateLimit
	// PeakContext is the largest prompt any single turn sent, in tokens, and
	// ContextWindow is what the model had to hold it. A zero window means
	// the stream never said; the result JSON usually has it instead.
	PeakContext   int
	ContextWindow int
	ExitCode      int
	Reason        string
	Duration      time.Duration
	LogPath       string
	Killed        bool
	Attempts      int
	// ResumeAt is set when a quota wall was hit and the wait was too long
	// to sit through. The caller reports it; nothing sleeps on it.
	ResumeAt time.Time
	// SessionID identifies the conversation, so a caller can CONTINUE it
	// rather than starting again.
	//
	// This is what makes an advisor loop affordable. Without it, answering an
	// implementer's question means re-running from the top: the agent
	// re-reads the spec, re-explores the code and re-derives everything it
	// already knew, paying for the whole context a second time and possibly
	// making different choices. Resuming costs one message.
	SessionID string
	// Final is the agent's closing message. When a run stops to ask
	// something, this is the question.
	Final string
}

const (
	defaultMaxMinutes = 30
	defaultMaxTurns   = 120
	// graceTimeout is how long a killed process gets to exit on SIGINT
	// before it is killed outright. Claude Code flushes its transcript on
	// interrupt, and losing that transcript loses the diagnosis.
	graceTimeout = 8 * time.Second
	// captureBytes bounds the output kept in memory for quota inspection.
	// The full stream still goes to the log; this is only the tail that
	// gets pattern-matched.
	captureBytes = 96 * 1024
)

// Run executes one supervised stage, retrying across quota resets.
func Run(ws *workspace.Workspace, opts Options) (*Result, error) {
	if opts.MaxMinutes <= 0 {
		opts.MaxMinutes = defaultMaxMinutes
	}
	if opts.MaxTurns <= 0 {
		opts.MaxTurns = defaultMaxTurns
	}

	prompt := opts.Prompt
	if prompt == "" {
		var err error
		prompt, err = stagePrompt(ws, opts.Stage)
		if err != nil {
			return nil, err
		}
	}

	bin, err := exec.LookPath("claude")
	if err != nil {
		return nil, errors.New("claude CLI not found on PATH.\n" +
			"  Orion supervises the CLI; it does not embed a model client.\n" +
			"  Check with: orion doctor")
	}
	if err := os.MkdirAll(ws.LogsDir(), 0o755); err != nil {
		return nil, err
	}

	// Discovery gate: refuse to design from an unanswered question.
	//
	// Mirrors require_plan_before_edit rather than inventing a second
	// pattern: an artifact must be complete before the next stage reads it.
	// Without this, one ambiguous sentence propagates into spec, plan,
	// scaffold and a tracker tree, and every stage inherits the invention.
	if stageNeedsIntent(opts.Stage) {
		cfgEarly := config.Load(ws.RepoDir())
		intentPath := filepath.Join(ws.RepoDir(), cfgEarly.Paths.Intent, ws.Task.Slug+".md")
		if a := discovery.Assess(intentPath); a.Found && a.Open > 0 {
			return &Result{ExitCode: 0, Reason: "stopped at the discovery gate"},
				fmt.Errorf("%s", a.GateMessage(ws.ID))
		}
	}

	// Budget checkpoint BEFORE spending, not after. Checking afterwards
	// reports an overrun that has already happened, which is a receipt
	// rather than a control.
	cfg := config.Load(ws.RepoDir())
	lim := budget.Limits{WeeklyUSD: cfg.Budget.WeeklyUSD, WeeklyTokens: cfg.Budget.WeeklyTokens}
	ledger, ledgerErr := budget.Load(workspace.Home())
	if ledgerErr != nil {
		fmt.Fprintf(os.Stderr, "orion: %v\n", ledgerErr)
	}
	if st := ledger.Status(lim); st.Crossed > 0 && !opts.SkipBudgetCheck {
		return &Result{
			ExitCode: 0,
			Reason:   fmt.Sprintf("stopped at the %d%% budget checkpoint", st.Crossed),
		}, fmt.Errorf("%s", st.Message())
	}

	overall := time.Now()
	var last *Result

	for attempt := 1; attempt <= quota.MaxAttempts; attempt++ {
		res, output := runOnce(ws, bin, prompt, opts, attempt)
		recordUsage(ws, opts.Stage, output)
		recordTicketCost(ws, opts, res, output)
		// Numerator from the stream (a peak over turns), denominator from
		// wherever it was actually reported. Splitting the two sources is
		// deliberate: the window is a static property of the model and is
		// safe to take from the result JSON, while the occupancy is not --
		// taking THAT from the result JSON is what produced "656%".
		reportContextPressure(res.PeakContext, windowFor(res, output))
		res.Attempts = attempt
		last = res

		if res.ExitCode == 0 || opts.DryRun {
			break
		}

		// A killed run is Orion's own decision, never a provider limit.
		// Inspecting it for quota would misattribute a timeout.
		if res.Killed {
			break
		}

		v := quota.Inspect(output, attempt, time.Now())
		if !v.Exhausted {
			break
		}

		msg := v.Message(attempt)
		appendLog(res.LogPath, "[orion] "+msg)

		if opts.NoWait {
			res.Reason = "quota exhausted (--no-wait set, not waiting)"
			notify.Send(notify.Event{
				Level: notify.Blocked, Workspace: ws.ID, Channel: channelFor(ws),
				Title: "Orion stopped: quota exhausted",
				Body:  msg + "\nRe-run when the limit resets: orion run " + ws.ID + " --stage " + opts.Stage,
			})
			break
		}

		if !v.ShouldWaitInline(attempt) {
			// Too long to sit through, or out of attempts. Record when to
			// come back and hand control to the human rather than holding a
			// process open for hours.
			res.ResumeAt = v.ResetAt
			res.Reason = fmt.Sprintf("quota exhausted; resume after %s",
				v.ResetAt.Local().Format("15:04 MST"))
			ws.Task.Status = "waiting-on-quota"
			ws.Task.ResumeAt = v.ResetAt
			_ = ws.SaveTask()

			notify.Send(notify.Event{
				Level: notify.Blocked, Workspace: ws.ID, Channel: channelFor(ws),
				Title: "Orion paused: quota exhausted",
				Body: msg + "\n\nToo long to wait inline. Resume with:\n  orion run " +
					ws.ID + " --stage " + opts.Stage,
			})
			break
		}

		notify.Send(notify.Event{
			Level: notify.Warning, Workspace: ws.ID, Channel: channelFor(ws),
			Title: fmt.Sprintf("Orion waiting %s for quota reset", v.Wait.Round(time.Second)),
			Body:  msg + fmt.Sprintf("\nWill retry automatically (attempt %d of %d).", attempt+1, quota.MaxAttempts),
		})

		ws.Task.Status = "waiting-on-quota"
		ws.Task.ResumeAt = v.ResetAt
		_ = ws.SaveTask()

		if !sleepInterruptible(v.Wait) {
			res.Reason = "interrupted while waiting for quota reset"
			break
		}
		appendLog(res.LogPath, fmt.Sprintf("[orion] quota wait over, retrying (attempt %d)", attempt+1))
	}

	if last == nil {
		return nil, errors.New("supervisor produced no result")
	}
	last.Duration = time.Since(overall)

	ws.Task.Stage = opts.Stage
	ws.Task.Status = statusFor(last)
	if last.ResumeAt.IsZero() {
		ws.Task.ResumeAt = time.Time{}
	}
	ws.Task.Runs = append(ws.Task.Runs, workspace.RunRec{
		Stage: opts.Stage, StartedAt: overall.UTC(),
		Seconds: last.Duration.Seconds(), ExitCode: last.ExitCode,
		Reason: last.Reason, Log: last.LogPath, Attempts: last.Attempts,
	})
	if err := ws.SaveTask(); err != nil {
		fmt.Fprintf(os.Stderr, "orion: could not update task.json: %v\n", err)
	}

	if last.ExitCode != 0 {
		return last, fmt.Errorf("stage %s failed: %s", opts.Stage, last.Reason)
	}
	// Notify on failure, not only on the quota and timeout paths that
	// already did. A supervisor that stays silent when a stage fails is one
	// you have to poll, and a supervisor you have to poll is one you stop
	// checking.
	if last != nil && last.ExitCode != 0 {
		notify.Send(notify.Event{
			Level: notify.Blocked, Workspace: ws.ID, Channel: channelFor(ws),
			Title: fmt.Sprintf("orion: %s failed in %s", opts.Stage, ws.ID),
			Body:  fmt.Sprintf("exit %d after %d attempt(s): %s\nlog: %s", last.ExitCode, last.Attempts, last.Reason, last.LogPath),
		})
	}
	return last, nil
}

// stageNeedsIntent reports whether a stage designs from the captured intent.
// Intent itself is excluded for the obvious reason, and the later build and
// ship stages are excluded because by then the spec and plan are the
// governing artifacts and re-litigating intent would block finished work.
func stageNeedsIntent(stage string) bool {
	switch strings.ToLower(stage) {
	case "spec", "design", "plan", "scaffold", "decompose":
		return true
	}
	return false
}

// channelFor returns the workspace's Slack channel id, or "" when it has
// none. Keeping this in one place means a new notify call site cannot
// forget to route a project's message to that project's room.
func channelFor(ws *workspace.Workspace) string {
	if ws.Task.Slack != nil {
		return ws.Task.Slack.ID
	}
	return ""
}

// reportContextPressure warns when a stage ran close to filling its window.
//
// peak is the largest single turn's prompt, measured off the stream; window
// is what the run said it had. Both come from the run itself, and when
// either is missing this says NOTHING rather than guessing -- the previous
// version guessed, and printed "context reached 656% of the 1M window",
// which was cumulative throughput divided by a window size and could not be
// true of any context.
//
// The threshold is high because the consequence is real: Orion cannot
// compact mid-run, so a stage that fills its window will start losing the
// earliest part of its own reasoning.
// windowFor finds the context window, preferring the stream and falling back
// to the result JSON.
//
// Both are legitimate sources for THIS number, unlike the occupancy: the
// window is a fixed property of the model, so a cumulative document reports
// it just as accurately as a per-turn one. The CLI version in use here does
// not put context_window on the stream at all -- only modelUsage in the final
// result carries it -- so without this fallback the warning would go silent
// forever instead of being wrong, which is quieter but no more useful.
func windowFor(res *Result, out string) int {
	if res != nil && res.ContextWindow > 0 {
		return res.ContextWindow
	}
	if run, ok := budget.FromResultJSON(out); ok {
		return run.ContextWindow
	}
	return 0
}

func reportContextPressure(peak, window int) {
	if peak <= 0 || window <= 0 {
		return
	}
	p := peak * 100 / window
	if p < 70 {
		return
	}
	fmt.Fprintf(os.Stderr,
		"orion: context peaked at %d%% of the %s window on this stage (%s in one turn).\n"+
			"  Orion cannot compact mid-run; the CLI exposes no control for it.\n"+
			"  If this recurs, split the stage: each stage starts a fresh session.\n",
		p, humanTokens(window), humanTokens(peak))
}

// recordUsage books what a run actually cost. Failures here are reported and
// never fatal: losing an accounting row must not lose the work.
func recordUsage(ws *workspace.Workspace, stage, out string) {
	run, ok := budget.FromResultJSON(out)
	if !ok {
		return
	}
	run.Workspace, run.Stage = ws.ID, stage
	ledger, _ := budget.Load(workspace.Home())
	ledger.Record(run)
	if err := ledger.Save(workspace.Home()); err != nil {
		fmt.Fprintf(os.Stderr, "orion: could not record usage: %v\n", err)
		return
	}
	// Context pressure is NOT computed here any more. The result JSON reports
	// cumulative session usage, which cannot answer "how full did the context
	// get" -- see reportContextPressure, which reads the peak off the stream.
}

// recordTicketCost books this attempt against the TICKET, in the workspace's
// event log, so what a ticket cost can be reported when it closes.
//
// Separate from recordUsage above, which books the same run against the
// WEEKLY BUDGET in a ledger keyed by workspace and stage. Two questions, two
// records: one gates spending across all work, the other attributes spending
// to one ticket and one actor. Deriving either from the other would mean
// keeping a ledger of every run forever, or a per-ticket report that cannot
// name who spent what.
//
// An attempt that reported no usage is recorded ANYWAY, marked, rather than
// dropped: a run that died before its result JSON still spent everything it
// sent, and a report that silently omits it presents a lowball total as
// complete.
//
// Every failure here is silent by design. Losing an accounting line must not
// lose the work, and the log is opened per run rather than held open because
// the supervisor has no lifecycle to hang it on.
func recordTicketCost(ws *workspace.Workspace, opts Options, res *Result, out string) {
	if res == nil || opts.DryRun || opts.Actor == "" || opts.Key == "" {
		return
	}
	log, err := events.Open(events.Path(ws.Dir), events.Event{})
	if err != nil {
		return
	}
	defer func() { _ = log.Close() }()
	run, ok := budget.FromResultJSON(out)
	cost.Record(log, opts.Actor, opts.Key,
		cost.FromBudgetRun(run, ok, res.ExitCode != 0, res.Reason, res.Duration))
}

func humanTokens(n int) string {
	if n >= 1_000_000 {
		return fmt.Sprintf("%.0fM", float64(n)/1e6)
	}
	return fmt.Sprintf("%dk", n/1000)
}

// runOnce executes a single attempt and returns its result plus the tail
// of its combined output for quota inspection.
func runOnce(ws *workspace.Workspace, bin, prompt string, opts Options, attempt int) (*Result, string) {
	stamp := time.Now().UTC().Format("20060102-150405")
	logPath := filepath.Join(ws.LogsDir(),
		fmt.Sprintf("%s-%s-a%d.log", stamp, safe(opts.Stage), attempt))

	args := []string{}
	if opts.Resume != "" {
		// Continue the existing conversation. The prompt here is the ANSWER
		// to what the agent asked, not a fresh instruction.
		args = append(args, "--resume", opts.Resume)
	}
	args = append(args,
		"-p", prompt,
		"--settings", ws.SettingsPath(),
		// stream-json rather than json: both carry the run's own usage and
		// cost (text output reports neither), but json emits a single object
		// AT EXIT, so a forty minute run is indistinguishable from a hung one
		// while it happens. NDJSON lets the same run be narrated live.
		//
		// --verbose is not optional here: the CLI refuses stream-json in
		// print mode without it.
		"--output-format", "stream-json",
		"--verbose",
		// Undocumented: --max-turns is absent from `claude --help` but is
		// accepted (exit 0). Passed as a best-effort belt to the braces of
		// Orion's own tool-call breaker and wall clock, which are the
		// controls actually relied upon.
		"--max-turns", fmt.Sprint(opts.MaxTurns),
	)
	if opts.Model != "" {
		args = append(args, "--model", opts.Model)
	}

	if opts.DryRun {
		fmt.Printf("would run: %s %s\n  cwd: %s\n", bin, strings.Join(quoteAll(args), " "), ws.RepoDir())
		return &Result{Reason: "dry run", LogPath: logPath}, ""
	}

	logFile, err := os.Create(logPath)
	if err != nil {
		return &Result{ExitCode: 1, Reason: "could not open log: " + err.Error(), LogPath: logPath}, ""
	}
	defer logFile.Close()

	tail := &ringWriter{max: captureBytes}
	activity := newActivityWriter(ws.RepoDir(), opts.OnActivity)

	ctx, cancel := context.WithTimeout(context.Background(),
		time.Duration(opts.MaxMinutes)*time.Minute)
	defer cancel()

	cmd := exec.Command(bin, args...)
	cmd.Dir = ws.RepoDir()
	cmd.Env = childEnv(ws)
	// Four destinations, and NOT the terminal: with stream-json the raw
	// stream is machine output, and printing it would replace the missing
	// progress with unreadable progress. The activity writer produces the
	// human-facing lines; the log keeps the stream verbatim for postmortems;
	// the ring buffer holds the tail for quota detection and for the closing
	// result object.
	cmd.Stdout = io.MultiWriter(logFile, tail, activity)
	// stderr stays on the terminal. It is where the CLI reports its own
	// failures -- a bad flag, an auth problem -- and swallowing those would
	// turn a clear error into a silent empty run.
	cmd.Stderr = io.MultiWriter(os.Stderr, logFile, tail)

	started := time.Now()
	if err := cmd.Start(); err != nil {
		return &Result{ExitCode: 1, Reason: "starting claude: " + err.Error(), LogPath: logPath}, ""
	}

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	res := &Result{LogPath: logPath}
	select {
	case err := <-done:
		res.Duration = time.Since(started)
		res.ExitCode = exitCode(err)
		res.Reason = classify(res.ExitCode, ws)
		// The session id and the closing message come from the run's own JSON
		// result. Both are needed to answer a question the agent asked: the
		// message IS the question, and the id is what lets the conversation
		// continue rather than start over.
		res.SessionID, res.Final = sessionAndFinal(tail.String())
		// The plan limit the run itself reported. Recorded on every result,
		// not only on failures: a run that succeeded while already on
		// overage is the last one that will, and the caller deciding
		// whether to start another needs to know that now rather than by
		// being refused next time.
		res.Limit = activity.Limit()
		// Measured off the stream, per turn, so it is a peak rather than a
		// session total. Carried on the Result rather than reported here,
		// because the WINDOW it must be divided by is not always on the
		// stream -- the caller has the result JSON, which is.
		res.PeakContext, res.ContextWindow = activity.Context()
		// A process that exits 0 without ever emitting the stream's own
		// "result" line did not finish -- it was cut off mid-run (a sandbox
		// rejection killing the CLI, an OOM, a crash after a partial flush).
		// Left alone this reads as a clean success with an empty SessionID
		// and no cost recorded, and the caller (orion watch) never learns
		// anything went wrong (OR-127). Re-report it as the failure it is,
		// through the same Reason/ExitCode the caller already checks.
		if res.ExitCode == 0 && !activity.SawResult() {
			res.ExitCode = 1
			res.Reason = "claude exited without ever emitting a stream result: " +
				"the process was cut off mid-run rather than finishing"
			fmt.Fprintf(logFile, "\n[orion] %s\n", res.Reason)
		}
	case <-ctx.Done():
		res.Killed = true
		res.Duration = time.Since(started)
		res.ExitCode = 124 // conventional timeout code
		res.Reason = fmt.Sprintf("killed: exceeded %d minute wall clock", opts.MaxMinutes)
		terminate(cmd, done)
		fmt.Fprintf(logFile, "\n[orion] %s\n", res.Reason)
		notify.Send(notify.Event{
			Level: notify.Blocked, Workspace: ws.ID, Channel: channelFor(ws),
			Title: "Orion killed a runaway session",
			Body:  res.Reason + "\nLog: " + logPath,
		})
	}
	return res, tail.String()
}

// sleepInterruptible waits, returning false if the user interrupts.
//
// Signal handling is the point, not decoration: a forty minute quota wait
// that ignores ctrl-c strands the user's terminal, and the obvious escape
// (kill -9) skips the deferred cleanup that closes the log.
func sleepInterruptible(d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sig)

	deadline := time.Now().Add(d)
	tick := time.NewTicker(5 * time.Minute)
	defer tick.Stop()

	for {
		select {
		case <-timer.C:
			return true
		case <-sig:
			fmt.Println("\norion: interrupted; not retrying.")
			return false
		case <-tick.C:
			// Periodic proof of life. A silent process that will not respond
			// for forty minutes looks identical to one that has hung.
			fmt.Printf("orion: still waiting on quota, %s remaining (ctrl-c to stop)\n",
				time.Until(deadline).Round(time.Minute))
		}
	}
}

// ringWriter keeps only the last max bytes written. Bounded on purpose:
// an agent can emit hundreds of megabytes, and quota errors appear at the
// end of a failed run, never the beginning.
type ringWriter struct {
	mu  sync.Mutex
	buf []byte
	max int
}

func (r *ringWriter) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.buf = append(r.buf, p...)
	if len(r.buf) > r.max {
		r.buf = r.buf[len(r.buf)-r.max:]
	}
	return len(p), nil
}

func (r *ringWriter) String() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return string(r.buf)
}

func appendLog(path, line string) {
	if path == "" {
		return
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "\n%s %s\n", time.Now().UTC().Format(time.RFC3339), line)
}

// terminate asks politely, then insists. SIGINT lets Claude Code flush
// its transcript; without that flush the log is useless for diagnosis.
func terminate(cmd *exec.Cmd, done <-chan error) {
	if cmd.Process == nil {
		return
	}
	_ = cmd.Process.Signal(os.Interrupt)
	select {
	case <-done:
	case <-time.After(graceTimeout):
		_ = cmd.Process.Kill()
		<-done
	}
}

// childEnv builds the environment for the child, stripping the secrets a
// supervised run has no business seeing. The OS sandbox denies these too;
// doing it here as well means a misconfigured sandbox is not the only
// thing standing between an agent and a cloud credential.
func childEnv(ws *workspace.Workspace) []string {
	drop := map[string]bool{
		"AWS_SECRET_ACCESS_KEY": true, "AWS_SESSION_TOKEN": true,
		"GITHUB_TOKEN": true, "GH_TOKEN": true,
		"NPM_TOKEN": true, "DOCKER_PASSWORD": true, "KUBECONFIG": true,
	}
	// Any inherited git authorship is dropped too, so the alias below is
	// decided here rather than by whatever the parent shell happened to
	// export.
	drop["GIT_AUTHOR_NAME"] = true
	drop["GIT_AUTHOR_EMAIL"] = true

	var out []string
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		if drop[k] {
			continue
		}
		out = append(out, kv)
	}
	out = append(out, "ORION_WORKSPACE="+ws.ID, "ORION_WORKSPACE_DIR="+ws.Dir)
	out = append(out, agentAuthorEnv(ws.RepoDir())...)
	return append(out, agentTrackerEnv(ws.RepoDir())...)
}

// agentAuthorEnv marks commits the agent makes as its own, in both fields.
//
// This used to set only GIT_AUTHOR_*, on the reasoning that the alias should
// say who wrote the change while the committer said who was answerable for
// it landing. That reasoning was sound and the result was still wrong: the
// commits went up authored orionbot and GitHub displayed the account owner's
// name and avatar, because GitHub resolves commits by EMAIL and shows the
// COMMITTER for the "X committed" line. Leaving the committer as the human
// while sharing the human's address meant the alias existed only in `git log`
// -- true, and invisible in the place anyone reviewing a pull request looks.
//
// A commit landing on a branch is not an approval, either. Orion pushes and
// opens the pull request; the human approves at the MERGE, which is where
// accountability actually attaches. Naming the human as committer implied a
// review that had not happened yet.
//
// The email is what decides the display. See config.AgentAuthorEmail for the
// trade this makes with contribution graphs and email allowlists.
func agentAuthorEnv(repoDir string) []string {
	cfg := config.Load(repoDir)
	name := strings.TrimSpace(cfg.VCS.AgentAuthorName)
	if name == "" {
		return nil // opted out; commits are authored as the human
	}
	email := strings.TrimSpace(cfg.VCS.AgentAuthorEmail)
	if email == "" {
		out, err := exec.Command("git", "-C", repoDir, "config", "user.email").Output()
		email = strings.TrimSpace(string(out))
		if err != nil || email == "" {
			// Git refuses to commit with an empty author email, so an alias
			// with no address would break every commit the agent makes. Not
			// setting the alias is far better than that.
			return nil
		}
	}
	return []string{
		"GIT_AUTHOR_NAME=" + name, "GIT_AUTHOR_EMAIL=" + email,
		// Both, or the alias is invisible: GitHub's "X committed" line reads
		// the committer, and a mismatched pair also makes every agent commit
		// display the misleading "authored and committed by different people"
		// badge on a change no second person touched.
		"GIT_COMMITTER_NAME=" + name, "GIT_COMMITTER_EMAIL=" + email,
	}
}

// agentTrackerEnv publishes the tracker contract to the child.
//
// Orion's own Go code creates the PROJECT and stops there; the Epic/Story/
// Task tree is filed by the agent through nj-agents, so a label can only be
// applied where the issues are actually created. These variables are that
// contract: the skill reads them and stamps every issue it files.
//
// Stated plainly because it is a real limit -- exporting a variable does not
// make an agent use it. Until the PM skill reads ORION_TRACKER_LABEL, this
// makes the intent available and enforces nothing. The alternative, filing
// the tree from Go, would duplicate decomposition logic that belongs in the
// skill and drift from it.
func agentTrackerEnv(repoDir string) []string {
	cfg := config.Load(repoDir)
	if !cfg.Tracker.Enabled {
		return nil
	}
	var out []string
	if k := strings.TrimSpace(cfg.Tracker.ProjectKey); k != "" {
		out = append(out, "ORION_TRACKER_PROJECT="+k)
	}
	if l := strings.TrimSpace(cfg.Tracker.AgentLabel); l != "" {
		out = append(out, "ORION_TRACKER_LABEL="+l)
	}
	if n := strings.TrimSpace(cfg.VCS.AgentAuthorName); n != "" {
		// The same alias as the git author, so a commit and the issue it
		// closes carry one name rather than two names for one actor.
		out = append(out, "ORION_AGENT_NAME="+n)
	}
	return out
}

// classify turns an exit code into something a human can act on, checking
// breaker state so "exit 1" becomes "a breaker tripped" when that is what
// actually happened.
// sessionAndFinal pulls the session id and the agent's last message out of
// the --output-format json result.
//
// Scans for the LAST JSON object in the stream rather than parsing the whole
// thing: the tail buffer may begin mid-object, and with stream-json there are
// several. Failing to find them is not an error -- it costs the ability to
// resume, which degrades to a fresh run, never to a wrong one.
func sessionAndFinal(out string) (sessionID, final string) {
	dec := json.NewDecoder(strings.NewReader(out))
	for {
		var m map[string]any
		if err := dec.Decode(&m); err != nil {
			break
		}
		if v, ok := m["session_id"].(string); ok && v != "" {
			sessionID = v
		}
		if v, ok := m["result"].(string); ok && strings.TrimSpace(v) != "" {
			final = strings.TrimSpace(v)
		}
	}
	if sessionID != "" || final != "" {
		return sessionID, final
	}
	// The buffer probably starts mid-object. Try from the last opening brace
	// that parses, which is the common shape for a truncated tail.
	if i := strings.LastIndex(out, "{\"is_error\""); i >= 0 {
		var m map[string]any
		if json.Unmarshal([]byte(out[i:]), &m) == nil {
			sid, _ := m["session_id"].(string)
			fin, _ := m["result"].(string)
			return sid, strings.TrimSpace(fin)
		}
	}
	return "", ""
}

func classify(code int, ws *workspace.Workspace) string {
	if b, err := os.ReadFile(filepath.Join(ws.StateDir(), "tripped")); err == nil {
		if s := strings.TrimSpace(string(b)); s != "" {
			return "breaker tripped: " + s
		}
	}
	switch code {
	case 0:
		return "completed"
	case 124:
		return "timed out"
	case 130:
		return "interrupted"
	default:
		return fmt.Sprintf("claude exited %d", code)
	}
}

func statusFor(r *Result) string {
	switch {
	case !r.ResumeAt.IsZero():
		return "waiting-on-quota"
	case r.Killed:
		return "killed"
	case r.ExitCode == 0:
		return "ready-for-review"
	default:
		return "failed"
	}
}

func exitCode(err error) int {
	if err == nil {
		return 0
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode()
	}
	return 1
}

func safe(s string) string {
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' {
			b.WriteRune(r)
		} else {
			b.WriteByte('-')
		}
	}
	if b.Len() == 0 {
		return "stage"
	}
	return b.String()
}

func quoteAll(args []string) []string {
	out := make([]string, len(args))
	for i, a := range args {
		if strings.ContainsAny(a, " \t\"'") {
			out[i] = fmt.Sprintf("%q", a)
		} else {
			out[i] = a
		}
	}
	return out
}
