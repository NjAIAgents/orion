package supervisor

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/orion-sdlc/orion/internal/agentcfg"
	"github.com/orion-sdlc/orion/internal/budget"
	"github.com/orion-sdlc/orion/internal/config"
	"github.com/orion-sdlc/orion/internal/events"
	"github.com/orion-sdlc/orion/internal/workspace"
)

// fakeClaude puts a `claude` on PATH that records having been invoked, and
// returns the path of the marker file. Its whole purpose is to make "the
// agent was never launched" an observable fact rather than an assumption.
func fakeClaude(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	canary := filepath.Join(dir, "was-launched")
	bin := filepath.Join(dir, "claude")
	if err := os.WriteFile(bin,
		[]byte("#!/bin/sh\ntouch "+canary+"\necho '{}'\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return canary
}

// shrinkWallClock makes a MaxMinutes deadline and its post-SIGINT grace
// reachable in milliseconds, and restores the real clocks afterwards.
//
// The unit, not MaxMinutes itself: a test still passes the MaxMinutes a
// caller would, so it exercises the same arithmetic and the same "exceeded
// %d minute wall clock" reason string. Only the second the unit stands for
// gets shorter. Mirrors internal/events' shrink() (OR-9).
func shrinkWallClock(t *testing.T, unit, grace time.Duration) {
	t.Helper()
	ou, og := wallClockUnit, graceTimeout
	wallClockUnit, graceTimeout = unit, grace
	t.Cleanup(func() { wallClockUnit, graceTimeout = ou, og })
}

// ws builds a workspace on disk with a repo, state and logs directory.
func ws(t *testing.T, cfgJSON string) *workspace.Workspace {
	t.Helper()
	home := t.TempDir()
	t.Setenv("ORION_HOME", home)
	dir := filepath.Join(home, "projects", "t-1")
	for _, d := range []string{"repo", ".orion/logs", ".orion/state"} {
		if err := os.MkdirAll(filepath.Join(dir, d), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if cfgJSON != "" {
		if err := os.WriteFile(filepath.Join(dir, "repo", "orion.json"), []byte(cfgJSON), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return &workspace.Workspace{
		ID: "t-1", Dir: dir,
		Task: workspace.Task{ID: "t-1", Slug: "thing", Idea: "an idea"},
	}
}

// The single most valuable test in this package: a crossed budget must stop
// a run BEFORE it spends anything. Checking afterwards is a receipt, not a
// control, and the failure mode is money.
func TestRunStopsAtTheBudgetCheckpointBeforeSpending(t *testing.T) {
	w := ws(t, `{"budget":{"weekly_tokens":1000,"pause_at_percent":[50,75,90,95]}}`)

	// Book spend that puts the window past the first checkpoint.
	l, _ := budget.Load(workspace.Home())
	l.Record(budget.Run{At: time.Now().UTC(), InputTokens: 900, OutputTokens: 0})
	if err := l.Save(workspace.Home()); err != nil {
		t.Fatal(err)
	}

	// A fake `claude` that must never run. Run resolves the binary by name
	// before the gate, so the canary has to be a real, findable claude --
	// otherwise the test would pass for the wrong reason (LookPath failing).
	canary := fakeClaude(t)

	res, err := Run(w, Options{Stage: "intent", Prompt: "do a thing",
		MaxMinutes: 1, MaxTurns: 1})
	if err == nil {
		t.Fatal("the run proceeded past a crossed budget checkpoint")
	}
	if !strings.Contains(err.Error(), "BUDGET CHECKPOINT") {
		t.Errorf("error should be the checkpoint message, got: %v", err)
	}
	if res == nil || !strings.Contains(res.Reason, "budget checkpoint") {
		t.Errorf("result = %+v", res)
	}
	if _, statErr := os.Stat(canary); statErr == nil {
		t.Fatal("the agent was launched despite the checkpoint: money was spent")
	}
}

// The acknowledge-and-continue path must be the only way past it.
func TestSkipBudgetCheckLetsAnAcknowledgedRunProceed(t *testing.T) {
	w := ws(t, `{"budget":{"weekly_tokens":1000}}`)
	l, _ := budget.Load(workspace.Home())
	l.Record(budget.Run{At: time.Now().UTC(), InputTokens: 900})
	_ = l.Save(workspace.Home())

	fakeClaude(t)
	res, err := Run(w, Options{Stage: "intent", Prompt: "x", MaxMinutes: 1,
		MaxTurns: 1, DryRun: true, SkipBudgetCheck: true})
	if err != nil {
		t.Fatalf("an acknowledged run must proceed: %v", err)
	}
	if res == nil || res.Reason != "dry run" {
		t.Errorf("result = %+v", res)
	}
}

func TestClassifyNamesTheRealCause(t *testing.T) {
	w := ws(t, "")
	for _, tc := range []struct {
		code int
		want string
	}{
		{0, "completed"},
		{124, "timed out"},
		{130, "interrupted"},
		{7, "claude exited 7"},
	} {
		if got := classify(tc.code, w); got != tc.want {
			t.Errorf("classify(%d) = %q, want %q", tc.code, got, tc.want)
		}
	}

	// A tripped breaker outranks the exit code: "exit 1" is true and useless,
	// "breaker tripped: 400 tool calls" is what a person can act on.
	if err := os.WriteFile(filepath.Join(w.StateDir(), "tripped"),
		[]byte("400 tool calls\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := classify(1, w); !strings.Contains(got, "breaker tripped") {
		t.Errorf("classify with a tripped breaker = %q", got)
	}
	if got := classify(0, w); !strings.Contains(got, "breaker tripped") {
		t.Errorf("a trip must be reported even on exit 0, got %q", got)
	}
}

func TestStatusForOrdersOutcomesByUrgency(t *testing.T) {
	for _, tc := range []struct {
		r    Result
		want string
	}{
		{Result{ExitCode: 0}, "ready-for-review"},
		{Result{ExitCode: 1}, "failed"},
		{Result{ExitCode: 0, Killed: true}, "killed"},
		{Result{ExitCode: 1, ResumeAt: time.Now()}, "waiting-on-quota"},
		// Quota outranks killed: the run was stopped BY the wall clock while
		// already waiting, and "retry later" is the useful reading.
		{Result{Killed: true, ResumeAt: time.Now()}, "waiting-on-quota"},
	} {
		if got := statusFor(&tc.r); got != tc.want {
			t.Errorf("statusFor(%+v) = %q, want %q", tc.r, got, tc.want)
		}
	}
}

func TestStageNeedsIntent(t *testing.T) {
	for _, s := range []string{"spec", "design", "plan", "scaffold", "decompose", "SPEC", "Plan"} {
		if !stageNeedsIntent(s) {
			t.Errorf("%q must require a captured intent", s)
		}
	}
	for _, s := range []string{"intent", "forge", "", "review"} {
		if stageNeedsIntent(s) {
			t.Errorf("%q must not require intent", s)
		}
	}
}

func TestChannelForIsEmptyWithoutASlackChannel(t *testing.T) {
	w := ws(t, "")
	if got := channelFor(w); got != "" {
		t.Errorf("got %q", got)
	}
	w.Task.Slack = &workspace.SlackChannel{ID: "C123"}
	if got := channelFor(w); got != "C123" {
		t.Errorf("got %q", got)
	}
}

// A log filename is built from the stage. A stage containing a slash would
// otherwise write outside the logs directory.
func TestSafeCannotEscapeTheLogDirectory(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"intent", "intent"},
		{"../../etc/passwd", "------etc-passwd"},
		{"a b", "a-b"},
		{"", "stage"},
		{"///", "---"},
	} {
		got := safe(tc.in)
		if got != tc.want {
			t.Errorf("safe(%q) = %q, want %q", tc.in, got, tc.want)
		}
		if strings.ContainsAny(got, `/\`) {
			t.Errorf("safe(%q) = %q still contains a path separator", tc.in, got)
		}
	}
}

func TestHumanTokens(t *testing.T) {
	for _, tc := range []struct {
		in   int
		want string
	}{{200000, "200k"}, {1000000, "1M"}, {1500000, "2M"}, {0, "0k"}} {
		if got := humanTokens(tc.in); got != tc.want {
			t.Errorf("humanTokens(%d) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// An agent can emit hundreds of megabytes; quota errors appear at the END of
// a failed run. The buffer must stay bounded and keep the tail, not the head.
func TestRingWriterKeepsTheTailAndStaysBounded(t *testing.T) {
	r := &ringWriter{max: 10}
	for i := 0; i < 100; i++ {
		if _, err := r.Write([]byte("0123456789")); err != nil {
			t.Fatal(err)
		}
	}
	got := r.String()
	if len(got) != 10 {
		t.Fatalf("buffer grew to %d bytes; it must stay bounded", len(got))
	}
	_, _ = r.Write([]byte("TAIL"))
	if !strings.HasSuffix(r.String(), "TAIL") {
		t.Errorf("got %q; the newest bytes must survive", r.String())
	}
}

func TestRingWriterIsConcurrencySafe(t *testing.T) {
	r := &ringWriter{max: 1024}
	var wg sync.WaitGroup
	for i := 0; i < 40; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, _ = r.Write([]byte("xxxxxxxxxx")) }()
	}
	wg.Wait()
	if len(r.String()) > 1024 {
		t.Error("bound exceeded under concurrent writes")
	}
}

func TestQuoteAllOnlyQuotesWhatNeedsIt(t *testing.T) {
	got := quoteAll([]string{"-p", "two words", "plain", `has"quote`})
	if got[0] != "-p" || got[2] != "plain" {
		t.Errorf("plain args were quoted: %v", got)
	}
	if !strings.HasPrefix(got[1], `"`) || !strings.HasPrefix(got[3], `"`) {
		t.Errorf("args needing quotes were left bare: %v", got)
	}
}

// Usage accounting must never lose the work: a malformed result is dropped
// silently rather than failing the run that produced it.
func TestRecordUsageIsResilientAndBooksRealRuns(t *testing.T) {
	w := ws(t, "")
	recordUsage(w, "intent", "not json at all")
	recordUsage(w, "intent", "")

	l, _ := budget.Load(workspace.Home())
	if len(l.Runs) != 0 {
		t.Fatalf("garbage was booked as a run: %+v", l.Runs)
	}

	good, _ := json.Marshal(map[string]any{
		"total_cost_usd": 0.42,
		"usage": map[string]any{
			"input_tokens": 100, "output_tokens": 50,
			"cache_read_input_tokens": 10, "cache_creation_input_tokens": 5,
		},
	})
	recordUsage(w, "plan", string(good))

	l2, _ := budget.Load(workspace.Home())
	if len(l2.Runs) != 1 {
		t.Fatalf("a real result was not booked: %+v", l2.Runs)
	}
	if l2.Runs[0].Stage != "plan" || l2.Runs[0].Workspace != "t-1" {
		t.Errorf("run = %+v; stage and workspace must be stamped", l2.Runs[0])
	}
}

func TestAgentTrackerEnvOnlyWhenTrackerIsEnabled(t *testing.T) {
	w := ws(t, `{"tracker":{"enabled":false,"project_key":"FCIA","agent_label":"orion_agent"}}`)
	if env := agentTrackerEnv(w.RepoDir()); len(env) != 0 {
		t.Errorf("a disabled tracker must publish nothing, got %v", env)
	}

	w2 := ws(t, `{"tracker":{"enabled":true,"project_key":"FCIA","agent_label":"orion_agent"},
	              "vcs":{"agent_author_name":"orion_agent"}}`)
	env := agentTrackerEnv(w2.RepoDir())
	joined := strings.Join(env, " ")
	for _, want := range []string{"ORION_TRACKER_PROJECT=FCIA", "ORION_TRACKER_LABEL=orion_agent",
		"ORION_AGENT_NAME=orion_agent"} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %s in %v", want, env)
		}
	}
}

// Credentials must not reach the agent, and an inherited git identity must
// not decide authorship instead of the config.
func TestChildEnvDropsSecretsAndInheritedAuthorship(t *testing.T) {
	w := ws(t, "")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "shh")
	t.Setenv("GITHUB_TOKEN", "ghp_shh")
	t.Setenv("GIT_AUTHOR_NAME", "someone-else")
	t.Setenv("HARMLESS_VAR", "keep-me")

	env := childEnv(w, &agentcfg.Run{}, events.ActorImplementer)
	joined := strings.Join(env, "\n")
	for _, banned := range []string{"AWS_SECRET_ACCESS_KEY=", "GITHUB_TOKEN=", "GIT_AUTHOR_NAME=someone-else"} {
		if strings.Contains(joined, banned) {
			t.Errorf("%s reached the agent", banned)
		}
	}
	if !strings.Contains(joined, "HARMLESS_VAR=keep-me") {
		t.Error("an ordinary variable was dropped")
	}
	if !strings.Contains(joined, "ORION_WORKSPACE=t-1") {
		t.Error("the workspace id was not published")
	}
}

func TestStagePromptCoversEveryKnownStage(t *testing.T) {
	w := ws(t, "")
	for _, stage := range []string{"intent", "spec", "design", "plan", "scaffold"} {
		got, err := stagePrompt(w, stage, config.Toolkit{})
		if err != nil {
			t.Errorf("%s: %v", stage, err)
			continue
		}
		if strings.TrimSpace(got) == "" {
			t.Errorf("%s produced an empty prompt", stage)
		}
	}
	if _, err := stagePrompt(w, "no-such-stage", config.Toolkit{}); err == nil {
		t.Error("an unknown stage must be an error, not an empty prompt")
	}
}

// The session id and the closing message are what make an advisor loop
// possible: the message IS the question, and the id is what lets the
// conversation continue instead of starting over and paying for the whole
// context a second time.
func TestSessionAndFinalAreRecoveredFromTheResult(t *testing.T) {
	body := `{"is_error":false,"num_turns":3,"session_id":"abc-123",` +
		`"result":"I need to know whether segments are keyed by MCC or issuer.",` +
		`"total_cost_usd":0.4}`
	sid, final := sessionAndFinal(body)
	if sid != "abc-123" {
		t.Errorf("session = %q", sid)
	}
	if !strings.Contains(final, "MCC") {
		t.Errorf("final = %q", final)
	}
}

// The tail buffer is bounded, so it can begin part-way through the stream.
func TestSessionAndFinalSurvivesATruncatedTail(t *testing.T) {
	truncated := `ns":6}},"service_tier":"standard"}` + "\n" +
		`{"is_error":false,"session_id":"xyz-789","result":"done"}`
	sid, final := sessionAndFinal(truncated)
	if sid != "xyz-789" || final != "done" {
		t.Errorf("sid = %q, final = %q", sid, final)
	}
}

// Losing them must degrade to a fresh run, never to a wrong one.
func TestSessionAndFinalOnGarbage(t *testing.T) {
	for _, in := range []string{"", "not json at all", "<html>error</html>"} {
		sid, final := sessionAndFinal(in)
		if sid != "" || final != "" {
			t.Errorf("sessionAndFinal(%q) invented %q / %q", in, sid, final)
		}
	}
}

// OR-127: a `claude` that exits 0 without ever printing a "result" line
// (killed externally after a partial flush, a crash mid-stream) must be
// reported as a failed run, not read as a quiet success with nothing to
// show for it.
func TestRunFailsWhenClaudeExitsWithoutEmittingAResult(t *testing.T) {
	w := ws(t, "")

	dir := t.TempDir()
	bin := filepath.Join(dir, "claude")
	// A well-formed init line, an assistant line, then exit 0 -- no result.
	script := "#!/bin/sh\n" +
		`echo '{"type":"system","subtype":"init","model":"claude-opus-5"}'` + "\n" +
		`echo '{"type":"assistant","message":{"model":"claude-opus-5","content":[{"type":"text","text":"working on it"}]}}'` + "\n" +
		"exit 0\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	res, err := Run(w, Options{Stage: "intent", Prompt: "do a thing",
		MaxMinutes: 1, MaxTurns: 1})
	if err == nil {
		t.Fatal("a process that never emitted a result must be reported as a failure")
	}
	if res == nil || res.ExitCode == 0 {
		t.Fatalf("result must carry a non-zero exit code, got %+v", res)
	}
	if !strings.Contains(res.Reason, "without ever emitting a stream result") {
		t.Errorf("reason should explain the missing result, got: %q", res.Reason)
	}
}

// The ordinary case must still read as success: a result line present.
func TestRunSucceedsWhenClaudeEmitsAResult(t *testing.T) {
	w := ws(t, "")

	dir := t.TempDir()
	bin := filepath.Join(dir, "claude")
	script := "#!/bin/sh\n" +
		`echo '{"type":"system","subtype":"init","model":"claude-opus-5"}'` + "\n" +
		`echo '{"type":"result","session_id":"abc","result":"done","total_cost_usd":0.1,"is_error":false}'` + "\n" +
		"exit 0\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	res, err := Run(w, Options{Stage: "intent", Prompt: "do a thing",
		MaxMinutes: 1, MaxTurns: 1})
	if err != nil {
		t.Fatalf("a run that emitted a result must succeed, got: %v (res=%+v)", err, res)
	}
	if res.ExitCode != 0 {
		t.Errorf("exit code = %d, want 0", res.ExitCode)
	}
}

