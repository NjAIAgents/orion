package supervisor

import (
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
