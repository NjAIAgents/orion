package work

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/orion-sdlc/orion/internal/events"
	"github.com/orion-sdlc/orion/internal/ui"
)

// The failure this exists for: an agent's commit claiming a human wrote it.
// It must be reported as WRONG rather than merely unattributed, because it
// is the one status that asserts something false (OR-193).
func TestUnassistedOnAnAgentCommitIsReportedAsWrong(t *testing.T) {
	a := attribution{Status: "unassisted", Total: 3}
	if !a.wrong() {
		t.Fatal("an unassisted agent commit is a false claim, not a gap")
	}
	if !strings.Contains(a.line(), "recorded as a human's") {
		t.Errorf("the line does not say what is wrong: %q", a.line())
	}
	if !strings.Contains(a.line(), "OR-193") {
		t.Errorf("the line does not point at the ticket: %q", a.line())
	}
}

// Absence of evidence is not a false claim, and must not be reported as one.
func TestUnmatchedIsHonestRatherThanWrong(t *testing.T) {
	a := attribution{Status: "unmatched", Total: 2}
	if a.wrong() {
		t.Error("unmatched says 'I could not tell', which is honest")
	}
	if a.line() == "" {
		t.Error("unmatched still deserves a line: it is not the answer we want")
	}
}

// A run whose commits are properly attributed says nothing. The reader is
// watching a run, not auditing a ledger.
func TestAnAttributedRunSaysNothing(t *testing.T) {
	for _, s := range []string{"intersected", "assisted", "observed"} {
		a := attribution{Status: s, Total: 4}
		if got := a.line(); got != "" {
			t.Errorf("%s should be silent, said %q", s, got)
		}
	}
}

// The worst status among a run's commits is the one reported: a run that
// produced one honest commit and one false one has a problem.
func TestTheWorstStatusIsTheOneReported(t *testing.T) {
	dir := t.TempDir()
	gitInit(t, dir)
	commitWithTrailer(t, dir, "first", "v=2; status=intersected; method=intersected")
	commitWithTrailer(t, dir, "second", "v=2; status=unassisted; method=undetermined")

	got := readAttribution(dir, "main", 2)

	if got.Status != "unassisted" {
		t.Errorf("status is %q, want unassisted -- the worst of the two", got.Status)
	}
	if got.Total != 2 {
		t.Errorf("counted %d commits, want 2", got.Total)
	}
	if !got.wrong() {
		t.Error("a run containing a false claim is wrong overall")
	}
}

// A commit with no trailer at all is a different fault from one whose
// trailer could not decide: it means the hook never ran here.
func TestACommitWithNoTrailerIsReportedAsTheHookNotRunning(t *testing.T) {
	dir := t.TempDir()
	gitInit(t, dir)
	commitWithTrailer(t, dir, "no trailer here", "")

	got := readAttribution(dir, "main", 1)

	if got.Missing != 1 {
		t.Errorf("missing is %d, want 1", got.Missing)
	}
	if !strings.Contains(got.line(), "hook did not run") {
		t.Errorf("the line does not name the fault: %q", got.line())
	}
}

// Reading attribution must never be able to fail a run. It is a record
// ABOUT the work; good code does not get thrown away over a trailer.
func TestReadingAttributionNeverFailsTheRun(t *testing.T) {
	if got := readAttribution(t.TempDir(), "main", 2); got.line() == "" && got.Total != 0 {
		t.Error("a non-repository should degrade quietly")
	}
	if got := readAttribution(t.TempDir(), "main", 0); got.Status != "" {
		t.Error("no commits means nothing to say")
	}
}

func gitInit(t *testing.T, dir string) {
	t.Helper()
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"},
		{"config", "user.email", "t@example.com"},
		{"config", "user.name", "T"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
}

func commitWithTrailer(t *testing.T, dir, msg, trailer string) {
	t.Helper()
	name := filepath.Join(dir, strings.ReplaceAll(msg, " ", "_")+".txt")
	if err := exec.Command("touch", name).Run(); err != nil {
		t.Fatal(err)
	}
	body := msg
	if trailer != "" {
		body += "\n\nAI-Attribution: " + trailer
	}
	for _, args := range [][]string{{"add", "-A"}, {"commit", "-q", "-m", body}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
}

// The bug this kind exists to prevent: `orion log` replays a stored event
// through ui.VerbFor(kind). Emitted as a plain note, a commit falsely
// claiming a human wrote an agent's code would read back in GREEN as
// something that worked -- the failure repeating itself inside the tool
// built to surface it (OR-193).
func TestAStoredAttributionEventReplaysAsAWarning(t *testing.T) {
	if got := ui.VerbFor(events.KindAttribution); got != ui.VerbWarn {
		t.Errorf("a stored attribution event replays as %q, want %q", got, ui.VerbWarn)
	}
}
