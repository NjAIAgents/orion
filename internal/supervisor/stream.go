package supervisor

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"sync"
)

// Streaming the agent's activity.
//
// With --output-format json the CLI prints ONE object, at exit. Everything
// the agent did in between -- every file read, every edit, every test run --
// arrives after it is over, which makes a long run indistinguishable from a
// hung one. The event log showed "implementing FCIA-6" and then nothing for
// as long as the work took.
//
// --output-format stream-json emits NDJSON as it goes, so the same run can be
// narrated live. The cost is that stdout becomes a firehose of JSON, which is
// why the raw stream now goes only to the log file and the terminal gets the
// humanised lines built here.

// Activity is one observable thing the agent did, already reduced to
// something worth a line in a log a person reads.
type Activity struct {
	Kind   string // "tool", "text", "start"
	Tool   string // Read, Edit, Bash, ...
	Detail string // the argument that identifies WHICH read, edit or command
	// Model that produced this. Taken from the message itself rather than
	// from what Orion asked for: --model is a request, and a fallback after
	// a capacity error is exactly the case where knowing what actually ran
	// matters most.
	Model string
}

// activityWriter turns the NDJSON stream into Activity callbacks.
//
// An io.Writer rather than a scanner over a pipe so it can sit in the same
// MultiWriter as the log file and the quota ring buffer, and so a malformed
// line degrades to silence instead of killing the run: observability must
// never be able to fail the thing it observes.
type activityWriter struct {
	mu  sync.Mutex
	buf []byte
	on  func(Activity)
	// base is the worktree the agent runs in, so paths render relative to
	// IT rather than to Orion's own cwd -- which is wherever the user
	// happened to be standing, often outside the repository entirely.
	base string
	seen map[string]bool // tool_use ids already reported
	// model last seen on the stream, carried forward across frames that do
	// not name one.
	model string
	// limit is the last plan-limit verdict the run reported.
	limit RateLimit
}

// Limit returns the plan limit last reported by this run.
func (w *activityWriter) Limit() RateLimit {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.limit
}

// maxLine bounds the accumulator. A single stream-json line carries whole
// file contents on a Write, so the ceiling is generous -- but unbounded
// growth on a stream that never emits a newline would be a memory leak in a
// process that already runs for an hour.
const maxLine = 4 << 20 // 4MiB

func newActivityWriter(base string, on func(Activity)) *activityWriter {
	return &activityWriter{on: on, base: base, seen: map[string]bool{}}
}

func (w *activityWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	w.buf = append(w.buf, p...)
	for {
		i := indexByte(w.buf, '\n')
		if i < 0 {
			break
		}
		line := w.buf[:i]
		w.buf = w.buf[i+1:]
		w.emit(line)
	}
	if len(w.buf) > maxLine {
		// Drop it. A line this long is not one this code can use, and
		// keeping it only starves the process that is doing the real work.
		w.buf = w.buf[:0]
	}
	return len(p), nil
}

func indexByte(b []byte, c byte) int {
	for i, x := range b {
		if x == c {
			return i
		}
	}
	return -1
}

