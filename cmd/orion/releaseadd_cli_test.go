package main

// End-to-end coverage of `orion release add`, the wiring releaseadd_test.go's
// unit tests (expandTicketKeys, planFixVersion, projectForKeys) cannot reach:
// the command's flag parsing, its FindVersion/GetIssue/SetFixVersion
// sequencing, its exit codes, and the text it prints. runReleaseAdd calls
// os.Exit directly and has no injectable Jira client, so -- same as
// releaseclose_cli_test.go -- this drives the compiled binary as a subprocess
// against a fake Jira server. It stays a local integration test: nothing here
// reaches a real Jira instance.

import (
	"encoding/json"
	"github.com/orion-sdlc/orion/internal/testproc"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// fakeMilestoneJira answers the surface `release add` uses: listing a
// project's versions, fetching one issue by key, and writing its fixVersions.
type fakeMilestoneJira struct {
	versions []map[string]any
	// fixVersionsByKey is every ticket that exists, and the milestone names it
	// carries. A key ABSENT from this map 404s, the way a key naming no ticket
	// does.
	fixVersionsByKey map[string][]string
	writes           []map[string]any // one per PUT, with the key recorded
	writeStatus      int              // 0 means 200
}

func (f *fakeMilestoneJira) server(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/versions"):
			// Only project OR exists, so a lookup under any other key comes
			// back empty rather than answering for every project.
			if !strings.Contains(r.URL.Path, "/project/OR/") {
				_ = json.NewEncoder(w).Encode([]map[string]any{})
				return
			}
			_ = json.NewEncoder(w).Encode(f.versions)

		case r.Method == "GET" && strings.Contains(r.URL.Path, "/rest/api/3/issue/"):
			key := strings.TrimPrefix(r.URL.Path, "/rest/api/3/issue/")
			names, ok := f.fixVersionsByKey[key]
			if !ok {
				w.WriteHeader(404)
				_, _ = w.Write([]byte(`{"errorMessages":["Issue does not exist"]}`))
				return
			}
			versions := make([]map[string]any, 0, len(names))
			for _, n := range names {
				versions = append(versions, map[string]any{"name": n})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"key": key,
				"fields": map[string]any{
					"summary":     "x",
					"status":      map[string]any{"name": "To Do"},
					"fixVersions": versions,
				},
			})

		case r.Method == "PUT" && strings.Contains(r.URL.Path, "/rest/api/3/issue/"):
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			body["_key"] = strings.TrimPrefix(r.URL.Path, "/rest/api/3/issue/")
			f.writes = append(f.writes, body)
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

// writtenKeys is the tickets the run actually wrote to, in order.
func (f *fakeMilestoneJira) writtenKeys() []string {
	keys := make([]string, 0, len(f.writes))
	for _, wr := range f.writes {
		keys = append(keys, wr["_key"].(string))
	}
	return keys
}

// runAdd invokes `orion release add <args>` as a subprocess against a fake
// Jira server, isolated from any real Orion home or registry.
func runAdd(t *testing.T, bin, jiraURL string, args ...string) (stdout, stderr string, exitCode int) {
	t.Helper()
	cmd := testproc.Command(t, bin, append([]string{"release", "add"}, args...)...)
	cmd.Dir = t.TempDir()
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
		t.Fatalf("running orion release add %v: %v", args, err)
	}
	return out.String(), errb.String(), code
}

func openVersion(id, name string) map[string]any {
	return map[string]any{"id": id, "name": name, "released": false}
}

// Bare keys attach, and the project is inferred from the keys with no
// --project given.
func TestCLIAddAttachesBareKeys(t *testing.T) {
	bin := orionBinary(t)
	f := &fakeMilestoneJira{
		versions:         []map[string]any{openVersion("500", "v0.8.3")},
		fixVersionsByKey: map[string][]string{"OR-100": nil, "OR-133": nil},
	}
	srv := f.server(t)

	out, errOut, code := runAdd(t, bin, srv.URL, "v0.8.3", "OR-100", "OR-133")
	if code != 0 {
		t.Fatalf("expected success, got exit %d: %s%s", code, out, errOut)
	}
	if got := strings.Join(f.writtenKeys(), ","); got != "OR-100,OR-133" {
		t.Errorf("wrote to %q, want OR-100,OR-133", got)
	}
	// The write must carry the version ID FindVersion resolved, not a name
	// that could re-resolve to a near-miss on the way to Jira.
	fields := f.writes[0]["fields"].(map[string]any)
	first := fields["fixVersions"].([]any)[0].(map[string]any)
	if first["id"] != "500" {
		t.Errorf("fixVersions was not written by version id: %v", fields["fixVersions"])
	}
}

