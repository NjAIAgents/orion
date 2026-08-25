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

	"github.com/orion-sdlc/orion/internal/notify"
	"github.com/orion-sdlc/orion/internal/quota"
	"github.com/orion-sdlc/orion/internal/workspace"
)

type Options struct {
	Stage      string
	Prompt     string
	MaxMinutes int
	MaxTurns   int
	DryRun     bool
	// NoWait skips quota waiting entirely and fails fast instead. For CI,
	// where sleeping a runner for forty minutes costs real money.
	NoWait bool
}

type Result struct {
	ExitCode int
	Reason   string
	Duration time.Duration
	LogPath  string
	Killed   bool
	Attempts int
	// ResumeAt is set when a quota wall was hit and the wait was too long
	// to sit through. The caller reports it; nothing sleeps on it.
	ResumeAt time.Time
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

	overall := time.Now()
	var last *Result

	for attempt := 1; attempt <= quota.MaxAttempts; attempt++ {
		res, output := runOnce(ws, bin, prompt, opts, attempt)
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
				Level: notify.Blocked, Workspace: ws.ID,
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
				Level: notify.Blocked, Workspace: ws.ID,
				Title: "Orion paused: quota exhausted",
				Body: msg + "\n\nToo long to wait inline. Resume with:\n  orion run " +
					ws.ID + " --stage " + opts.Stage,
			})
			break
		}

		notify.Send(notify.Event{
			Level: notify.Warning, Workspace: ws.ID,
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
	return last, nil
}

// runOnce executes a single attempt and returns its result plus the tail
// of its combined output for quota inspection.
func runOnce(ws *workspace.Workspace, bin, prompt string, opts Options, attempt int) (*Result, string) {
	stamp := time.Now().UTC().Format("20060102-150405")
	logPath := filepath.Join(ws.LogsDir(),
		fmt.Sprintf("%s-%s-a%d.log", stamp, safe(opts.Stage), attempt))

	args := []string{
		"-p", prompt,
		"--settings", ws.SettingsPath(),
		"--max-turns", fmt.Sprint(opts.MaxTurns),
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

	ctx, cancel := context.WithTimeout(context.Background(),
		time.Duration(opts.MaxMinutes)*time.Minute)
	defer cancel()

	cmd := exec.Command(bin, args...)
	cmd.Dir = ws.RepoDir()
	cmd.Env = childEnv(ws)
	// Three destinations: the terminal so a watching user sees progress,
	// the log so the postmortem survives, and the ring buffer so quota
	// detection has something to match without holding the whole stream.
	cmd.Stdout = io.MultiWriter(os.Stdout, logFile, tail)
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
	case <-ctx.Done():
		res.Killed = true
		res.Duration = time.Since(started)
		res.ExitCode = 124 // conventional timeout code
		res.Reason = fmt.Sprintf("killed: exceeded %d minute wall clock", opts.MaxMinutes)
		terminate(cmd, done)
		fmt.Fprintf(logFile, "\n[orion] %s\n", res.Reason)
		notify.Send(notify.Event{
			Level: notify.Blocked, Workspace: ws.ID,
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
	var out []string
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		if drop[k] {
			continue
		}
		out = append(out, kv)
	}
	return append(out, "ORION_WORKSPACE="+ws.ID, "ORION_WORKSPACE_DIR="+ws.Dir)
}

// classify turns an exit code into something a human can act on, checking
// breaker state so "exit 1" becomes "a breaker tripped" when that is what
// actually happened.
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
