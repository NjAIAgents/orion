package collect

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// Where a merge request lives between polls.
//
// The approval is read from ONE message, identified by its Slack timestamp,
// and that timestamp has to outlive the process that posted it. Without it a
// later poll would have to scan the channel and guess which message it was
// answering -- and guessing wrong means reading an approval meant for a
// different ticket.
//
// A file rather than the ticket, because a Jira comment is prose that a
// person may edit or delete, and this is machine state. It sits beside the
// event log in the workspace, so it is scoped to the project it belongs to
// and disappears with it.

// Request is one outstanding merge request.
type Request struct {
	Key       string    `json:"key"`
	Channel   string    `json:"channel"`
	TS        string    `json:"ts"`
	PR        string    `json:"pr"`
	AskedAt   time.Time `json:"asked_at"`
	Commit    string    `json:"commit"`
	Approvers []string  `json:"approvers"` // the allowlist as it stood when asked
}

type requestFile struct {
	Version  int                `json:"version"`
	Requests map[string]Request `json:"requests"`
}

func requestPath(wsDir string) string {
	return filepath.Join(wsDir, ".orion", "merge-requests.json")
}

func loadRequests(wsDir string) requestFile {
	f := requestFile{Version: 1, Requests: map[string]Request{}}
	b, err := os.ReadFile(requestPath(wsDir))
	if err != nil {
		return f
	}
	// A corrupt file is treated as empty rather than fatal. The cost is one
	// duplicate merge request in Slack; the alternative is a collector that
	// refuses to run at all because of a file it can rewrite.
	if json.Unmarshal(b, &f) != nil || f.Requests == nil {
		return requestFile{Version: 1, Requests: map[string]Request{}}
	}
	return f
}

func saveRequest(wsDir string, r Request) error {
	f := loadRequests(wsDir)
	f.Requests[r.Key] = r
	return writeRequests(wsDir, f)
}

func clearRequest(wsDir, key string) error {
	f := loadRequests(wsDir)
	if _, ok := f.Requests[key]; !ok {
		return nil
	}
	delete(f.Requests, key)
	return writeRequests(wsDir, f)
}

func writeRequests(wsDir string, f requestFile) error {
	p := requestPath(wsDir)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	// Write-and-rename: a collector interrupted mid-write must not leave a
	// half-file, because the recovery from that is a duplicate request and
	// a second approval nobody expected to give.
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}
