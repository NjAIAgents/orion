package main

// End-to-end coverage of `orion prioritise` (OR-280): the dispatch, the
// GetIssue/rank sequencing, the preview that precedes every write, and the
// exit codes. runPrioritise calls os.Exit and has no injectable Jira client,
// so -- like queueedit_cli_test.go -- this drives the compiled binary as a
// subprocess against a fake Jira. Nothing here reaches a real Jira.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/orion-sdlc/orion/internal/testproc"
)

// rankTicket is one ticket as the fake tracker holds it.
type rankTicket struct {
	labels   []string
	priority string
}

// fakeRankJira answers the surface `orion prioritise` uses: fetching an issue
// and the agile ranking endpoint.
type fakeRankJira struct {
	tickets map[string]*rankTicket
	// ranks is one entry per rank call: "MOVED after TARGET".
	ranks []string
	// rankStatus, when set, is the status every rank call answers with.
	rankStatus int
}

func (f *fakeRankJira) server(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "PUT" && r.URL.Path == "/rest/agile/1.0/issue/rank":
			var body struct {
				Issues         []string `json:"issues"`
				RankAfterIssue string   `json:"rankAfterIssue"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			f.ranks = append(f.ranks, strings.Join(body.Issues, "+")+" after "+body.RankAfterIssue)
			if f.rankStatus != 0 {
				w.WriteHeader(f.rankStatus)
				_, _ = w.Write([]byte(`{"errorMessages":["nope"]}`))
				return
			}
			w.WriteHeader(204)

		case r.Method == "GET" && strings.HasPrefix(r.URL.Path, "/rest/api/3/issue/"):
			key := strings.TrimPrefix(r.URL.Path, "/rest/api/3/issue/")
			tk, ok := f.tickets[key]
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
			}
			if tk.priority != "" {
				fields["priority"] = map[string]any{"name": tk.priority}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"key": key, "fields": fields})

		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// runPrioritiseCmd invokes `orion prioritise ...` as a subprocess against a
// fake Jira, isolated from any real Orion home or registry.
func runPrioritiseCmd(t *testing.T, bin, jiraURL, workdir string, args ...string) (stdout, stderr string, exitCode int) {
	t.Helper()
	cmd := testproc.Command(t, bin, append([]string{"prioritise"}, args...)...)
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
		t.Fatalf("running orion prioritise %v: %v", args, err)
	}
	return out.String(), errb.String(), code
}

func inQueue(priority string) *rankTicket {
	return &rankTicket{labels: []string{"ORION"}, priority: priority}
}

// The whole point of the command: the tickets named end up in the order
// typed, which means each one is ranked immediately behind the one before it.
func TestCLIPrioritiseRanksEachTicketBehindTheOneBeforeIt(t *testing.T) {
	bin := orionBinary(t)
	f := &fakeRankJira{tickets: map[string]*rankTicket{
		"OR-100": inQueue("Medium"),
		"OR-140": inQueue("Medium"),
		"OR-142": inQueue("Medium"),
	}}
	srv := f.server(t)

	out, errOut, code := runPrioritiseCmd(t, bin, srv.URL, queueProject(t),
		"OR-142", "OR-100", "OR-140")
	if code != 0 {
		t.Fatalf("expected success, got exit %d: %s%s", code, out, errOut)
	}
	want := "OR-100 after OR-142|OR-140 after OR-100"
	if got := strings.Join(f.ranks, "|"); got != want {
		t.Errorf("ranked %q, want %q -- the order given is the order written", got, want)
	}
}

// The order is previewed before the first write, the same property `queue
// add` has: what a range expanded to is visible before anything moves.
func TestCLIPrioritisePreviewsTheOrderBeforeWriting(t *testing.T) {
	bin := orionBinary(t)
	f := &fakeRankJira{tickets: map[string]*rankTicket{
		"OR-140": inQueue("Medium"),
		"OR-141": inQueue("Medium"),
		"OR-142": inQueue("Medium"),
	}}
	srv := f.server(t)

	out, errOut, code := runPrioritiseCmd(t, bin, srv.URL, queueProject(t), "OR-140..OR-142")
	if code != 0 {
		t.Fatalf("expected success, got exit %d: %s%s", code, out, errOut)
	}
	planAt := strings.Index(out, "plan")
	writeAt := strings.Index(out, "ranked")
	if planAt < 0 || writeAt < 0 || planAt > writeAt {
		t.Errorf("the plan was not printed before the first write (plan %d, write %d): %s",
			planAt, writeAt, out)
	}
	if got := strings.Join(f.ranks, "|"); got != "OR-141 after OR-140|OR-142 after OR-141" {
		t.Errorf("a range did not expand into the order it names: %q", strings.Join(f.ranks, "|"))
	}
}

// Priority is read before rank, so an order priority would override must not
// be written at all -- and a script must be able to tell.
func TestCLIPrioritiseWritesNothingWhenPrioritiesDiffer(t *testing.T) {
	bin := orionBinary(t)
	f := &fakeRankJira{tickets: map[string]*rankTicket{
		"OR-100": inQueue("Medium"),
		"OR-140": inQueue("High"),
	}}
	srv := f.server(t)

	out, errOut, code := runPrioritiseCmd(t, bin, srv.URL, queueProject(t), "OR-100", "OR-140")
	combined := out + errOut
	if code == 0 {
		t.Fatalf("an ordering priority would override exited 0: %s", combined)
	}
	if len(f.ranks) != 0 {
		t.Errorf("it wrote %v before refusing; nothing may move", f.ranks)
	}
	if !strings.Contains(combined, "priority") {
		t.Errorf("the refusal does not say priority is the reason: %s", combined)
	}
}

// A ticket outside the queue stops the whole run: an ordering is one
// intention, so applying the part that resolved leaves a queue nobody asked
// for while reporting success.
func TestCLIPrioritiseWritesNothingWhenOneTicketIsNotQueued(t *testing.T) {
	bin := orionBinary(t)
	f := &fakeRankJira{tickets: map[string]*rankTicket{
		"OR-100": inQueue("Medium"),
		"OR-140": {priority: "Medium"}, // never queued
		"OR-142": inQueue("Medium"),
	}}
	srv := f.server(t)

	out, errOut, code := runPrioritiseCmd(t, bin, srv.URL, queueProject(t),
		"OR-100", "OR-140", "OR-142")
	combined := out + errOut
	if code == 0 {
		t.Fatalf("a partial ordering exited 0: %s", combined)
	}
	if len(f.ranks) != 0 {
		t.Errorf("it ranked %v anyway, so a partial order was written: %s", f.ranks, combined)
	}
	if !strings.Contains(combined, "orion queue add") {
		t.Errorf("the refusal does not name the way out: %s", combined)
	}
}

// One ticket is not an ordering. Accepting it would be a command that writes
// nothing and reports success, which is how a UI button comes to do nothing.
func TestCLIPrioritiseRefusesASingleTicket(t *testing.T) {
	bin := orionBinary(t)
	f := &fakeRankJira{tickets: map[string]*rankTicket{"OR-100": inQueue("Medium")}}
	srv := f.server(t)

	out, errOut, code := runPrioritiseCmd(t, bin, srv.URL, queueProject(t), "OR-100")
	if code != 64 {
		t.Fatalf("expected a usage exit (64), got %d: %s%s", code, out, errOut)
	}
	if len(f.ranks) != 0 {
		t.Errorf("it wrote something for a single ticket: %v", f.ranks)
	}
}
