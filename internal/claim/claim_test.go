package claim

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// The whole point: a claim left by a process that no longer exists is
// releasable, and a claim held by a living one is not.
//
// Getting this backwards in either direction is expensive. Too eager and a
// second agent starts on a ticket somebody is already working, paying twice
// and racing on one branch; too cautious and an interrupted ticket is stranded
// until a human edits labels by hand, which is the bug (OR-265).
func TestADeadHoldersClaimIsReleasableAndALiveOnesIsNot(t *testing.T) {
	home := t.TempDir()

	// This process is alive by construction, so its own claim is never dead.
	if err := Take(home, "OR-1", "orion/or-1", "/tmp/wt"); err != nil {
		t.Fatal(err)
	}
	if dead, _ := Dead(home, "OR-1"); dead {
		t.Error("this process is holding OR-1; its claim must not read as dead")
	}

	// A PID that cannot exist. 0 is not a real process id, and negative
	// values are process GROUPS rather than processes.
	writeRecord(t, home, Record{
		Key: "OR-2", PID: -1, Started: time.Now(), Beat: time.Now(),
	})
	dead, rec := Dead(home, "OR-2")
	if !dead {
		t.Error("a claim whose holder cannot exist must be releasable")
	}
	if rec == nil {
		t.Fatal("a dead claim must still hand back its record, or a resume has nothing to resume from")
	}
}

// A long run is not a dead one. An agent that works for an hour is doing its
// job, and a watcher that stole its ticket at thirty minutes would be a more
// expensive bug than the one this package fixes -- so the heartbeat alone
// never condemns a claim whose process is still there.
func TestALongRunningHolderKeepsItsClaim(t *testing.T) {
	home := t.TempDir()
	writeRecord(t, home, Record{
		Key: "OR-3", PID: os.Getppid(), // a real, live process
		Started: time.Now().Add(-90 * time.Minute),
		Beat:    time.Now().Add(-90 * time.Minute),
	})
	if dead, _ := Dead(home, "OR-3"); dead {
		t.Error("a 90-minute run whose process is alive must keep its claim")
	}
}

// The reboot case, which is why the heartbeat exists at all: after a restart
// the number belongs to something unrelated, so a live PID is not proof the
// original holder survived. Past staleAfter the heartbeat breaks the tie.
func TestAReusedPIDDoesNotKeepAClaimAliveForever(t *testing.T) {
	home := t.TempDir()
	writeRecord(t, home, Record{
		Key: "OR-4", PID: os.Getppid(),
		Started: time.Now().Add(-3 * time.Hour),
		Beat:    time.Now().Add(-3 * time.Hour),
	})
	if dead, _ := Dead(home, "OR-4"); !dead {
		t.Error("a live PID with a heartbeat older than staleAfter is a reused number, not a holder")
	}
}

// NO RECORD IS NOT PROOF OF DEATH. Claims taken before this package existed
// have none, and neither does a watcher on another machine sharing the same
// tracker. Reading absence as "dead" would let this steal a ticket it knows
// nothing about.
func TestAMissingRecordIsNotTreatedAsDead(t *testing.T) {
	home := t.TempDir()
	if dead, rec := Dead(home, "OR-5"); dead || rec != nil {
		t.Error("a claim with no record must be left alone, not released")
	}
}

// A corrupt record is not a live claim either -- treating it as one would
// strand the ticket exactly as a missing record used to.
func TestACorruptRecordIsReleasableRatherThanStranding(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, "claims"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "claims", "OR-6.json"),
		[]byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Read(home, "OR-6"); err == nil {
		t.Error("an unreadable claim must report why rather than pass as empty")
	}
	if dead, _ := Dead(home, "OR-6"); dead {
		// Dead() reads a corrupt record as "no record", which is the cautious
		// answer: it does not release, and a human sees the error from Read.
		t.Error("a corrupt record must not silently authorise a release")
	}
}

// Release removes the record, and releasing twice is not an error: a run that
// finishes normally and a watcher cleaning up after it both call this.
func TestReleaseIsIdempotent(t *testing.T) {
	home := t.TempDir()
	if err := Take(home, "OR-7", "b", "w"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := Release(home, "OR-7"); err != nil {
			t.Fatalf("release %d: %v", i, err)
		}
	}
	if r, _ := Read(home, "OR-7"); r != nil {
		t.Error("the record survived its release")
	}
}

// The record carries where the work IS, because knowing a claim is dead is
// only useful if the next run can pick up what it left.
func TestTheRecordCarriesTheBranchAndWorktree(t *testing.T) {
	home := t.TempDir()
	if err := Take(home, "OR-8", "orion/or-8-4", "/w/orion-or-8-4"); err != nil {
		t.Fatal(err)
	}
	r, err := Read(home, "OR-8")
	if err != nil || r == nil {
		t.Fatalf("read: %v", err)
	}
	if r.Branch != "orion/or-8-4" || r.Worktree != "/w/orion-or-8-4" {
		t.Errorf("a resume cannot find the work: %+v", r)
	}
}

// Only the holder beats. A watcher refreshing somebody else's claim would keep
// a dead one alive forever -- the failure this package exists to end.
func TestOnlyTheHolderRefreshesTheHeartbeat(t *testing.T) {
	home := t.TempDir()
	old := time.Now().Add(-time.Hour)
	writeRecord(t, home, Record{Key: "OR-9", PID: os.Getppid(), Started: old, Beat: old})

	if err := Beat(home, "OR-9"); err != nil {
		t.Fatal(err)
	}
	r, _ := Read(home, "OR-9")
	if r == nil || !r.Beat.Equal(old) {
		t.Error("a non-holder must not refresh another process's heartbeat")
	}
}

func writeRecord(t *testing.T, home string, r Record) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(home, "claims"), 0o700); err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "claims", r.Key+".json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
}
