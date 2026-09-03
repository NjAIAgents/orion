package collect

// What a batch proved, written down so a later pass does not buy it again
// (ADR 0017, OR-253).
//
// WHY THIS EXISTS AT ALL. A batch that is green and waiting on a human is
// finished with CI and not finished with the ref. Without a record, the next
// pass reassembles the same members, force-pushes a new merge commit, opens
// the pull request again and spends a second CI run re-proving what is
// already proved -- once per tick, for as long as the approver takes to look.
// An approval gate that costs a CI run per minute of human latency is worse
// than no gate.
//
// A FILE, not a database (ADR 0017). It sits beside the approval requests in
// the workspace directory and is written the same way, so the whole of a
// batch's durable state is greppable, diffable, and inspectable after a crash
// without a client.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// batchState is one batch, mid-flight.
//
// The SHAs are the point. Members and a ref say WHAT was assembled; the SHAs
// say what was PROVED and what it was proved against, which is the only pair
// that can answer "may this still merge?" on a later pass.
type batchState struct {
	Ref     string    `json:"ref"`
	Base    string    `json:"base"`
	Members []string  `json:"members"`
	Status  string    `json:"status"`
	SavedAt time.Time `json:"saved_at"`

	// BaseSHA is the base as it stood when the set was assembled and tested.
	// ValidatedSHA is the ref CI proved. Recorded rather than re-derived: a
	// repository that has moved on cannot answer either question afterwards.
	BaseSHA      string `json:"base_sha"`
	ValidatedSHA string `json:"validated_sha"`

	// PRURL is the batch's pull request, recorded so a landed member's ticket
	// can be commented with where its work went (OR-314).
	//
	// IN THE RECORD because approval is a human-length gap: the process that
	// opened the pull request is often not the process that merges it, and a
	// comment that has to say "the batch" with no address sends the reader
	// nowhere.
	PRURL string `json:"pr_url,omitempty"`

	// TestingSince is when the batch was published for CI, and it is where
	// the deadline lives now (OR-251).
	//
	// IN THE RECORD, because the wait spans ticks. It used to be a local
	// variable inside a poll loop, which is why waiting for a build meant
	// blocking the tick that every other ticket reports through.
	TestingSince time.Time `json:"testing_since,omitempty"`
}

// waitedOut reports whether a testing batch has exceeded its deadline.
//
// The same refusal as before, in a new home: silence is never read as green,
// however long it lasts. What changed is only who is waiting.
func (s batchState) waitedOut(now time.Time, limit time.Duration) bool {
	return s.Status == batchTesting && !s.TestingSince.IsZero() &&
		now.Sub(s.TestingSince) > limit
}

// batchTesting is a batch whose CI is still running (OR-251). It resumes to
// one more status read, never to a fresh assembly: the ref is published, the
// pull request is open, and the build that is running is the one being waited
// for.
const batchTesting = "testing"

// batchValidated is the only status that resumes to a MERGE. A batch in any other state
// has nothing worth reusing, and the honest thing is to assemble again.
const batchValidated = "validated"

func batchStatePath(wsDir string) string {
	return filepath.Join(wsDir, "batch-state.json")
}

// loadBatchState reads the record, reporting absence rather than erroring.
//
// A corrupt or unreadable file is treated as no record at all, for the reason
// requests.go gives about its own: the cost is one repeated CI run, and the
// alternative is a collector that refuses to work because of a file it is
// about to rewrite anyway.
func loadBatchState(wsDir string) (batchState, bool) {
	b, err := os.ReadFile(batchStatePath(wsDir))
	if err != nil {
		return batchState{}, false
	}
	var s batchState
	if err := json.Unmarshal(b, &s); err != nil || s.Ref == "" {
		return batchState{}, false
	}
	return s, true
}

func saveBatchState(wsDir string, s batchState) error {
	s.SavedAt = time.Now()
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(batchStatePath(wsDir), append(b, '\n'), 0o600)
}

// clearBatchState forgets the record. Best effort: a stale file costs one
// comparison on the next pass, and failing a landed batch over cleanup would
// be worse than the litter.
func clearBatchState(wsDir string) { _ = os.Remove(batchStatePath(wsDir)) }

// resumable reports whether this record still describes the batch about to be
// assembled, and may therefore be used instead of testing it again.
//
// EVERY condition has to hold, and each rules out a different way of merging
// something nobody proved:
//
//   - the same status: only a validated batch has anything to reuse.
//   - the same base branch: a record from a different work branch says
//     nothing about this one.
//   - the same members, exactly: a member added since was never tested, and
//     one missing means the set that was proved is not the set being landed.
//   - the same base commit: this is ADR 0017's precondition. If the base
//     moved, the proof belongs to a tree that no longer exists.
func (s batchState) resumable(base, baseSHA string, members []Member) bool {
	if s.Status != batchValidated || s.Base != base {
		return false
	}
	if s.BaseSHA == "" || s.BaseSHA != baseSHA {
		return false
	}
	return sameMembers(s.Members, members)
}

// sameMembers reports whether a recorded member list is the set now on offer.
//
// Shared by both resume gates (OR-261). A validated batch may only merge the
// set it proved, and a testing batch may only be waited on while the set it
// published is still the set being asked about -- the same comparison for two
// different reasons, and two copies of it would drift into disagreeing about
// what "the same batch" means.
func sameMembers(recorded []string, members []Member) bool {
	if len(recorded) != len(members) {
		return false
	}
	want := append([]string(nil), keysOf(members)...)
	have := append([]string(nil), recorded...)
	sort.Strings(want)
	sort.Strings(have)
	for i := range want {
		if want[i] != have[i] {
			return false
		}
	}
	return true
}