// An inclusive range expands to every ticket between its ends, both included.
// This is the reason the command exists: thirty-six consecutive tickets meant
// a scripted REST loop before it.
func TestCLIAddExpandsAnInclusiveRange(t *testing.T) {
	bin := orionBinary(t)
	f := &fakeMilestoneJira{
		versions: []map[string]any{openVersion("500", "v0.8.3")},
		fixVersionsByKey: map[string][]string{
			"OR-140": nil, "OR-141": nil, "OR-142": nil, "OR-143": nil,
		},
	}
	srv := f.server(t)

	out, errOut, code := runAdd(t, bin, srv.URL, "v0.8.3", "OR-140..OR-143")
	if code != 0 {
		t.Fatalf("expected success, got exit %d: %s%s", code, out, errOut)
	}
	if got := strings.Join(f.writtenKeys(), ","); got != "OR-140,OR-141,OR-142,OR-143" {
		t.Errorf("the range wrote to %q; both ends must be included", got)
	}
}

// A reversed or cross-project range is refused with the REASON, and nothing is
// written -- the refusal happens at parse time, before Jira is contacted.
func TestCLIAddRefusesABadRangeBeforeWritingAnything(t *testing.T) {
	for _, tc := range []struct {
		name  string
		token string
		want  string
	}{
		{"reversed", "OR-145..OR-140", "before it starts"},
		{"cross project", "OR-140..FCIA-145", "two projects"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bin := orionBinary(t)
			f := &fakeMilestoneJira{versions: []map[string]any{openVersion("500", "v0.8.3")}}
			srv := f.server(t)

			out, errOut, code := runAdd(t, bin, srv.URL, "v0.8.3", tc.token)
			if code == 0 {
				t.Fatalf("a %s range was accepted: %s", tc.name, out)
			}
			if !strings.Contains(out+errOut, tc.want) {
				t.Errorf("the refusal does not say %q: %s", tc.want, out+errOut)
			}
			if len(f.writes) != 0 {
				t.Errorf("a refused range still wrote to %v", f.writtenKeys())
			}
		})
	}
}

// The preview lists added, moved, already-present and not-found BEFORE
// anything is written -- so a range that quietly swept in a ticket the
// operator did not picture is visible before it moves, not after.
func TestCLIAddPreviewsEveryOutcomeBeforeWriting(t *testing.T) {
	bin := orionBinary(t)
	f := &fakeMilestoneJira{
		versions: []map[string]any{openVersion("500", "v0.8.3")},
		fixVersionsByKey: map[string][]string{
			"OR-100": nil,        // add
			"OR-105": {"v0.8.2"}, // move
			"OR-133": {"v0.8.3"}, // already there
		},
	}
	srv := f.server(t)

	// OR-999 exists in no map entry, so it 404s: a key naming no ticket.
	out, errOut, code := runAdd(t, bin, srv.URL, "v0.8.3",
		"OR-100", "OR-105", "OR-133", "OR-999")
	combined := out + errOut
	if code == 0 {
		t.Fatalf("a key naming no ticket exited 0, so a typo in a cron line would "+
			"pass forever: %s", combined)
	}
	for _, want := range []string{"OR-100", "OR-105", "v0.8.2", "OR-133", "OR-999"} {
		if !strings.Contains(combined, want) {
			t.Errorf("the preview never mentions %q: %s", want, combined)
		}
	}
	// The plan has to be readable before the first write lands, or it is a
	// report and not a preview.
	planAt := strings.Index(out, "plan")
	writeAt := strings.Index(out, "added")
	if planAt < 0 || writeAt < 0 || planAt > writeAt {
		t.Errorf("the plan was not printed before the first write (plan at %d, "+
			"first write at %d): %s", planAt, writeAt, out)
	}
	// The two tickets that needed writing were written; the one already on the
	// milestone was not, and neither was the key with no ticket behind it.
	if got := strings.Join(f.writtenKeys(), ","); got != "OR-100,OR-105" {
		t.Errorf("wrote to %q, want only OR-100 and OR-105", got)
	}
}

