package main

// Additional end-to-end coverage of `orion prioritise` (OR-280), split from
// prioritise_cli_test.go: argument-shape refusals, the `--project` flag, the
// `prioritise`/`prioritize` alias, project binding, and a mid-sequence
// failure's exit code and count. Same fake-Jira-subprocess approach as the
// rest of the suite; nothing here reaches a real Jira.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/orion-sdlc/orion/internal/testproc"
)

// unboundQueueProject is a working copy with tracker on but no project
// binding: `prioritise` must accept tickets from any project when there is
// no bound project to check them against.
func unboundQueueProject(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	body := `{"version":1,"tracker":{"enabled":true,"provider":"jira",` +
		`"project_key":"","queue_label":"ORION"}}`
	if err := os.WriteFile(filepath.Join(dir, "orion.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// disabledTrackerProject is a working copy where the tracker stage itself is
// off: there is no queue to reorder at all.
func disabledTrackerProject(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	body := `{"version":1,"tracker":{"enabled":false,"provider":"jira",` +
		`"project_key":"OR","queue_label":"ORION"}}`
	if err := os.WriteFile(filepath.Join(dir, "orion.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// No tickets at all is refused with usage rather than acting on nothing.
func TestCLIPrioritiseRefusesNoTickets(t *testing.T) {
	bin := orionBinary(t)
	f := &fakeRankJira{tickets: map[string]*rankTicket{}}
	srv := f.server(t)

	out, errOut, code := runPrioritiseCmd(t, bin, srv.URL, queueProject(t))
	if code != 64 {
		t.Fatalf("expected a usage exit (64) for no tickets, got %d: %s%s", code, out, errOut)
	}
	if len(f.ranks) != 0 {
		t.Errorf("it wrote something for no tickets: %v", f.ranks)
	}
}

// Both spellings dispatch to the same command and behave identically.
func TestCLIPrioritiseAcceptsBothSpellings(t *testing.T) {
	bin := orionBinary(t)
	for _, verb := range []string{"prioritise", "prioritize"} {
		f := &fakeRankJira{tickets: map[string]*rankTicket{
			"OR-100": inQueue("Medium"),
			"OR-140": inQueue("Medium"),
		}}
		srv := f.server(t)

		out, errOut, code := runPrioritiseCmdWithVerb(t, bin, verb, srv.URL, queueProject(t), "OR-100", "OR-140")
		if code != 0 {
			t.Fatalf("%s: expected success, got exit %d: %s%s", verb, code, out, errOut)
		}
		if got := strings.Join(f.ranks, "|"); got != "OR-140 after OR-100" {
			t.Errorf("%s: ranked %q, want the same ordering the other spelling produces", verb, got)
		}
	}
}

// runPrioritiseCmdWithVerb is runPrioritiseCmd but lets the caller pick which
// spelling of the command to invoke.
func runPrioritiseCmdWithVerb(t *testing.T, bin, verb, jiraURL, workdir string, args ...string) (stdout, stderr string, exitCode int) {
	t.Helper()
	cmd := testproc.Command(t, bin, append([]string{verb}, args...)...)
	cmd.Dir = workdir
	cmd.Env = append(os.Environ(),
		"ORION_HOME="+t.TempDir(),
		"ORION_JIRA_URL="+jiraURL,
		"ORION_JIRA_EMAIL=qa@example.com",
		"ORION_JIRA_TOKEN=t",
		"NO_COLOR=1",
	)
	var out, errb strings.Builder
	cmd.Stdout = &out
	cmd.Stderr = &errb
	err := cmd.Run()
	code := 0
	if exitErr, ok := err.(*exec.ExitError); ok {
		code = exitErr.ExitCode()
	} else if err != nil {
		t.Fatalf("running orion %s %v: %v", verb, args, err)
	}
	return out.String(), errb.String(), code
}

// --project KEY names the same project the keys already imply: the flag
// confirms rather than contradicts, and the run proceeds exactly as it would
// without it.
func TestCLIPrioritiseProjectFlagMatchingKeysSucceeds(t *testing.T) {
	bin := orionBinary(t)
	f := &fakeRankJira{tickets: map[string]*rankTicket{
		"OR-100": inQueue("Medium"),
		"OR-140": inQueue("Medium"),
	}}
	srv := f.server(t)

	out, errOut, code := runPrioritiseCmd(t, bin, srv.URL, queueProject(t),
		"OR-100", "OR-140", "--project", "OR")
	if code != 0 {
		t.Fatalf("expected success, got exit %d: %s%s", code, out, errOut)
	}
	if got := strings.Join(f.ranks, "|"); got != "OR-140 after OR-100" {
		t.Errorf("ranked %q, want OR-140 after OR-100", got)
	}
}

// --project KEY that contradicts the project the keys imply is refused: one
// of the two is wrong, and writing to either without saying which would hide
// the mistake.
func TestCLIPrioritiseProjectFlagMismatchingKeysIsRefused(t *testing.T) {
	bin := orionBinary(t)
	f := &fakeRankJira{tickets: map[string]*rankTicket{
		"OR-100": inQueue("Medium"),
		"OR-140": inQueue("Medium"),
	}}
	srv := f.server(t)

	out, errOut, code := runPrioritiseCmd(t, bin, srv.URL, queueProject(t),
		"OR-100", "OR-140", "--project", "FCIA")
	combined := out + errOut
	if code == 0 {
		t.Fatalf("a --project mismatching the keys exited 0: %s", combined)
	}
	if len(f.ranks) != 0 {
		t.Errorf("it wrote %v before refusing the mismatch", f.ranks)
	}
	if !strings.Contains(combined, "FCIA") || !strings.Contains(combined, "OR") {
		t.Errorf("the refusal does not name both projects: %s", combined)
	}
}

// A range that expands to a single ticket is refused for the same reason a
// single bare key is: one ticket names no ordering.
func TestCLIPrioritiseRefusesARangeThatExpandsToOneTicket(t *testing.T) {
	bin := orionBinary(t)
	f := &fakeRankJira{tickets: map[string]*rankTicket{"OR-140": inQueue("Medium")}}
	srv := f.server(t)

	out, errOut, code := runPrioritiseCmd(t, bin, srv.URL, queueProject(t), "OR-140..OR-140")
	if code != 64 {
		t.Fatalf("expected a usage exit (64), got %d: %s%s", code, out, errOut)
	}
	if len(f.ranks) != 0 {
		t.Errorf("it wrote something for a single-ticket range: %v", f.ranks)
	}
}

// A rank call failing partway through the sequence stops the run there and
// reports exactly how many of the N moves before it succeeded, so a re-run
// picks up from a known point rather than guessing.
func TestCLIPrioritiseStopsAndReportsProgressOnMidSequenceFailure(t *testing.T) {
	bin := orionBinary(t)
	tickets := map[string]*rankTicket{
		"OR-100": inQueue("Medium"),
		"OR-140": inQueue("Medium"),
		"OR-142": inQueue("Medium"),
	}
	var ranks []string
	rankCalls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "PUT" && r.URL.Path == "/rest/agile/1.0/issue/rank":
			var body struct {
				Issues         []string `json:"issues"`
				RankAfterIssue string   `json:"rankAfterIssue"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			ranks = append(ranks, strings.Join(body.Issues, "+")+" after "+body.RankAfterIssue)
			rankCalls++
			// The first move (OR-140 after OR-100) succeeds; every move from
			// the second one onward fails, so the run stops with exactly one
			// of its two moves made.
			if rankCalls >= 2 {
				w.WriteHeader(500)
				_, _ = w.Write([]byte(`{"errorMessages":["nope"]}`))
				return
			}
			w.WriteHeader(204)

		case r.Method == "GET" && strings.HasPrefix(r.URL.Path, "/rest/api/3/issue/"):
			key := strings.TrimPrefix(r.URL.Path, "/rest/api/3/issue/")
			tk, ok := tickets[key]
			if !ok {
				w.WriteHeader(404)
				_, _ = w.Write([]byte(`{"errorMessages":["Issue does not exist"]}`))
				return
			}
			fields := map[string]any{
				"summary": "x",
				"labels":  tk.labels,
				"status": map[string]any{
					"name":           "To Do",
					"statusCategory": map[string]any{"key": "new"},
				},
				"priority": map[string]any{"name": tk.priority},
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"key": key, "fields": fields})

		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(srv.Close)

	out, errOut, code := runPrioritiseCmd(t, bin, srv.URL, queueProject(t),
		"OR-100", "OR-140", "OR-142")
	combined := out + errOut
	if code == 0 {
		t.Fatalf("a mid-sequence failure exited 0: %s", combined)
	}
	if !strings.Contains(combined, "1 of 2 moves were made") {
		t.Errorf("the failure does not report how many of the moves succeeded: %s", combined)
	}
	if len(ranks) != 2 {
		t.Errorf("expected the failed call to still be attempted (2 total), got %v", ranks)
	}
}

// A repository bound to one project refuses to reorder another project's
// tickets: the queue label belongs to the bound project, so writing rank to
// a foreign ticket would reorder a queue nothing here watches.
func TestCLIPrioritiseRefusesAnotherProjectsKeys(t *testing.T) {
	bin := orionBinary(t)
	f := &fakeRankJira{tickets: map[string]*rankTicket{
		"FCIA-6": inQueue("Medium"),
		"FCIA-7": inQueue("Medium"),
	}}
	srv := f.server(t)

	out, errOut, code := runPrioritiseCmd(t, bin, srv.URL, queueProject(t), "FCIA-6", "FCIA-7")
	combined := out + errOut
	if code == 0 {
		t.Fatalf("FCIA keys were reordered from a repository bound to OR: %s", combined)
	}
	if len(f.ranks) != 0 {
		t.Errorf("a refused prioritise still wrote %v", f.ranks)
	}
	if !strings.Contains(combined, "OR") || !strings.Contains(combined, "FCIA") {
		t.Errorf("the refusal does not name the bound project and the mismatch: %s", combined)
	}
}

// A repository with no project binding accepts tickets from any project --
// there is nothing bound to check them against.
func TestCLIPrioritiseAcceptsAnyProjectWhenUnbound(t *testing.T) {
	bin := orionBinary(t)
	f := &fakeRankJira{tickets: map[string]*rankTicket{
		"FCIA-6": inQueue("Medium"),
		"FCIA-7": inQueue("Medium"),
	}}
	srv := f.server(t)

	out, errOut, code := runPrioritiseCmd(t, bin, srv.URL, unboundQueueProject(t), "FCIA-6", "FCIA-7")
	if code != 0 {
		t.Fatalf("expected success from an unbound repository, got exit %d: %s%s", code, out, errOut)
	}
	if got := strings.Join(f.ranks, "|"); got != "FCIA-7 after FCIA-6" {
		t.Errorf("ranked %q, want FCIA-7 after FCIA-6", got)
	}
}

// A repository with the tracker disabled refuses with an explanation: there
// is no queue to reorder.
func TestCLIPrioritiseRefusesWhenTrackerDisabled(t *testing.T) {
	bin := orionBinary(t)
	f := &fakeRankJira{tickets: map[string]*rankTicket{
		"OR-100": inQueue("Medium"),
		"OR-140": inQueue("Medium"),
	}}
	srv := f.server(t)

	out, errOut, code := runPrioritiseCmd(t, bin, srv.URL, disabledTrackerProject(t), "OR-100", "OR-140")
	combined := out + errOut
	if code == 0 {
		t.Fatalf("a disabled tracker still exited 0: %s", combined)
	}
	if len(f.ranks) != 0 {
		t.Errorf("it wrote %v with the tracker disabled", f.ranks)
	}
	if !strings.Contains(combined, "disabled") {
		t.Errorf("the refusal does not explain the tracker is disabled: %s", combined)
	}
}

// Tickets that all carry the SAME priority are ordinary: nothing about their
// priority contradicts the order asked for, so the run proceeds regardless of
// what order the tickets were typed in relative to anything else.
func TestCLIPrioritiseAcceptsIdenticalPriorities(t *testing.T) {
	bin := orionBinary(t)
	f := &fakeRankJira{tickets: map[string]*rankTicket{
		"OR-100": inQueue("High"),
		"OR-140": inQueue("High"),
		"OR-142": inQueue("High"),
	}}
	srv := f.server(t)

	out, errOut, code := runPrioritiseCmd(t, bin, srv.URL, queueProject(t),
		"OR-142", "OR-100", "OR-140")
	if code != 0 {
		t.Fatalf("identical priorities were refused: exit %d: %s%s", code, out, errOut)
	}
	if got, want := strings.Join(f.ranks, "|"), "OR-100 after OR-142|OR-140 after OR-100"; got != want {
		t.Errorf("ranked %q, want %q", got, want)
	}
}

// A project with priority disabled reads every ticket's priority as empty --
// distinctPriorities treats that as one shared value ("no priority"), not as
// each ticket differing from the others, so the reorder goes through.
func TestCLIPrioritiseAcceptsWhenPriorityIsDisabledProjectWide(t *testing.T) {
	bin := orionBinary(t)
	f := &fakeRankJira{tickets: map[string]*rankTicket{
		"OR-100": inQueue(""),
		"OR-140": inQueue(""),
	}}
	srv := f.server(t)

	out, errOut, code := runPrioritiseCmd(t, bin, srv.URL, queueProject(t), "OR-100", "OR-140")
	if code != 0 {
		t.Fatalf("a priority-disabled project was refused: exit %d: %s%s", code, out, errOut)
	}
	if got, want := strings.Join(f.ranks, "|"), "OR-140 after OR-100"; got != want {
		t.Errorf("ranked %q, want %q", got, want)
	}
}

// The refusal for differing priorities has to be ACTIONABLE: an operator
// reading it needs to know which priority belongs to which ticket, not just
// that "priority" is the word behind the word "refused".
func TestCLIPrioritiseRefusalNamesEachPriorityAndItsTicket(t *testing.T) {
	bin := orionBinary(t)
	f := &fakeRankJira{tickets: map[string]*rankTicket{
		"OR-100": inQueue("Medium"),
		"OR-140": inQueue("High"),
	}}
	srv := f.server(t)

	out, errOut, code := runPrioritiseCmd(t, bin, srv.URL, queueProject(t), "OR-100", "OR-140")
	combined := out + errOut
	if code == 0 {
		t.Fatalf("differing priorities exited 0: %s", combined)
	}
	for _, want := range []string{"Medium", "High", "OR-100", "OR-140"} {
		if !strings.Contains(combined, want) {
			t.Errorf("refusal does not name %q, so the operator cannot tell which ticket to fix:\n%s",
				want, combined)
		}
	}
}

// The exit code is a contract a script can check: 0 on success, non-zero on
// failure, checked together so a change that breaks one direction shows up
// here rather than only in individual scenario tests.
func TestCLIPrioritiseExitCodeContract(t *testing.T) {
	bin := orionBinary(t)

	t.Run("success", func(t *testing.T) {
		f := &fakeRankJira{tickets: map[string]*rankTicket{
			"OR-100": inQueue("Medium"),
			"OR-140": inQueue("Medium"),
		}}
		srv := f.server(t)
		_, _, code := runPrioritiseCmd(t, bin, srv.URL, queueProject(t), "OR-100", "OR-140")
		if code != 0 {
			t.Errorf("a successful reorder exited %d, want 0", code)
		}
	})

	t.Run("failure", func(t *testing.T) {
		f := &fakeRankJira{
			tickets:    map[string]*rankTicket{"OR-100": inQueue("Medium"), "OR-140": inQueue("Medium")},
			rankStatus: 500,
		}
		srv := f.server(t)
		_, _, code := runPrioritiseCmd(t, bin, srv.URL, queueProject(t), "OR-100", "OR-140")
		if code == 0 {
			t.Errorf("a failed rank call exited 0, want non-zero")
		}
	})
}