// fakeClaudeRecordingArgs writes a `claude` stub onto PATH that dumps its
// own argv to argsFile before emitting a normal result line, so a test can
// assert on exactly what Orion invoked it with (OR-131).
func fakeClaudeRecordingArgs(t *testing.T) (argsFile string) {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "claude")
	argsFile = filepath.Join(dir, "args.txt")
	script := "#!/bin/sh\n" +
		`echo "$@" > ` + argsFile + "\n" +
		`echo '{"type":"result","session_id":"abc","result":"done","total_cost_usd":0.1,"is_error":false}'` + "\n" +
		"exit 0\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return argsFile
}

func TestEffortIsPassedToClaudeWhenSet(t *testing.T) {
	w := ws(t, "")
	argsFile := fakeClaudeRecordingArgs(t)

	if _, err := Run(w, Options{Stage: "intent", Prompt: "do a thing",
		MaxMinutes: 1, MaxTurns: 1, Effort: "high"}); err != nil {
		t.Fatal(err)
	}

	got, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), "--effort high") {
		t.Errorf("claude invocation missing --effort high, got: %q", got)
	}
}

func TestNoEffortFlagWhenOptionsLeavesItEmpty(t *testing.T) {
	w := ws(t, "")
	argsFile := fakeClaudeRecordingArgs(t)

	if _, err := Run(w, Options{Stage: "intent", Prompt: "do a thing",
		MaxMinutes: 1, MaxTurns: 1}); err != nil {
		t.Fatal(err)
	}

	got, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(got), "--effort") {
		t.Errorf("an unset Effort must not add --effort at all, got: %q", got)
	}
}
