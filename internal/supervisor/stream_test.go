package supervisor

import (
	"fmt"
	"strings"
	"testing"
)

// The fixtures below are the shapes the real CLI emits, captured from a live
// `claude -p --output-format stream-json --verbose` run rather than written
// from memory. That distinction matters: this parser is the only thing
// standing between the event log and silence, and a test built on an imagined
// schema would pass while the log stayed empty.

const initLine = `{"type":"system","subtype":"init","session_id":"s1","model":"claude-opus-4-1-20250805","tools":["Read"]}`

func toolLine(id, name, input string) string {
	return `{"type":"assistant","session_id":"s1","message":{"model":"claude-opus-4-1-20250805","content":[{"type":"tool_use","id":"` +
		id + `","name":"` + name + `","input":` + input + `}]}}`
}

func collect(t *testing.T, base string, lines ...string) []Activity {
	t.Helper()
	var got []Activity
	w := newActivityWriter(base, func(a Activity) { got = append(got, a) })
	for _, l := range lines {
		if _, err := w.Write([]byte(l + "\n")); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	return got
}

func TestToolCallsAreReportedWithTheirSubject(t *testing.T) {
	got := collect(t, "/repo",
		initLine,
		toolLine("t1", "Read", `{"file_path":"/repo/src/impact.py"}`),
		toolLine("t2", "Bash", `{"command":"pytest -q\n  tests/"}`),
		toolLine("t3", "Grep", `{"pattern":"def calc","path":"/repo/src"}`),
	)

	if len(got) != 4 {
		t.Fatalf("want 4 activities (1 start + 3 tools), got %d: %+v", len(got), got)
	}
	if got[0].Kind != "start" || got[0].Model == "" {
		t.Errorf("init should report the session and its model, got %+v", got[0])
	}

	// The detail is the whole point: "Read" tells a watcher nothing, and a
	// log of bare tool names cannot answer "is it working on the right file".
	want := []string{"src/impact.py", "pytest -q tests/", "def calc in src"}
	for i, w := range want {
		if got[i+1].Detail != w {
			t.Errorf("activity %d detail = %q, want %q", i+1, got[i+1].Detail, w)
		}
	}
}

// Model attribution is the reason a reader can tell a careful decision from a
// cheap one. It must come from the message rather than from what Orion asked
// for: --model is a request, and a fallback after a capacity error is exactly
// when the difference matters.
func TestEveryActivityCarriesTheModelThatProducedIt(t *testing.T) {
	got := collect(t, "/repo", initLine, toolLine("t1", "Read", `{"file_path":"/repo/a.py"}`))
	for _, a := range got {
		if !strings.Contains(a.Model, "opus") {
			t.Fatalf("activity %+v lost its model", a)
		}
	}
}

// The CLI repeats a tool_use block across partial message frames. Counting
// each frame would make one edit look like several, and a log that inflates
// what happened is worse than one that says less.
func TestARepeatedToolBlockIsReportedOnce(t *testing.T) {
	line := toolLine("dup", "Edit", `{"file_path":"/repo/a.py"}`)
	got := collect(t, "/repo", line, line, line)
	if len(got) != 1 {
		t.Fatalf("want 1 activity for a repeated block, got %d", len(got))
	}
}

// A line arrives in whatever chunks the pipe delivers. Splitting on newlines
// only when a whole line has accumulated is what keeps a large Write -- which
// carries an entire file's contents -- from being parsed as fragments.
func TestOutputSplitAcrossWritesIsStillParsed(t *testing.T) {
	line := toolLine("t1", "Write", `{"file_path":"/repo/new.py"}`) + "\n"
	var got []Activity
	w := newActivityWriter("/repo", func(a Activity) { got = append(got, a) })
	for i := 0; i < len(line); i += 7 {
		end := i + 7
		if end > len(line) {
			end = len(line)
		}
		if _, err := w.Write([]byte(line[i:end])); err != nil {
			t.Fatal(err)
		}
	}
	if len(got) != 1 || got[0].Detail != "new.py" {
		t.Fatalf("chunked write lost the activity: %+v", got)
	}
}

// Unknown and malformed lines must be silent, not fatal. The stream carries
// message types this code has no opinion about, and more will be added; an
// upgrade that changes nothing Orion depends on must not break the run it is
// only observing.
func TestUnrecognisedLinesAreIgnored(t *testing.T) {
	got := collect(t, "/repo",
		`{"type":"system","subtype":"hook_started","hook_name":"SessionStart"}`,
		`{"type":"rate_limit_event"}`,
		`{"type":"user","message":{"content":[{"type":"tool_result","content":"ok"}]}}`,
		`not json at all`,
		``,
	)
	if len(got) != 0 {
		t.Fatalf("expected silence, got %+v", got)
	}
}

// Paths are reported absolute and the worktree lives under a symlinked /tmp
// on macOS, so an unresolved comparison makes every file in the repository
// look like it is outside it.
func TestPathsRenderRelativeToTheWorktree(t *testing.T) {
	if got := short("/repo", "/repo/internal/work/work.go"); got != "internal/work/work.go" {
		t.Errorf("short() = %q, want a repo-relative path", got)
	}
	// Outside the tree: fall back to the base name rather than printing an
	// absolute path that pushes the useful part off the edge of a terminal.
	if got := short("/repo", "/etc/hosts"); got != "hosts" {
		t.Errorf("short() = %q, want %q", got, "hosts")
	}
}

// Context occupancy: a peak over turns, never a sum.
//
// The bug being locked out: Orion printed "context reached 656% of the 1M
// window on this stage" during a real FCIA-7 run. That figure came from
// dividing the session's CUMULATIVE prompt tokens by the window. cache_read
// re-counts the whole cached prefix every turn, so the numerator grows
// without bound while the actual context sits still. The warning could
// therefore never be right: over 100% on any long run, silent on a genuinely
// full one.
//
// These fixtures use per-turn usage exactly as the stream carries it.
func usageLine(input, cacheRead, cacheCreate, output, window int) string {
	return fmt.Sprintf(`{"type":"assistant","session_id":"s1","message":{`+
		`"model":"claude-opus-4-1-20250805","context_window":%d,`+
		`"usage":{"input_tokens":%d,"cache_read_input_tokens":%d,`+
		`"cache_creation_input_tokens":%d,"output_tokens":%d},`+
		`"content":[{"type":"text","text":"working"}]}}`,
		window, input, cacheRead, cacheCreate, output)
}

func TestContextIsMeasuredAsAPeakNotASum(t *testing.T) {
	w := newActivityWriter("/repo", func(Activity) {})
	// Forty turns, each re-reading a 150k cached prefix. Summing these gives
	// ~6M against a 1M window -- the 656% that shipped.
	for i := 0; i < 40; i++ {
		if _, err := w.Write([]byte(usageLine(2_000, 150_000, 0, 1_000, 1_000_000) + "\n")); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	peak, window := w.Context()
	if window != 1_000_000 {
		t.Fatalf("window = %d, want 1000000", window)
	}
	if peak != 153_000 {
		t.Fatalf("peak = %d, want 153000 (one turn's prompt, not forty)", peak)
	}
	// The number that matters to a person: this run was nowhere near full.
	if pct := peak * 100 / window; pct >= 70 {
		t.Errorf("a 15%%-full context reported %d%%; that warning would be false", pct)
	}
}

func TestTheLargestTurnWins(t *testing.T) {
	w := newActivityWriter("/repo", func(Activity) {})
	for _, l := range []string{
		usageLine(1_000, 10_000, 0, 500, 200_000),
		usageLine(1_000, 180_000, 5_000, 2_000, 200_000), // the peak
		usageLine(1_000, 20_000, 0, 500, 200_000),        // a later, smaller turn
	} {
		w.Write([]byte(l + "\n"))
	}
	peak, window := w.Context()
	if peak != 188_000 {
		t.Errorf("peak = %d, want 188000", peak)
	}
	// 94% of the window IS worth warning about, and this is the case the
	// old calculation would have buried among false positives.
	if pct := peak * 100 / window; pct < 70 {
		t.Errorf("a nearly-full context reported only %d%%", pct)
	}
}

// A run whose stream never reports usage must produce no percentage at all.
// Inventing a denominator is how the previous version justified printing a
// number it could not support.
func TestNoUsageMeansNoClaim(t *testing.T) {
	got := collect(t, "/repo", initLine, toolLine("t1", "Read", `{"file_path":"/repo/a.py"}`))
	if len(got) == 0 {
		t.Fatal("the fixture should still produce activities")
	}
	w := newActivityWriter("/repo", func(Activity) {})
	w.Write([]byte(initLine + "\n"))
	if peak, window := w.Context(); peak != 0 || window != 0 {
		t.Errorf("peak=%d window=%d; both must stay zero when nothing was reported", peak, window)
	}
}

// What a run was GIVEN is read off the CLI's own init frame, never inferred
// from the flags Orion passed. The flags are a request; this line is the
// answer, and it is the only honest way to check that curating the config
// directory did what it claims (OR-213).
func TestTheInitFrameReportsTheToolsetTheRunWasGiven(t *testing.T) {
	got := collect(t, "/repo",
		`{"type":"system","subtype":"init","session_id":"s1","model":"opus",`+
			`"tools":["Read","Edit","Bash"],`+
			`"mcp_servers":[{"name":"atlassian","status":"connected"}]}`)

	if len(got) != 1 || got[0].Kind != "start" {
		t.Fatalf("want one start activity, got %+v", got)
	}
	if got[0].Tools != 3 {
		t.Errorf("Tools = %d, want 3", got[0].Tools)
	}
	if len(got[0].MCPServers) != 1 || got[0].MCPServers[0] != "atlassian" {
		t.Errorf("MCPServers = %v, want [atlassian]", got[0].MCPServers)
	}
}

// A CLI that does not report either must leave both empty rather than have
// its silence recorded as "no MCP servers": the whole point of measuring is
// that nothing is asserted.
func TestAnInitFrameWithoutAToolsetClaimsNothing(t *testing.T) {
	got := collect(t, "/repo", initLine)

	if len(got) != 1 {
		t.Fatalf("want one start activity, got %+v", got)
	}
	if got[0].MCPServers != nil {
		t.Errorf("MCPServers = %v, want nil when the frame says nothing", got[0].MCPServers)
	}
}
