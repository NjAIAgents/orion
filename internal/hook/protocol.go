// Package hook implements the Claude Code hook protocol.
//
// Contract: the harness writes a JSON object to stdin and reads the
// process exit code.
//
//	exit 0  allow, stdout is shown to the user in transcript mode
//	exit 2  BLOCK, stderr is fed back to the model as the reason
//	other   non-blocking error, stderr shown to the user, action proceeds
//
// The distinction matters. A hook that crashes with exit 1 does not stop
// anything, so every guardrail here is written to fail closed on the
// decisions that matter and to exit 2 with a message a stranger can act
// on. Unparseable input is the one exception: it exits 0, because a
// malformed payload from a future harness version must not brick the
// user's session. That tradeoff is deliberate and is called out in the
// README under "Known gaps".
package hook

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
)

// Input is the payload the harness supplies. Fields absent for a given
// event stay zero; never assume ToolInput is populated.
type Input struct {
	SessionID      string          `json:"session_id"`
	TranscriptPath string          `json:"transcript_path"`
	CWD            string          `json:"cwd"`
	HookEventName  string          `json:"hook_event_name"`
	ToolName       string          `json:"tool_name"`
	ToolInput      json.RawMessage `json:"tool_input"`
	ToolResponse   json.RawMessage `json:"tool_response"`
}

// Exit codes, named so call sites read as decisions rather than numbers.
const (
	ExitAllow = 0
	ExitBlock = 2
	ExitWarn  = 1
)

// Read parses hook input from r. A read or parse failure returns an Input
// with only what could be recovered plus ok=false.
func Read(r io.Reader) (Input, bool) {
	var in Input
	b, err := io.ReadAll(r)
	if err != nil || len(b) == 0 {
		return in, false
	}
	if err := json.Unmarshal(b, &in); err != nil {
		return in, false
	}
	return in, true
}

// Command extracts tool_input.command for Bash-family tools.
func (in Input) Command() string {
	var ti struct {
		Command string `json:"command"`
	}
	if len(in.ToolInput) == 0 {
		return ""
	}
	_ = json.Unmarshal(in.ToolInput, &ti)
	return ti.Command
}

// FilePath extracts tool_input.file_path for Edit/Write-family tools,
// falling back to path and notebook_path used by some tools.
func (in Input) FilePath() string {
	var ti struct {
		FilePath     string `json:"file_path"`
		Path         string `json:"path"`
		NotebookPath string `json:"notebook_path"`
	}
	if len(in.ToolInput) == 0 {
		return ""
	}
	_ = json.Unmarshal(in.ToolInput, &ti)
	switch {
	case ti.FilePath != "":
		return ti.FilePath
	case ti.Path != "":
		return ti.Path
	default:
		return ti.NotebookPath
	}
}

// Background reports whether a Bash call was launched in the background,
// either by the harness flag or by a trailing `&`. A backgrounded command is
// one the agent will have to wait for, and waiting is the case the loop
// breaker used to be unable to tell from looping (OR-207).
func (in Input) Background() bool {
	var ti struct {
		RunInBackground bool `json:"run_in_background"`
	}
	if len(in.ToolInput) > 0 {
		_ = json.Unmarshal(in.ToolInput, &ti)
	}
	if ti.RunInBackground {
		return true
	}
	// A trailing `&&` is a conjunction, not a backgrounding: the command
	// after it runs in the foreground and the agent waits for both.
	c := strings.TrimSpace(in.Command())
	return strings.HasSuffix(c, "&") && !strings.HasSuffix(c, "&&")
}

// Failed reports whether a PostToolUse response indicates failure.
// Tool responses are not uniformly shaped, so this checks the several
// signals that appear in practice and errs toward "not a failure" so a
// successful run is never counted against the failure budget.
func (in Input) Failed() bool {
	if len(in.ToolResponse) == 0 {
		return false
	}
	var tr struct {
		Success     *bool  `json:"success"`
		IsError     *bool  `json:"is_error"`
		ExitCode    *int   `json:"exit_code"`
		Error       string `json:"error"`
		Stderr      string `json:"stderr"`
		Interrupted *bool  `json:"interrupted"`
	}
	if err := json.Unmarshal(in.ToolResponse, &tr); err != nil {
		// Some tools return a bare string or array. Not a failure signal.
		return false
	}
	if tr.Success != nil && !*tr.Success {
		return true
	}
	if tr.IsError != nil && *tr.IsError {
		return true
	}
	if tr.ExitCode != nil && *tr.ExitCode != 0 {
		return true
	}
	if tr.Error != "" {
		return true
	}
	return false
}

// Signature is a stable fingerprint of tool name plus normalized input,
// used to detect a session repeating the identical call. Whitespace is
// collapsed so trivially reformatted retries still collide.
func (in Input) Signature() string {
	norm := strings.Join(strings.Fields(string(in.ToolInput)), " ")
	h := fnv1a(in.ToolName + "\x00" + norm)
	return fmt.Sprintf("%016x", h)
}

// fnv1a is a 64-bit FNV-1a. Non-cryptographic on purpose: this is a
// bucket key for loop detection, not a security boundary.
func fnv1a(s string) uint64 {
	const (
		offset64 = 14695981039346656037
		prime64  = 1099511628211
	)
	var h uint64 = offset64
	for i := 0; i < len(s); i++ {
		h ^= uint64(s[i])
		h *= prime64
	}
	return h
}

// Decision is a hook's verdict. Hooks return one rather than calling
// os.Exit themselves, so the decision logic is a pure function of its
// inputs and can be tested without spawning a process. Emit is the only
// place that touches the process.
type Decision struct {
	Code int
	// Msg goes to stderr for Block and Warn, stdout for Allow. For a block
	// this string is the entire explanation the model receives, so it must
	// say what tripped, why, and what to do instead.
	Msg string
	// Notes are non-blocking observations printed alongside any verdict,
	// used for warnings that must not change the outcome (degraded config,
	// budget approaching).
	Notes []string
}

// Block halts the tool call. The model reads Msg and decides its next
// move from it alone, which is why every block message names a route
// forward rather than only stating a refusal.
func Block(format string, args ...any) Decision {
	return Decision{Code: ExitBlock, Msg: fmt.Sprintf(format, args...)}
}

// Allow permits the call, optionally with a transcript note.
func Allow(note string) Decision {
	return Decision{Code: ExitAllow, Msg: note}
}

// Warn reports a problem without blocking. Used when Orion itself is
// misconfigured: refusing every tool call because state is unwritable
// would be a worse failure than proceeding unguarded and saying so.
func Warn(format string, args ...any) Decision {
	return Decision{Code: ExitWarn, Msg: fmt.Sprintf(format, args...)}
}

// WithNote attaches a non-blocking observation.
func (d Decision) WithNote(format string, args ...any) Decision {
	d.Notes = append(d.Notes, fmt.Sprintf(format, args...))
	return d
}

// Blocked reports whether this decision halts the tool call.
func (d Decision) Blocked() bool { return d.Code == ExitBlock }

// Emit writes the decision and exits. The only impure part of the path.
func Emit(d Decision) {
	for _, n := range d.Notes {
		fmt.Fprintln(os.Stdout, "orion: "+n)
	}
	if d.Msg != "" {
		if d.Code == ExitAllow {
			fmt.Fprintln(os.Stdout, "orion: "+d.Msg)
		} else {
			fmt.Fprintln(os.Stderr, "orion: "+d.Msg)
		}
	}
	os.Exit(d.Code)
}