// emit parses one NDJSON line and reports what it describes.
//
// Silent on anything it does not recognise. The stream carries message
// shapes this code has no opinion about, and a future CLI version will carry
// more; treating an unknown type as an error would make Orion break on an
// upgrade that changed nothing it depends on.
func (w *activityWriter) emit(line []byte) {
	if w.on == nil || len(strings.TrimSpace(string(line))) == 0 {
		return
	}
	var m struct {
		Type    string `json:"type"`
		Subtype string `json:"subtype"`
		Model   string `json:"model"` // system/init carries it at the top level
		Message struct {
			Model   string `json:"model"` // every assistant message carries it
			Content []struct {
				Type  string          `json:"type"`
				Text  string          `json:"text"`
				ID    string          `json:"id"`
				Name  string          `json:"name"`
				Input json.RawMessage `json:"input"`
			} `json:"content"`
		} `json:"message"`
	}
	if json.Unmarshal(line, &m) != nil {
		return
	}

	// The plan's own limit. Parsed before the type switch because it is a
	// property of the ACCOUNT rather than of anything the agent did, and it
	// is the one field that decides whether more work may be started at all.
	if rl, ok := parseRateLimit(line); ok {
		w.limit = rl
		if !rl.OK() {
			w.on(Activity{Kind: "limit", Detail: rl.Describe(timeNow())})
		}
		return
	}

	switch m.Type {
	case "system":
		if m.Subtype == "init" {
			w.model = m.Model
			w.on(Activity{Kind: "start", Detail: "session open", Model: w.model})
		}
	case "assistant":
		// Remember it: tool_result frames and the closing summary carry no
		// model of their own, and an event attributed to nothing is worse
		// than one attributed to the last model known to be running.
		if m.Message.Model != "" {
			w.model = m.Message.Model
		}
		for _, c := range m.Message.Content {
			switch c.Type {
			case "tool_use":
				// The CLI can repeat a block across partial messages; report
				// each tool call once so the log counts actions, not frames.
				if c.ID != "" {
					if w.seen[c.ID] {
						continue
					}
					w.seen[c.ID] = true
				}
				w.on(Activity{Kind: "tool", Tool: c.Name, Model: w.model,
					Detail: toolDetail(w.base, c.Name, c.Input)})
			case "text":
				if t := firstSentence(c.Text); t != "" {
					w.on(Activity{Kind: "text", Detail: t, Model: w.model})
				}
			}
		}
	}
}

// toolDetail reduces a tool's input to the part that identifies which call
// this was. "Edit" is noise; "Edit internal/work/work.go" is a trace.
func toolDetail(base, name string, raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var in map[string]any
	if json.Unmarshal(raw, &in) != nil {
		return ""
	}
	str := func(k string) string {
		s, _ := in[k].(string)
		return strings.TrimSpace(s)
	}
	switch name {
	case "Read", "Edit", "Write", "NotebookEdit":
		return short(base, str("file_path"))
	case "Bash":
		return clip(oneLine(str("command")), 80)
	case "Grep":
		if p := str("path"); p != "" {
			return clip(str("pattern"), 40) + " in " + short(base, p)
		}
		return clip(str("pattern"), 60)
	case "Glob":
		return clip(str("pattern"), 60)
	case "Task", "Agent":
		return clip(str("description"), 60)
	case "WebFetch":
		return clip(str("url"), 60)
	case "TodoWrite":
		// The list itself is long and changes every turn. Its length is the
		// only part that tells a watcher anything.
		if t, ok := in["todos"].([]any); ok {
			return plural(len(t), "item")
		}
	}
	// Unknown tool: the first short string field is usually the subject.
	for _, k := range []string{"file_path", "path", "query", "prompt", "command"} {
		if v := str(k); v != "" {
			return clip(oneLine(v), 60)
		}
	}
	return ""
}

// short renders a path relative to the working tree when it is inside it, so
// a line reads "internal/work/work.go" and not an absolute path that pushes
// the useful part off the edge of a terminal.
func short(base, p string) string {
	if p == "" {
		return ""
	}
	// Resolve both sides. On macOS the worktree lives under /tmp while the
	// agent reports /private/tmp, and an unresolved comparison makes every
	// path in the repository look like it is outside it.
	if base != "" {
		b, err1 := filepath.EvalSymlinks(base)
		q, err2 := filepath.EvalSymlinks(p)
		if err1 != nil {
			b = base
		}
		if err2 != nil {
			q = p
		}
		if rel, err := filepath.Rel(b, q); err == nil && !strings.HasPrefix(rel, "..") {
			return rel
		}
	}
	return filepath.Base(p)
}

// firstSentence takes the opening line of the agent's prose.
//
// The full text is in the log. What belongs in an event stream is enough to
// tell whether the agent is doing the right thing, and a paragraph per turn
// buries the tool calls that carry the actual trace.
func firstSentence(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if i := strings.IndexByte(s, '\n'); i > 0 {
		s = s[:i]
	}
	return clip(strings.TrimSpace(s), 110)
}

func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return itoa(n) + " " + word + "s"
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