// The output distinguishes a MOVE from an ADD, and names the milestone the
// ticket left. Moving nine tickets from v0.8.2 to v0.8.3 was exactly this
// operation, and a bare "9 updated" would have hidden which nine left which
// milestone.
func TestCLIAddDistinguishesAMoveFromAnAdd(t *testing.T) {
	bin := orionBinary(t)
	f := &fakeMilestoneJira{
		versions: []map[string]any{openVersion("500", "v0.8.3")},
		fixVersionsByKey: map[string][]string{
			"OR-100": nil,
			"OR-105": {"v0.8.2"},
		},
	}
	srv := f.server(t)

	out, errOut, code := runAdd(t, bin, srv.URL, "v0.8.3", "OR-100", "OR-105")
	if code != 0 {
		t.Fatalf("expected success, got exit %d: %s%s", code, out, errOut)
	}
	if !strings.Contains(out, "added") || !strings.Contains(out, "moved") {
		t.Errorf("the output does not use different words for an add and a move: %s", out)
	}
	// The move line has to name where the ticket came FROM: nothing else
	// reports that the other milestone's contents changed.
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "moved") {
			if !strings.Contains(line, "OR-105") || !strings.Contains(line, "v0.8.2") {
				t.Errorf("the move line does not say which milestone OR-105 left: %q", line)
			}
		}
	}
}

// Re-running changes nothing and SAYS so. A command that errors on a re-run
// cannot be retried, which is the property `release create` already has.
func TestCLIAddIsIdempotent(t *testing.T) {
	bin := orionBinary(t)
	f := &fakeMilestoneJira{
		versions: []map[string]any{openVersion("500", "v0.8.3")},
		fixVersionsByKey: map[string][]string{
			"OR-100": {"v0.8.3"},
			"OR-133": {"v0.8.3"},
		},
	}
	srv := f.server(t)

	out, errOut, code := runAdd(t, bin, srv.URL, "v0.8.3", "OR-100", "OR-133")
	if code != 0 {
		t.Fatalf("a re-run failed instead of being a no-op: exit %d: %s%s", code, out, errOut)
	}
	if len(f.writes) != 0 {
		t.Errorf("a re-run wrote to %v; it must change nothing", f.writtenKeys())
	}
	if !strings.Contains(out, "unchanged") || !strings.Contains(out, "already") {
		t.Errorf("the re-run does not report that nothing changed: %s", out)
	}
}

// A released milestone records what SHIPPED. Adding to it rewrites public
// history, so it is refused -- and --force is the explicit override.
func TestCLIAddRefusesAReleasedVersionWithoutForce(t *testing.T) {
	bin := orionBinary(t)
	released := map[string]any{
		"id": "500", "name": "v0.8.2", "released": true, "releaseDate": "2026-08-29",
	}
	f := &fakeMilestoneJira{
		versions:         []map[string]any{released},
		fixVersionsByKey: map[string][]string{"OR-100": nil},
	}
	srv := f.server(t)

	out, errOut, code := runAdd(t, bin, srv.URL, "v0.8.2", "OR-100")
	if code == 0 {
		t.Fatalf("adding to a released milestone succeeded without an override: %s", out)
	}
	if !strings.Contains(out+errOut, "already released") {
		t.Errorf("the refusal does not say the version is already released: %s", out+errOut)
	}
	if len(f.writes) != 0 {
		t.Errorf("a refused add still wrote to %v", f.writtenKeys())
	}

	// --force is the explicit override the refusal points at.
	out, errOut, code = runAdd(t, bin, srv.URL, "v0.8.2", "OR-100", "--force")
	if code != 0 {
		t.Fatalf("--force did not get past the released milestone: exit %d: %s%s",
			code, out, errOut)
	}
	if got := strings.Join(f.writtenKeys(), ","); got != "OR-100" {
		t.Errorf("--force wrote to %q, want OR-100", got)
	}
}

