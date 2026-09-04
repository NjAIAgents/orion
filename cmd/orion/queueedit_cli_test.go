package main

// End-to-end coverage of `orion queue add` and `orion queue remove` (OR-223):
// the flag parsing, the GetIssue/SetLabels/TransitionTo sequencing, the preview
// that precedes every write, the exit codes and the text printed. runQueueEdit
// calls os.Exit directly and has no injectable Jira client, so -- same as
// releaseadd_cli_test.go -- this drives the compiled binary as a subprocess
// against a fake Jira server. It stays a local integration test: nothing here
// reaches a real Jira instance.

import (
	"encoding/json"
	"github.com/orion-sdlc/orion/internal/testproc"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// queueTicket is one ticket as the fake tracker holds it.
type queueTicket struct {
	labels   []string
	status   string
	versions []string
}

// fakeQueueJira answers the surface the queue verbs use: fetching an issue,
// updating its labels, and transitioning it.
type fakeQueueJira struct {
	tickets map[string]*queueTicket
	// labelWrites is one entry per PUT: the key, and the add/remove ops.
	labelWrites []string
	// transitions is one entry per POST to /transitions: "KEY->STATUS".
	transitions []string
	writeStatus int // 0 means 204
}

func (f *fakeQueueJira) server(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		switch {
		case r.Method == "GET" && strings.HasSuffix(path, "/transitions"):
			_ = json.NewEncoder(w).Encode(map[string]any{"transitions": []map[string]any{
				{"id": "11", "name": "Back to To Do", "to": map[string]any{"name": "To Do"}},
			}})

		case r.Method == "POST" && strings.HasSuffix(path, "/transitions"):
			key := strings.TrimSuffix(strings.TrimPrefix(path, "/rest/api/3/issue/"), "/transitions")
			f.transitions = append(f.transitions, key+"->To Do")
			if tk := f.tickets[key]; tk != nil {
				tk.status = "To Do"
			}
			w.WriteHeader(204)

		case r.Method == "GET" && strings.HasPrefix(path, "/rest/api/3/issue/"):
			key := strings.TrimPrefix(path, "/rest/api/3/issue/")
			tk, ok := f.tickets[key]
			if !ok {
				w.WriteHeader(404)
				_, _ = w.Write([]byte(`{"errorMessages":["Issue does not exist"]}`))
				return
			}
			versions := make([]map[string]any, 0, len(tk.versions))
			for _, n := range tk.versions {
				versions = append(versions, map[string]any{"name": n})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"key": key,
				"fields": map[string]any{
					"summary": "x",
					"labels":  tk.labels,
					"status": map[string]any{
						"name":           tk.status,
						"statusCategory": map[string]any{"key": "new"},
					},
					"fixVersions": versions,
				},
			})

		case r.Method == "PUT" && strings.HasPrefix(path, "/rest/api/3/issue/"):
			key := strings.TrimPrefix(path, "/rest/api/3/issue/")
			var body struct {
				Update struct {
					Labels []map[string]string `json:"labels"`
				} `json:"update"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			ops := []string{key}
			for _, op := range body.Update.Labels {
				for verb, label := range op {
					ops = append(ops, verb+":"+label)
				}
			}
			f.labelWrites = append(f.labelWrites, strings.Join(ops, " "))
			if f.writeStatus != 0 {
				w.WriteHeader(f.writeStatus)
				_, _ = w.Write([]byte(`{"error":"boom"}`))
				return
			}
			w.WriteHeader(204)

		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// queueProject is a working copy `orion queue` will act in: tracker on, bound
// to OR, queue label ORION.
func queueProject(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	body := `{"version":1,"tracker":{"enabled":true,"provider":"jira",` +
		`"project_key":"OR","queue_label":"ORION"}}`
	if err := os.WriteFile(filepath.Join(dir, "orion.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// runQueueCmd invokes `orion queue <verb> <args>` as a subprocess against a
// fake Jira server, isolated from any real Orion home or registry.
func runQueueCmd(t *testing.T, bin, jiraURL, workdir string, args ...string) (stdout, stderr string, exitCode int) {
	t.Helper()
	cmd := testproc.Command(t, bin, append([]string{"queue"}, args...)...)
	cmd.Dir = workdir
	cmd.Env = append(os.Environ(),
		"ORION_HOME="+t.TempDir(),
		"ORION_JIRA_URL="+jiraURL,
		"ORION_JIRA_EMAIL=qa@example.com",
		"ORION_JIRA_TOKEN=t",
		// No colour: the assertions below read the words, not the escapes.
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
		t.Fatalf("running orion queue %v: %v", args, err)
	}
	return out.String(), errb.String(), code
}

func ready(versions ...string) *queueTicket {
	return &queueTicket{status: "To Do", versions: versions}
}

// Keys and an inclusive range both queue, in one invocation. This is the
// operation that was fifteen hand-written REST calls in one evening.
func TestCLIQueueAddQueuesKeysAndRanges(t *testing.T) {
	bin := orionBinary(t)
	f := &fakeQueueJira{tickets: map[string]*queueTicket{
		"OR-100": ready("v0.8.6"),
		"OR-140": ready("v0.8.6"),
		"OR-141": ready("v0.8.6"),
		"OR-142": ready("v0.8.6"),
	}}
	srv := f.server(t)

	out, errOut, code := runQueueCmd(t, bin, srv.URL, queueProject(t),
		"add", "OR-100", "OR-140..OR-142")
	if code != 0 {
		t.Fatalf("expected success, got exit %d: %s%s", code, out, errOut)
	}
	want := "OR-100 add:ORION|OR-140 add:ORION|OR-141 add:ORION|OR-142 add:ORION"
	if got := strings.Join(f.labelWrites, "|"); got != want {
		t.Errorf("wrote %q, want %q -- both ends of the range included", got, want)
	}
	if len(f.transitions) != 0 {
		t.Errorf("queueing a To Do ticket moved its status: %v", f.transitions)
	}
}

// The preview lists what will be queued, what is already queued and what does
// not exist BEFORE the first write -- so a ticket the range swept in is visible
// before it moves, not after.
func TestCLIQueueAddPreviewsBeforeWriting(t *testing.T) {
	bin := orionBinary(t)
	f := &fakeQueueJira{tickets: map[string]*queueTicket{
		"OR-100": ready("v0.8.6"),
		"OR-133": {status: "To Do", labels: []string{"ORION"}, versions: []string{"v0.8.6"}},
	}}
	srv := f.server(t)

	// OR-999 exists in no map entry, so it 404s: a key naming no ticket.
	out, errOut, code := runQueueCmd(t, bin, srv.URL, queueProject(t),
		"add", "OR-100", "OR-133", "OR-999")
	combined := out + errOut
	if code == 0 {
		t.Fatalf("a key naming no ticket exited 0, so a typo in a script would pass "+
			"forever: %s", combined)
	}
	for _, want := range []string{"OR-100", "OR-133", "already", "OR-999", "no such ticket"} {
		if !strings.Contains(combined, want) {
			t.Errorf("the preview never mentions %q: %s", want, combined)
		}
	}
	// "<- ORION" is a line reporting a write that HAPPENED, distinct from the
	// plan header, which also contains the word "queued".
	planAt := strings.Index(out, "plan")
	writeAt := strings.Index(out, "<- ORION")
	if planAt < 0 || writeAt < 0 || planAt > writeAt {
		t.Errorf("the plan was not printed before the first write (plan at %d, first "+
			"write at %d): %s", planAt, writeAt, out)
	}
	if got := strings.Join(f.labelWrites, "|"); got != "OR-100 add:ORION" {
		t.Errorf("wrote %q, want only OR-100: the queued ticket must not be rewritten", got)
	}
}

// Re-running changes nothing and SAYS so. Requeueing a set that is already
// half queued is the normal way this gets used.
func TestCLIQueueAddIsIdempotent(t *testing.T) {
	bin := orionBinary(t)
	f := &fakeQueueJira{tickets: map[string]*queueTicket{
		"OR-100": {status: "To Do", labels: []string{"ORION"}, versions: []string{"v0.8.6"}},
	}}
	srv := f.server(t)

	out, errOut, code := runQueueCmd(t, bin, srv.URL, queueProject(t), "add", "OR-100")
	if code != 0 {
		t.Fatalf("a re-run failed instead of being a no-op: exit %d: %s%s", code, out, errOut)
	}
	if len(f.labelWrites) != 0 {
		t.Errorf("a re-run wrote %v; it must change nothing", f.labelWrites)
	}
	if !strings.Contains(out, "unchanged") || !strings.Contains(out, "already") {
		t.Errorf("the re-run does not report that nothing changed: %s", out)
	}
}

// A ticket with no fixVersion is refused, naming the missing version -- once
// OR-221 lands it could never be claimed, so labelling it would create the
// silent never-runs state that gate exists to prevent.
func TestCLIQueueAddRefusesATicketWithNoFixVersion(t *testing.T) {
	bin := orionBinary(t)
	f := &fakeQueueJira{tickets: map[string]*queueTicket{"OR-100": ready()}}
	srv := f.server(t)

	out, errOut, code := runQueueCmd(t, bin, srv.URL, queueProject(t), "add", "OR-100")
	combined := out + errOut
	if code == 0 {
		t.Fatalf("a ticket with no milestone was queued: %s", combined)
	}
	if !strings.Contains(combined, "fixVersion") || !strings.Contains(combined, "release add") {
		t.Errorf("the refusal does not name the missing version or how to attach one: %s", combined)
	}
	if len(f.labelWrites) != 0 {
		t.Errorf("a refused add still wrote %v", f.labelWrites)
	}
}

// A claimed ticket is never touched, either way round: the label is the lock
// the whole watcher depends on.
func TestCLIQueueRefusesAClaimedTicket(t *testing.T) {
	for _, claim := range []string{"orion-working", "orion-ci-wait"} {
		for _, verb := range []string{"add", "remove"} {
			t.Run(claim+" "+verb, func(t *testing.T) {
				bin := orionBinary(t)
				f := &fakeQueueJira{tickets: map[string]*queueTicket{
					"OR-100": {status: "In Progress", labels: []string{claim, "ORION"},
						versions: []string{"v0.8.6"}},
				}}
				srv := f.server(t)

				out, errOut, code := runQueueCmd(t, bin, srv.URL, queueProject(t), verb, "OR-100")
				combined := out + errOut
				if code == 0 {
					t.Fatalf("queue %s touched a %s ticket: %s", verb, claim, combined)
				}
				if !strings.Contains(combined, claim) {
					t.Errorf("the refusal does not name the state: %s", combined)
				}
				if len(f.labelWrites) != 0 {
					t.Errorf("a refused %s still wrote %v", verb, f.labelWrites)
				}
			})
		}
	}
}

// A failed ticket is reset to queued in ONE command: the failed label cleared,
// the queue label added and the status returned to To Do. Doing one and not the
// other is the mistake that left a ticket sitting unclaimable.
func TestCLIQueueAddResetsAFailedTicket(t *testing.T) {
	bin := orionBinary(t)
	failed := func() *fakeQueueJira {
		return &fakeQueueJira{tickets: map[string]*queueTicket{
			"OR-217": {status: "In Progress", labels: []string{"orion-failed"},
				versions: []string{"v0.8.6"}},
		}}
	}

	// Without --reset it is refused, and the refusal points at the flag.
	f := failed()
	srv := f.server(t)
	out, errOut, code := runQueueCmd(t, bin, srv.URL, queueProject(t), "add", "OR-217")
	combined := out + errOut
	if code == 0 {
		t.Fatalf("a failed ticket was requeued without --reset: %s", combined)
	}
	if !strings.Contains(combined, "--reset") {
		t.Errorf("the refusal does not point at --reset: %s", combined)
	}
	if len(f.labelWrites) != 0 {
		t.Errorf("a refused requeue still wrote %v", f.labelWrites)
	}

	// With it, both halves happen in the one invocation.
	f = failed()
	srv = f.server(t)
	out, errOut, code = runQueueCmd(t, bin, srv.URL, queueProject(t), "add", "OR-217", "--reset")
	if code != 0 {
		t.Fatalf("--reset failed: exit %d: %s%s", code, out, errOut)
	}
	if len(f.labelWrites) != 1 ||
		!strings.Contains(f.labelWrites[0], "add:ORION") ||
		!strings.Contains(f.labelWrites[0], "remove:orion-failed") {
		t.Errorf("the requeue did not both add %s and clear orion-failed: %v",
			"ORION", f.labelWrites)
	}
	if got := strings.Join(f.transitions, ","); got != "OR-217->To Do" {
		t.Errorf("transitions = %q, want the ticket returned to To Do", got)
	}
}

// Remove means unqueue: the label goes, and NOTHING else. A remove that also
// reverted a status or dropped a milestone would make the cheap operation the
// dangerous one.
func TestCLIQueueRemoveLeavesStatusAndFixVersionAlone(t *testing.T) {
	bin := orionBinary(t)
	f := &fakeQueueJira{tickets: map[string]*queueTicket{
		"OR-140": {status: "In Progress", labels: []string{"ORION"}, versions: []string{"v0.8.6"}},
		"OR-141": {status: "In Progress", labels: []string{"ORION"}, versions: []string{"v0.8.6"}},
	}}
	srv := f.server(t)

	out, errOut, code := runQueueCmd(t, bin, srv.URL, queueProject(t), "remove", "OR-140..OR-141")
	if code != 0 {
		t.Fatalf("expected success, got exit %d: %s%s", code, out, errOut)
	}
	want := "OR-140 remove:ORION|OR-141 remove:ORION"
	if got := strings.Join(f.labelWrites, "|"); got != want {
		t.Errorf("wrote %q, want %q -- only the queue label may move", got, want)
	}
	if len(f.transitions) != 0 {
		t.Errorf("a remove transitioned a ticket: %v", f.transitions)
	}
	for key, tk := range f.tickets {
		if tk.status != "In Progress" || strings.Join(tk.versions, ",") != "v0.8.6" {
			t.Errorf("%s changed beyond its label: status %q, versions %v",
				key, tk.status, tk.versions)
		}
	}
	// --reset has no meaning here, and quietly ignoring it is how an operator
	// comes to believe a remove resets a failed ticket.
	_, resetErr, code := runQueueCmd(t, bin, srv.URL, queueProject(t),
		"remove", "OR-140", "--reset")
	if code == 0 {
		t.Error("`queue remove --reset` was accepted")
	}
	if !strings.Contains(resetErr, "--reset") || !strings.Contains(resetErr, "queue add") {
		t.Errorf("the refusal does not explain --reset belongs to `queue add`: %s", resetErr)
	}
}

// Remove's preview prints before the first write, and skipped tickets are
// reported as "not queued" -- not "already", which is add's word for the same
// idea and would misdescribe a ticket that was never in the queue.
func TestCLIQueueRemovePreviewsBeforeWriting(t *testing.T) {
	bin := orionBinary(t)
	f := &fakeQueueJira{tickets: map[string]*queueTicket{
		"OR-140": {status: "In Progress", labels: []string{"ORION"}, versions: []string{"v0.8.6"}},
		"OR-141": {status: "To Do", versions: []string{"v0.8.6"}}, // not queued
	}}
	srv := f.server(t)

	out, errOut, code := runQueueCmd(t, bin, srv.URL, queueProject(t),
		"remove", "OR-140", "OR-141")
	if code != 0 {
		t.Fatalf("expected success, got exit %d: %s%s", code, out, errOut)
	}
	if !strings.Contains(out, "not queued") {
		t.Errorf("the preview does not report OR-141 as not queued: %s", out)
	}
	planAt := strings.Index(out, "plan")
	writeAt := strings.Index(out, "-> ORION removed")
	if planAt < 0 || writeAt < 0 || planAt > writeAt {
		t.Errorf("the plan was not printed before the first write (plan at %d, first "+
			"write at %d): %s", planAt, writeAt, out)
	}
}

// Re-running a remove over a set that is already unqueued writes nothing and
// says so, the same as add: a remove that errors on a re-run cannot safely be
// retried.
func TestCLIQueueRemoveIsIdempotent(t *testing.T) {
	bin := orionBinary(t)
	f := &fakeQueueJira{tickets: map[string]*queueTicket{
		"OR-100": {status: "To Do", versions: []string{"v0.8.6"}}, // never queued
	}}
	srv := f.server(t)

	out, errOut, code := runQueueCmd(t, bin, srv.URL, queueProject(t), "remove", "OR-100")
	if code != 0 {
		t.Fatalf("a re-run failed instead of being a no-op: exit %d: %s%s", code, out, errOut)
	}
	if len(f.labelWrites) != 0 {
		t.Errorf("a re-run wrote %v; it must change nothing", f.labelWrites)
	}
	if !strings.Contains(out, "unchanged") {
		t.Errorf("the re-run does not report that nothing changed: %s", out)
	}
}

// A blocked ticket in the same invocation as writeable ones does not stop the
// writes that are safe to make: they complete, and the command exits 1
// afterwards because the operator did not get everything they asked for.
func TestCLIQueueAddCompletesWritesThenExitsOneOnBlocked(t *testing.T) {
	bin := orionBinary(t)
	f := &fakeQueueJira{tickets: map[string]*queueTicket{
		"OR-100": ready("v0.8.6"),
		"OR-101": {status: "In Progress", labels: []string{"orion-working"}, versions: []string{"v0.8.6"}},
	}}
	srv := f.server(t)

	out, errOut, code := runQueueCmd(t, bin, srv.URL, queueProject(t), "add", "OR-100", "OR-101")
	if code == 0 {
		t.Fatalf("a run with a blocked ticket exited 0: %s%s", out, errOut)
	}
	if got := strings.Join(f.labelWrites, "|"); got != "OR-100 add:ORION" {
		t.Errorf("the writeable ticket was not written despite the blocked one: %v", f.labelWrites)
	}
	if !strings.Contains(out, "<- ORION") {
		t.Errorf("the successful write was not reported: %s", out)
	}
	if !strings.Contains(out+errOut, "orion-working") {
		t.Errorf("the blocked ticket's reason is missing: %s", out+errOut)
	}
}

// The key and range parser is the SAME code `orion release add` uses, so a
// reversed or cross-project range is refused identically -- and at parse time,
// before the tracker is contacted at all.
func TestCLIQueueUsesTheSameParserAsReleaseAdd(t *testing.T) {
	bin := orionBinary(t)
	for _, tc := range []struct {
		name  string
		token string
		want  string
	}{
		{"reversed", "OR-145..OR-140", "before it starts"},
		{"cross project", "OR-140..FCIA-145", "two projects"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeQueueJira{tickets: map[string]*queueTicket{}}
			srv := f.server(t)

			out, errOut, code := runQueueCmd(t, bin, srv.URL, queueProject(t), "add", tc.token)
			combined := out + errOut
			if code == 0 {
				t.Fatalf("a %s range was accepted: %s", tc.name, combined)
			}
			if !strings.Contains(combined, tc.want) {
				t.Errorf("the refusal does not say %q: %s", tc.want, combined)
			}
			// The same refusal, word for word, from the sibling command.
			_, releaseErr, releaseCode := runAdd(t, bin, srv.URL, "v0.8.6", tc.token)
			if releaseCode == 0 || !strings.Contains(releaseErr, tc.want) {
				t.Errorf("`release add` refuses it differently: %s", releaseErr)
			}
			if len(f.labelWrites) != 0 {
				t.Errorf("a refused range still wrote %v", f.labelWrites)
			}
		})
	}
}

// Bare `orion queue` still reads and writes nothing: adding a verb must not
// turn the command that reports the queue into one that changes it.
func TestCLIQueueBareStillOnlyReads(t *testing.T) {
	bin := orionBinary(t)
	f := &fakeQueueJira{tickets: map[string]*queueTicket{"OR-100": ready("v0.8.6")}}
	srv := f.server(t)

	out, _, _ := runQueueCmd(t, bin, srv.URL, queueProject(t))
	if len(f.labelWrites) != 0 {
		t.Errorf("`orion queue` wrote %v: %s", f.labelWrites, out)
	}
}

// A write that fails is reported and exits non-zero, rather than being
// summarised as part of a success.
func TestCLIQueueAddReportsAFailedWrite(t *testing.T) {
	bin := orionBinary(t)
	f := &fakeQueueJira{
		tickets:     map[string]*queueTicket{"OR-100": ready("v0.8.6")},
		writeStatus: 500,
	}
	srv := f.server(t)

	out, errOut, code := runQueueCmd(t, bin, srv.URL, queueProject(t), "add", "OR-100")
	if code == 0 {
		t.Fatalf("a failed write exited 0: %s", out)
	}
	if !strings.Contains(out+errOut, "OR-100") {
		t.Errorf("the failure does not name the ticket it could not update: %s", out+errOut)
	}
}

// Keys naming another project are refused: the queue label is this
// repository's, so writing it elsewhere puts work in a queue no watcher here
// reads.
func TestCLIQueueAddRefusesAnotherProjectsKeys(t *testing.T) {
	bin := orionBinary(t)
	f := &fakeQueueJira{tickets: map[string]*queueTicket{"FCIA-6": ready("v0.8.6")}}
	srv := f.server(t)

	out, errOut, code := runQueueCmd(t, bin, srv.URL, queueProject(t), "add", "FCIA-6")
	if code == 0 {
		t.Fatalf("FCIA keys were queued from a repository bound to OR: %s", out)
	}
	if !strings.Contains(out+errOut, "FCIA") {
		t.Errorf("the refusal does not name the mismatch: %s", out+errOut)
	}
	if len(f.labelWrites) != 0 {
		t.Errorf("a refused add still wrote %v", f.labelWrites)
	}
}

// No tickets at all shows usage rather than acting on nothing.
func TestCLIQueueAddMissingArgumentsShowUsage(t *testing.T) {
	bin := orionBinary(t)
	f := &fakeQueueJira{tickets: map[string]*queueTicket{}}
	srv := f.server(t)

	for _, verb := range []string{"add", "remove"} {
		out, errOut, code := runQueueCmd(t, bin, srv.URL, queueProject(t), verb)
		if code == 0 {
			t.Errorf("`queue %s` with no keys succeeded: %s", verb, out)
		}
		if !strings.Contains(out+errOut, "queue "+verb) {
			t.Errorf("`queue %s` did not print usage: %s", verb, out+errOut)
		}
	}
}
