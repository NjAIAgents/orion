package session

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Given a running supervisor, Then its session is reported alive from the
// heartbeat, not from last-event-timestamp.
func TestAStartedSessionIsAliveByEnumerate(t *testing.T) {
	home := t.TempDir()
	s := Start(home, KindWatch, []string{"proj-a"})
	defer s.Stop()

	live, err := Enumerate(home, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(live) != 1 {
		t.Fatalf("Enumerate = %d sessions, want 1", len(live))
	}
	if live[0].ID != s.ID() {
		t.Errorf("ID = %q, want %q", live[0].ID, s.ID())
	}
	if live[0].Kind != KindWatch {
		t.Errorf("Kind = %q, want %q", live[0].Kind, KindWatch)
	}
}

// Given a supervisor killed with SIGKILL, Then within ~60s no live session
// covers it. Simulated here by writing a record whose LastBeat is old
// enough to be Stale -- exactly what a killed process's last-written record
// looks like once nothing is beating it anymore.
func TestARecordOlderThanStaleAfterIsNotLive(t *testing.T) {
	home := t.TempDir()
	old := Record{
		ID:       "killed",
		Kind:     KindWork,
		Started:  time.Now().Add(-time.Hour),
		LastBeat: time.Now().Add(-StaleAfter - time.Second),
	}
	if err := write(home, old); err != nil {
		t.Fatal(err)
	}

	live, err := Enumerate(home, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(live) != 0 {
		t.Errorf("Enumerate = %d sessions, want 0 (stale record must not show as live)", len(live))
	}
}

// A record beating well within StaleAfter is live.
func TestARecentRecordIsLive(t *testing.T) {
	home := t.TempDir()
	fresh := Record{
		ID:       "fresh",
		Kind:     KindWatch,
		Started:  time.Now(),
		LastBeat: time.Now().Add(-time.Second),
	}
	if err := write(home, fresh); err != nil {
		t.Fatal(err)
	}

	live, err := Enumerate(home, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(live) != 1 {
		t.Fatalf("Enumerate = %d sessions, want 1", len(live))
	}
}

// Given two watchers on one machine, Then each appears as its own session,
// distinguished by scope and pid.
func TestTwoConcurrentSessionsAreDistinct(t *testing.T) {
	home := t.TempDir()
	a := Start(home, KindWatch, []string{"proj-a"})
	defer a.Stop()
	b := Start(home, KindWatch, []string{"proj-b"})
	defer b.Stop()

	if a.ID() == b.ID() {
		t.Fatal("two Start calls produced the same session id")
	}

	live, err := Enumerate(home, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(live) != 2 {
		t.Fatalf("Enumerate = %d sessions, want 2", len(live))
	}
}

// Given a session record left by a process that no longer exists, Then it
// is filtered out by readers (proven above) AND swept by the next session
// start -- never shown as alive, and eventually removed rather than
// accumulating forever.
func TestStartSweepsStaleRecordsLeftByAnEarlierProcess(t *testing.T) {
	home := t.TempDir()
	dead := Record{
		ID:       "long-dead",
		Kind:     KindWatch,
		LastBeat: time.Now().Add(-24 * time.Hour),
	}
	if err := write(home, dead); err != nil {
		t.Fatal(err)
	}

	s := Start(home, KindWork, []string{"proj-a"})
	defer s.Stop()

	if _, err := os.Stat(filepath.Join(home, "sessions", "long-dead.json")); !os.IsNotExist(err) {
		t.Error("Start did not sweep a stale record left by an earlier process")
	}
}

// The dashboard/Enumerate must never delete: sweeping happens only when a
// session STARTS, never as a side effect of reading. Proven by calling
// Enumerate many times against a stale record and confirming the file is
// still there afterward -- only Start (or a direct Sweep call) may remove
// it.
func TestEnumerateNeverDeletesAStaleRecord(t *testing.T) {
	home := t.TempDir()
	dead := Record{ID: "stale-but-untouched", LastBeat: time.Now().Add(-time.Hour)}
	if err := write(home, dead); err != nil {
		t.Fatal(err)
	}

	for i := 0; i < 5; i++ {
		if _, err := Enumerate(home, time.Now()); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := os.Stat(filepath.Join(home, "sessions", "stale-but-untouched.json")); err != nil {
		t.Errorf("Enumerate deleted a stale record (or otherwise disturbed it): %v", err)
	}
}

// Given a session that ends cleanly, Then its record vanishes rather than
// lingering as a stale entry Sweep has to clean up later.
func TestStopRemovesTheRecord(t *testing.T) {
	home := t.TempDir()
	s := Start(home, KindWatch, []string{"proj-a"})
	recPath := filepath.Join(home, "sessions", s.ID()+".json")
	if _, err := os.Stat(recPath); err != nil {
		t.Fatalf("record was never written: %v", err)
	}

	s.Stop()

	if _, err := os.Stat(recPath); !os.IsNotExist(err) {
		t.Error("Stop did not remove the session's record")
	}
}

// Sweep reports how many stale records it removed, and removes only those.
func TestSweepRemovesOnlyStaleRecordsAndReportsTheCount(t *testing.T) {
	home := t.TempDir()
	must := func(r Record) {
		t.Helper()
		if err := write(home, r); err != nil {
			t.Fatal(err)
		}
	}
	must(Record{ID: "stale-1", LastBeat: time.Now().Add(-time.Hour)})
	must(Record{ID: "stale-2", LastBeat: time.Now().Add(-2 * time.Hour)})
	must(Record{ID: "fresh-1", LastBeat: time.Now()})

	n, err := Sweep(home, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("Sweep removed %d records, want 2", n)
	}

	live, err := Enumerate(home, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(live) != 1 || live[0].ID != "fresh-1" {
		t.Errorf("Enumerate after Sweep = %+v, want only fresh-1", live)
	}
}

// THE RUN PATH IS UNAFFECTED ON FAILURE: Start against an unwritable home
// must still return a usable *Session rather than erroring the caller's run
// -- proven by the signature itself (Start returns no error) and by Stop
// completing without hanging or panicking even though nothing was ever
// written.
func TestStartDoesNotFailWhenHomeIsUnwritable(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("running as root: permission checks below do not apply")
	}
	parent := t.TempDir()
	unwritable := filepath.Join(parent, "locked")
	if err := os.Mkdir(unwritable, 0o555); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(unwritable, 0o755) // so TempDir's own cleanup can remove it

	s := Start(filepath.Join(unwritable, "home"), KindWatch, []string{"proj-a"})
	s.Stop()
}

// PID and Host are recorded for display only. Proven negatively: two
// sessions started back to back from the SAME process (same pid) must
// still both enumerate as live and distinct by ID, because liveness never
// consults PID.
func TestPIDIsNotConsultedForLiveness(t *testing.T) {
	home := t.TempDir()
	a := Start(home, KindWatch, nil)
	defer a.Stop()
	b := Start(home, KindWatch, nil)
	defer b.Stop()

	live, err := Enumerate(home, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(live) != 2 {
		t.Fatalf("Enumerate = %d, want 2 (same-pid sessions must both be live)", len(live))
	}
	for _, r := range live {
		if r.PID != os.Getpid() {
			t.Errorf("record %s PID = %d, want this test process's pid %d", r.ID, r.PID, os.Getpid())
		}
	}
}

// A corrupt record must not fail Enumerate or Sweep for every other
// session -- the same partial-tolerance posture as events.Follow.
func TestACorruptRecordIsSkippedNotFatal(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, "sessions"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "sessions", "corrupt.json"), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	good := Record{ID: "good", LastBeat: time.Now()}
	if err := write(home, good); err != nil {
		t.Fatal(err)
	}

	live, err := Enumerate(home, time.Now())
	if err != nil {
		t.Fatalf("Enumerate failed on a corrupt record instead of skipping it: %v", err)
	}
	if len(live) != 1 || live[0].ID != "good" {
		t.Errorf("Enumerate = %+v, want only the good record", live)
	}
}

// Enumerate and Sweep against a sessions directory that does not exist yet
// (nothing has ever started) must return empty/zero, not an error.
func TestEnumerateAndSweepOnAMachineWhereNothingHasEverRun(t *testing.T) {
	home := t.TempDir() // no sessions/ subdirectory created

	live, err := Enumerate(home, time.Now())
	if err != nil {
		t.Fatalf("Enumerate on an empty machine returned an error: %v", err)
	}
	if len(live) != 0 {
		t.Errorf("Enumerate on an empty machine = %d, want 0", len(live))
	}

	n, err := Sweep(home, time.Now())
	if err != nil {
		t.Fatalf("Sweep on an empty machine returned an error: %v", err)
	}
	if n != 0 {
		t.Errorf("Sweep on an empty machine removed %d, want 0", n)
	}
}
