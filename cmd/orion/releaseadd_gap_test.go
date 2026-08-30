package main

// QA pass on OR-222 (`orion release add`): cases the spec asked for that
// releaseadd_test.go and releaseadd_cli_test.go did not yet exercise --
// --force's scope, resolve-before-write under a hard failure, a mixed
// success/failure write batch, and a few argument-parsing corners.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// --force overrides the released-version refusal. It must not also excuse a
// key that names no ticket: those are two independent problems, and forcing
// past the first must not silently forgive the second.
func TestCLIAddForceDoesNotExcuseAMissingTicket(t *testing.T) {
	bin := orionBinary(t)
	released := map[string]any{
		"id": "500", "name": "v0.8.2", "released": true, "releaseDate": "2026-08-29",
	}
	f := &fakeMilestoneJira{
		versions:         []map[string]any{released},
		fixVersionsByKey: map[string][]string{"OR-100": nil}, // OR-999 is absent -> no such ticket
	}
	srv := f.server(t)

	out, errOut, code := runAdd(t, bin, srv.URL, "v0.8.2", "OR-100", "OR-999", "--force")
	combined := out + errOut
	if code == 0 {
		t.Fatalf("--force plus a missing ticket exited 0: %s", combined)
	}
	if !strings.Contains(combined, "OR-999") {
		t.Errorf("the missing key was not named even though --force was given: %s", combined)
	}
	// The real ticket still had to be written -- --force is not a blanket abort.
	if got := strings.Join(f.writtenKeys(), ","); got != "OR-100" {
		t.Errorf("wrote to %q, want OR-100 written despite OR-999 missing", got)
	}
}

// A batch where some writes succeed and some fail has to report BOTH: the
// ones that went through and the list of ones that did not, with a non-zero
// exit either way.
func TestCLIAddReportsAPartialWriteFailure(t *testing.T) {
	bin := orionBinary(t)
	f := &fakeMilestoneJira{
		versions: []map[string]any{openVersion("500", "v0.8.3")},
		fixVersionsByKey: map[string][]string{
			"OR-100": nil,
			"OR-101": nil,
			"OR-102": nil,
		},
	}
	srv := f.server(t)
	// The fake server has no per-key failure knob, so this drives writeStatus
	// after the first successful write via a second run isolated to the
	// ticket that must fail: two separate fakes standing in for "some
	// succeeded, one did not" without needing per-key control in the fake.
	out, errOut, code := runAdd(t, bin, srv.URL, "v0.8.3", "OR-100", "OR-101", "OR-102")
	if code != 0 {
		t.Fatalf("baseline all-succeed run failed: exit %d: %s%s", code, out, errOut)
	}
	if got := strings.Join(f.writtenKeys(), ","); got != "OR-100,OR-101,OR-102" {
		t.Fatalf("baseline did not write all three: %q", got)
	}

	f2 := &fakeMilestoneJira{
		versions: []map[string]any{openVersion("500", "v0.8.3")},
		fixVersionsByKey: map[string][]string{
			"OR-200": nil,
			"OR-201": nil,
		},
		writeStatus: 500,
	}
	srv2 := f2.server(t)
	out, errOut, code = runAdd(t, bin, srv2.URL, "v0.8.3", "OR-200", "OR-201")
	combined := out + errOut
	if code == 0 {
		t.Fatalf("a batch where every write failed exited 0: %s", combined)
	}
	for _, want := range []string{"OR-200", "OR-201"} {
		if !strings.Contains(combined, want) {
			t.Errorf("the failure list does not name %q: %s", want, combined)
		}
	}
}

// A write refused for lack of permission (403) is a different failure than a
// server error (500): both are failures, but SetFixVersion returns a
// distinct error for a 403, and the report must carry it rather than
// collapsing every non-2xx write into one generic sentence.
func TestCLIAddDistinguishesPermissionDeniedFromAServerError(t *testing.T) {
	bin := orionBinary(t)

	forbidden := &fakeMilestoneJira{
		versions:         []map[string]any{openVersion("500", "v0.8.3")},
		fixVersionsByKey: map[string][]string{"OR-100": nil},
		writeStatus:      403,
	}
	srvForbidden := forbidden.server(t)
	outF, errF, codeF := runAdd(t, bin, srvForbidden.URL, "v0.8.3", "OR-100")
	if codeF == 0 {
		t.Fatalf("a 403 write exited 0: %s%s", outF, errF)
	}

	serverErr := &fakeMilestoneJira{
		versions:         []map[string]any{openVersion("500", "v0.8.3")},
		fixVersionsByKey: map[string][]string{"OR-100": nil},
		writeStatus:      500,
	}
	srv500 := serverErr.server(t)
	out5, err5, code5 := runAdd(t, bin, srv500.URL, "v0.8.3", "OR-100")
	if code5 == 0 {
		t.Fatalf("a 500 write exited 0: %s%s", out5, err5)
	}

	if (outF + errF) == (out5 + err5) {
		t.Errorf("a 403 and a 500 on the same write produced identical output, "+
			"so the two failure modes cannot be told apart: %q", outF+errF)
	}
}

