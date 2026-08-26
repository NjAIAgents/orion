package budget

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Transcript accounting reads Claude Code's own session logs.
//
// This is the honest source for "how much have I used this week", and it is
// strictly better than counting only Orion's supervised runs: the JSONL files
// under ~/.claude/projects cover EVERY session — interactive work, other
// projects, subagents — not just the slice Orion drove. A budget that ignored
// the interactive session you spent all morning in would be measuring the
// wrong thing.
//
// It still is not the provider's weekly allowance. Nothing reports that. What
// this gives is a complete local record of consumption, compared against a
// limit you set.

// TokenBreakdown separates the four counts because they are priced very
// differently, and lumping them together makes an hour of cache reads look
// like an hour of fresh input.
type TokenBreakdown struct {
	Input         int
	CacheCreation int
	CacheRead     int
	Output        int
}

// Total is the raw sum. The three input counts are DISJOINT in the Anthropic
// API: input_tokens excludes anything served from or written to cache. A
// transcript line reading input_tokens=2 beside cache_creation=67399 is the
// proof; treating input as a superset would undercount that turn by 97k.
func (t TokenBreakdown) Total() int {
	return t.Input + t.CacheCreation + t.CacheRead + t.Output
}

// Effective weights each class by roughly what it costs relative to a plain
// input token: cache reads are much cheaper, cache writes dearer, output
// dearest. Raw totals overstate a cache-heavy session and understate a
// generation-heavy one, so a budget built on raw counts binds at the wrong
// moment.
func (t TokenBreakdown) Effective() int {
	return int(float64(t.Input) +
		float64(t.CacheRead)*0.1 +
		float64(t.CacheCreation)*2.0 +
		float64(t.Output)*5.0)
}

// TranscriptUsage is what a scan found.
type TranscriptUsage struct {
	Tokens    TokenBreakdown
	Turns     int
	Sessions  int
	Sidechain int // turns attributable to subagents
	Since     time.Time
	Scanned   int // files read
	Skipped   int // files unreadable, reported rather than hidden
}

// TranscriptDir is where Claude Code keeps session logs.
func TranscriptDir() string {
	if h, err := os.UserHomeDir(); err == nil {
		return filepath.Join(h, ".claude", "projects")
	}
	return ""
}

// ScanTranscripts totals usage over the window.
//
// Files are filtered by modification time before being opened. There can be
// well over a thousand transcripts; parsing them all on every check would
// make the budget gate slower than the work it guards.
func ScanTranscripts(dir string, since time.Time) (TranscriptUsage, error) {
	u := TranscriptUsage{Since: since}
	if dir == "" {
		return u, nil
	}
	sessions := map[string]bool{}

	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			// A single unreadable directory must not abort the scan; a
			// partial total that says it is partial beats no total.
			return nil
		}
		if d.IsDir() || !strings.HasSuffix(path, ".jsonl") {
			return nil
		}
		info, statErr := d.Info()
		if statErr != nil || info.ModTime().Before(since) {
			return nil
		}
		f, openErr := os.Open(path)
		if openErr != nil {
			u.Skipped++
			return nil
		}
		defer f.Close()
		u.Scanned++

		sc := bufio.NewScanner(f)
		// Transcript lines carry whole tool results and can be very large;
		// the default 64k limit would silently truncate them.
		sc.Buffer(make([]byte, 0, 1<<20), 16<<20)
		for sc.Scan() {
			line := sc.Bytes()
			if !strings.Contains(string(line), `"usage"`) {
				continue
			}
			var rec struct {
				Timestamp   string `json:"timestamp"`
				SessionID   string `json:"sessionId"`
				IsSidechain bool   `json:"isSidechain"`
				Message     struct {
					Model string `json:"model"`
					Usage struct {
						Input         int `json:"input_tokens"`
						CacheCreation int `json:"cache_creation_input_tokens"`
						CacheRead     int `json:"cache_read_input_tokens"`
						Output        int `json:"output_tokens"`
					} `json:"usage"`
				} `json:"message"`
			}
			if json.Unmarshal(line, &rec) != nil {
				continue
			}
			// Filter per turn, not per file: a long-running session's file is
			// recent while most of its turns may predate the window.
			ts, tsErr := time.Parse(time.RFC3339, rec.Timestamp)
			if tsErr != nil || ts.Before(since) {
				continue
			}
			us := rec.Message.Usage
			if us.Input == 0 && us.CacheCreation == 0 && us.CacheRead == 0 && us.Output == 0 {
				continue
			}
			u.Tokens.Input += us.Input
			u.Tokens.CacheCreation += us.CacheCreation
			u.Tokens.CacheRead += us.CacheRead
			u.Tokens.Output += us.Output
			u.Turns++
			if rec.IsSidechain {
				u.Sidechain++
			}
			if rec.SessionID != "" {
				sessions[rec.SessionID] = true
			}
		}
		// A scanner error mid-file leaves what was counted so far, which is
		// correct: partial data beats discarding a whole session's usage.
		if sc.Err() != nil {
			u.Skipped++
		}
		return nil
	})

	u.Sessions = len(sessions)
	return u, err
}