// A version that does not exist is refused NAMING the versions that do, so the
// operator can tell a typo from a case difference from a milestone nobody
// created.
func TestCLIAddUnknownVersionNamesTheOnesThatExist(t *testing.T) {
	bin := orionBinary(t)
	f := &fakeMilestoneJira{
		versions: []map[string]any{
			openVersion("500", "v0.8.2"),
			openVersion("501", "v0.8.3"),
		},
		fixVersionsByKey: map[string][]string{"OR-100": nil},
	}
	srv := f.server(t)

	out, errOut, code := runAdd(t, bin, srv.URL, "v9.9.9", "OR-100")
	combined := out + errOut
	if code == 0 {
		t.Fatalf("adding to a nonexistent version succeeded: %s", combined)
	}
	for _, want := range []string{"v9.9.9", "v0.8.2", "v0.8.3"} {
		if !strings.Contains(combined, want) {
			t.Errorf("the refusal does not name %q: %s", want, combined)
		}
	}
	if len(f.writes) != 0 {
		t.Errorf("a refused add still wrote to %v", f.writtenKeys())
	}
}

// Version resolution is case-exact, through the same FindVersion `create` and
// `close` use: V0.8.3 must not resolve to v0.8.3.
func TestCLIAddVersionResolutionIsCaseExact(t *testing.T) {
	bin := orionBinary(t)
	f := &fakeMilestoneJira{
		versions:         []map[string]any{openVersion("500", "v0.8.3")},
		fixVersionsByKey: map[string][]string{"OR-100": nil},
	}
	srv := f.server(t)

	out, errOut, code := runAdd(t, bin, srv.URL, "V0.8.3", "OR-100")
	if code == 0 {
		t.Fatalf("V0.8.3 resolved to v0.8.3, a different milestone: %s", out)
	}
	if !strings.Contains(out+errOut, "V0.8.3") {
		t.Errorf("the refusal does not name the exact input: %s", out+errOut)
	}
	if len(f.writes) != 0 {
		t.Errorf("a refused add still wrote to %v", f.writtenKeys())
	}
}

// --project is inferred from the keys, but a flag that CONTRADICTS them is
// refused: resolving the version under one project while writing to tickets in
// another is exactly how a ticket lands on the wrong milestone.
func TestCLIAddRefusesAProjectFlagThatContradictsTheKeys(t *testing.T) {
	bin := orionBinary(t)
	f := &fakeMilestoneJira{
		versions:         []map[string]any{openVersion("500", "v0.8.3")},
		fixVersionsByKey: map[string][]string{"OR-100": nil},
	}
	srv := f.server(t)

	out, errOut, code := runAdd(t, bin, srv.URL, "v0.8.3", "OR-100", "--project", "FCIA")
	if code == 0 {
		t.Fatalf("--project FCIA was accepted alongside OR keys: %s", out)
	}
	if !strings.Contains(out+errOut, "does not match") {
		t.Errorf("the refusal does not explain the contradiction: %s", out+errOut)
	}
	if len(f.writes) != 0 {
		t.Errorf("a refused add still wrote to %v", f.writtenKeys())
	}
}

// A write that fails is reported and exits non-zero, rather than being
// summarised as part of a success.
func TestCLIAddReportsAFailedWrite(t *testing.T) {
	bin := orionBinary(t)
	f := &fakeMilestoneJira{
		versions:         []map[string]any{openVersion("500", "v0.8.3")},
		fixVersionsByKey: map[string][]string{"OR-100": nil},
		writeStatus:      500,
	}
	srv := f.server(t)

	out, errOut, code := runAdd(t, bin, srv.URL, "v0.8.3", "OR-100")
	if code == 0 {
		t.Fatalf("a failed write exited 0: %s", out)
	}
	if !strings.Contains(out+errOut, "OR-100") {
		t.Errorf("the failure does not name the ticket it could not update: %s", out+errOut)
	}
}

// A missing version argument shows usage rather than acting on a zero value.
func TestCLIAddMissingArgumentsShowUsage(t *testing.T) {
	bin := orionBinary(t)
	f := &fakeMilestoneJira{versions: []map[string]any{openVersion("500", "v0.8.3")}}
	srv := f.server(t)

	for _, args := range [][]string{nil, {"v0.8.3"}} {
		out, errOut, code := runAdd(t, bin, srv.URL, args...)
		if code == 0 {
			t.Errorf("`release add %v` succeeded with nothing to do: %s", args, out)
		}
		if !strings.Contains(out+errOut, "release add") {
			t.Errorf("`release add %v` did not print usage: %s", args, out+errOut)
		}
	}
}