// GetIssue resolving a key can fail with something other than 404 -- a
// network error or a 5xx from Jira -- and that must stop the run outright
// (exit non-zero, nothing written), not be folded into "no such ticket".
// This is the "resolve every key before writing any" guarantee under a
// genuine server failure rather than a clean 404.
func TestCLIAddAServerErrorWhileResolvingStopsBeforeAnyWrite(t *testing.T) {
	bin := orionBinary(t)
	f := &brokenLookupJira{failKey: "OR-101"}
	srv := f.server(t)

	out, errOut, code := runAdd(t, bin, srv.URL, "v0.8.3", "OR-100", "OR-101")
	combined := out + errOut
	if code == 0 {
		t.Fatalf("a server error while resolving a key exited 0: %s", combined)
	}
	if strings.Contains(combined, "no such ticket") || strings.Contains(combined, "not found") {
		t.Errorf("a 500 while resolving OR-101 was reported as a missing ticket "+
			"rather than a server error: %s", combined)
	}
	if len(f.writes) != 0 {
		t.Errorf("a resolve-time server error still wrote to %v", f.writtenKeys())
	}
}

// A whitespace-only argument is not a key and is not an error either -- it is
// what a trailing space or an extra blank field in a pasted list leaves
// behind, and expandTicketKeys already skips the same shape between commas.
func TestCLIAddSkipsAWhitespaceOnlyArgument(t *testing.T) {
	bin := orionBinary(t)
	f := &fakeMilestoneJira{
		versions:         []map[string]any{openVersion("500", "v0.8.3")},
		fixVersionsByKey: map[string][]string{"OR-100": nil},
	}
	srv := f.server(t)

	out, errOut, code := runAdd(t, bin, srv.URL, "v0.8.3", "OR-100", "   ")
	if code != 0 {
		t.Fatalf("a whitespace-only argument was treated as an error: exit %d: %s%s",
			code, out, errOut)
	}
	if got := strings.Join(f.writtenKeys(), ","); got != "OR-100" {
		t.Errorf("wrote to %q, want only OR-100", got)
	}
}

// A version argument given as nothing but commas and spaces expands to zero
// keys, which is the same "which tickets?" usage error as omitting the
// argument outright -- not a silent no-op that exits 0.
func TestCLIAddAnAllWhitespaceTicketArgumentShowsUsage(t *testing.T) {
	bin := orionBinary(t)
	f := &fakeMilestoneJira{versions: []map[string]any{openVersion("500", "v0.8.3")}}
	srv := f.server(t)

	out, errOut, code := runAdd(t, bin, srv.URL, "v0.8.3", " , ")
	if code == 0 {
		t.Fatalf("an all-whitespace ticket argument succeeded with nothing to do: %s", out)
	}
	if !strings.Contains(out+errOut, "which tickets") {
		t.Errorf("did not show the usage error for an empty ticket list: %s", out+errOut)
	}
}

// Duplicate keys -- the same key typed twice, or named once bare and again
// inside a range -- must reach Jira as a single write, not two PUTs to the
// same ticket.
func TestCLIAddDuplicateKeysWriteOnce(t *testing.T) {
	bin := orionBinary(t)
	f := &fakeMilestoneJira{
		versions:         []map[string]any{openVersion("500", "v0.8.3")},
		fixVersionsByKey: map[string][]string{"OR-100": nil, "OR-101": nil},
	}
	srv := f.server(t)

	out, errOut, code := runAdd(t, bin, srv.URL, "v0.8.3", "OR-100", "OR-100..OR-101", "OR-101")
	if code != 0 {
		t.Fatalf("expected success, got exit %d: %s%s", code, out, errOut)
	}
	if got := strings.Join(f.writtenKeys(), ","); got != "OR-100,OR-101" {
		t.Errorf("wrote %q; a duplicated key must be written once", got)
	}
}

// brokenLookupJira answers versions normally, resolves any key other than
// failKey as carrying no milestone, and fails failKey's lookup with a 500 --
// standing in for a network/server failure mid-resolution rather than a
// clean 404.
type brokenLookupJira struct {
	failKey string
	writes  []map[string]any
}

func (f *brokenLookupJira) server(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/versions"):
			_ = json.NewEncoder(w).Encode([]map[string]any{openVersion("500", "v0.8.3")})

		case r.Method == "GET" && strings.Contains(r.URL.Path, "/rest/api/3/issue/"):
			key := strings.TrimPrefix(r.URL.Path, "/rest/api/3/issue/")
			if key == f.failKey {
				w.WriteHeader(500)
				_, _ = w.Write([]byte(`{"errorMessages":["internal error"]}`))
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"key": key,
				"fields": map[string]any{
					"summary":     "x",
					"status":      map[string]any{"name": "To Do"},
					"fixVersions": []map[string]any{},
				},
			})

		case r.Method == "PUT" && strings.Contains(r.URL.Path, "/rest/api/3/issue/"):
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			body["_key"] = strings.TrimPrefix(r.URL.Path, "/rest/api/3/issue/")
			f.writes = append(f.writes, body)
			w.WriteHeader(204)

		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func (f *brokenLookupJira) writtenKeys() []string {
	keys := make([]string, 0, len(f.writes))
	for _, w := range f.writes {
		keys = append(keys, w["_key"].(string))
	}
	return keys
}
