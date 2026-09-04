package main

// End-to-end coverage of `orion release close`, the wiring that
// releaseclose_test.go's unit tests (decideClose, releaseDate) cannot reach:
// the command's own flag parsing, its FindVersion/IssuesInVersion/MarkReleased
// sequencing, its exit codes, and the text it prints. runReleaseClose calls
// os.Exit directly and has no injectable Jira client, so -- same as
// watch_test.go's TestTheBannerIsPrintedBeforeAnyNetworkCall -- there is no
// in-process seam. This drives the compiled binary as a subprocess instead,
// against a fake Jira server, which stays a local integration test: nothing
// here reaches a real Jira instance or a non-production deployment.
//
// No end-to-end suite was attempted: this repository has no non-production
// target configured for /e2e-run, and OR-209's acceptance criteria are about
// this command's own logic, not a deployed environment.

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

// fakeJira answers just enough of the Jira REST surface for `release close`:
// listing versions (FindVersion), searching issues (IssuesInVersion), and
// marking a version released (MarkReleased).
type fakeJira struct {
	versions    []map[string]any
	issuesByFix map[string][]map[string]any
	markCalls   []map[string]any
	markStatus  int // 0 means 200
	versionsErr bool
	searchErr   bool
}

func (f *fakeJira) server(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "GET" && strings.Contains(r.URL.Path, "/versions"):
			if f.versionsErr {
				w.WriteHeader(500)
				_, _ = w.Write([]byte(`{"error":"boom"}`))
				return
			}
			_ = json.NewEncoder(w).Encode(f.versions)

		case r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/search/jql"):
			if f.searchErr {
				w.WriteHeader(500)
				_, _ = w.Write([]byte(`{"error":"boom"}`))
				return
			}
			jql := r.URL.Query().Get("jql")
			var issues []map[string]any
			for fix, is := range f.issuesByFix {
				if strings.Contains(jql, `fixVersion = "`+fix+`"`) {
					issues = is
				}
			}
			resp := map[string]any{"issues": issues}
			_ = json.NewEncoder(w).Encode(resp)

		case r.Method == "PUT" && strings.Contains(r.URL.Path, "/rest/api/3/version/"):
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			f.markCalls = append(f.markCalls, body)
			if f.markStatus != 0 {
				w.WriteHeader(f.markStatus)
				_, _ = w.Write([]byte(`{"error":"boom"}`))
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})

		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func issueStub(key, statusCategory string) map[string]any {
	return map[string]any{
		"key": key,
		"fields": map[string]any{
			"summary": "x",
			"status": map[string]any{
				"name":           statusCategory,
				"statusCategory": map[string]any{"key": statusCategory},
			},
		},
	}
}

// orionBinary compiles `orion` once for the whole test binary run and
// returns its path.
func orionBinary(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "orion")
	cmd := testproc.Command(t, "go", "build", "-o", bin, ".")
	cmd.Dir = "."
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("building orion for the subprocess test: %v\n%s", err, out)
	}
	return bin
}

// runClose invokes `orion release close <args>` as a subprocess against a
// fake Jira server, isolated from any real Orion home or registry.
func runClose(t *testing.T, bin, jiraURL, workdir string, args ...string) (stdout, stderr string, exitCode int) {
	t.Helper()
	full := append([]string{"release", "close"}, args...)
	cmd := testproc.Command(t, bin, full...)
	cmd.Dir = workdir
	cmd.Env = append(os.Environ(),
		"ORION_HOME="+t.TempDir(),
		"ORION_JIRA_URL="+jiraURL,
		"ORION_JIRA_EMAIL=qa@example.com",
		"ORION_JIRA_TOKEN=t",
	)
	var out, errb strings.Builder
	cmd.Stdout = &out
	cmd.Stderr = &errb
	err := cmd.Run()
	code := 0
	if exitErr, ok := err.(*exec.ExitError); ok {
		code = exitErr.ExitCode()
	} else if err != nil {
		t.Fatalf("running orion release close %v: %v", args, err)
	}
	return out.String(), errb.String(), code
}

// gitRepoWithTag makes an isolated repo with one commit tagged `name`, dated
// `date`, so releaseDate's tag-derived path (cases 6, 22) can be exercised
// through the real command instead of only the extracted function.
func gitRepoWithTag(t *testing.T, name, date string) string {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_DATE="+date+"T12:00:00",
			"GIT_COMMITTER_DATE="+date+"T12:00:00",
			"GIT_AUTHOR_NAME=qa", "GIT_AUTHOR_EMAIL=qa@example.com",
			"GIT_COMMITTER_NAME=qa", "GIT_COMMITTER_EMAIL=qa@example.com",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q")
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "f.txt")
	run("commit", "-q", "-m", "seed")
	run("tag", name)
	return dir
}

// Case 1: closing a complete, open version marks it released and reports
// version, project, date, ticket count and id.
func TestCLICloseCompleteVersionReportsWhatItClosed(t *testing.T) {
	bin := orionBinary(t)
	f := &fakeJira{
		versions: []map[string]any{{"id": "500", "name": "v1.0.0", "released": false}},
		issuesByFix: map[string][]map[string]any{
			"v1.0.0": {issueStub("OR-1", "done")},
		},
	}
	srv := f.server(t)
	dir := gitRepoWithTag(t, "v1.0.0", "2026-08-29")

	out, _, code := runClose(t, bin, srv.URL, dir, "v1.0.0", "--project", "OR")
	if code != 0 {
		t.Fatalf("expected success, got exit %d, stdout=%s", code, out)
	}
	for _, want := range []string{"v1.0.0", "OR", "2026-08-29", "1 ticket", "500"} {
		if !strings.Contains(out, want) {
			t.Errorf("close output missing %q: %s", want, out)
		}
	}
	if len(f.markCalls) != 1 {
		t.Fatalf("expected exactly one MarkReleased call, got %d", len(f.markCalls))
	}
	if f.markCalls[0]["releaseDate"] != "2026-08-29" {
		t.Errorf("MarkReleased was not sent the tag's own date: %v", f.markCalls[0])
	}
}

// Case 2 / 14 / 15: an already-released version is reported, not acted on --
// even when it still carries unfinished tickets and --force is passed.
func TestCLIAlreadyReleasedIsReportedNotActedOn(t *testing.T) {
	bin := orionBinary(t)
	f := &fakeJira{
		versions: []map[string]any{{
			"id": "500", "name": "v1.0.0", "released": true, "releaseDate": "2026-08-01",
		}},
		issuesByFix: map[string][]map[string]any{
			"v1.0.0": {issueStub("OR-1", "new")},
		},
	}
	srv := f.server(t)
	dir := gitRepoWithTag(t, "v1.0.0", "2026-08-29")

	out, _, code := runClose(t, bin, srv.URL, dir, "v1.0.0", "--project", "OR", "--force")
	if code != 0 {
		t.Fatalf("expected success (a no-op), got exit %d: %s", code, out)
	}
	if !strings.Contains(out, "already") {
		t.Errorf("did not report the version as already released: %s", out)
	}
	if len(f.markCalls) != 0 {
		t.Errorf("re-closing an already-released version called MarkReleased %d time(s); "+
			"it must change nothing", len(f.markCalls))
	}
}

// Case 3 / 16: unfinished tickets refuse the close, without --force, and
// every unfinished key is named.
func TestCLIUnfinishedTicketsRefuseAndNameEveryTicket(t *testing.T) {
	bin := orionBinary(t)
	f := &fakeJira{
		versions: []map[string]any{{"id": "500", "name": "v1.0.0", "released": false}},
		issuesByFix: map[string][]map[string]any{
			"v1.0.0": {issueStub("OR-1", "new"), issueStub("OR-2", "new")},
		},
	}
	srv := f.server(t)
	dir := gitRepoWithTag(t, "v1.0.0", "2026-08-29")

	out, errOut, code := runClose(t, bin, srv.URL, dir, "v1.0.0", "--project", "OR")
	if code == 0 {
		t.Fatalf("expected a non-zero exit refusing the close, got 0: %s", out)
	}
	combined := out + errOut
	for _, want := range []string{"OR-1", "OR-2"} {
		if !strings.Contains(combined, want) {
			t.Errorf("refusal does not name unfinished ticket %s: %s", want, combined)
		}
	}
	if len(f.markCalls) != 0 {
		t.Error("a refused close still called MarkReleased")
	}
}

// Case 4 / 17: --force closes past unfinished tickets and warns which ones.
func TestCLIForceClosesAndWarnsWhichTicketsItClosedPast(t *testing.T) {
	bin := orionBinary(t)
	f := &fakeJira{
		versions: []map[string]any{{"id": "500", "name": "v1.0.0", "released": false}},
		issuesByFix: map[string][]map[string]any{
			"v1.0.0": {issueStub("OR-1", "new"), issueStub("OR-2", "new")},
		},
	}
	srv := f.server(t)
	dir := gitRepoWithTag(t, "v1.0.0", "2026-08-29")

	out, _, code := runClose(t, bin, srv.URL, dir, "v1.0.0", "--project", "OR", "--force")
	if code != 0 {
		t.Fatalf("expected success under --force, got exit %d: %s", code, out)
	}
	for _, want := range []string{"OR-1", "OR-2"} {
		if !strings.Contains(out, want) {
			t.Errorf("forced-close warning does not name %s: %s", want, out)
		}
	}
	if len(f.markCalls) != 1 {
		t.Errorf("expected MarkReleased to be called once under --force, got %d", len(f.markCalls))
	}
}

// Case 5: an explicit --date overrides the tag's own commit date.
func TestCLIExplicitDateOverridesTheTag(t *testing.T) {
	bin := orionBinary(t)
	f := &fakeJira{
		versions:    []map[string]any{{"id": "500", "name": "v1.0.0", "released": false}},
		issuesByFix: map[string][]map[string]any{"v1.0.0": {}},
	}
	srv := f.server(t)
	dir := gitRepoWithTag(t, "v1.0.0", "2026-08-29")

	out, _, code := runClose(t, bin, srv.URL, dir, "v1.0.0", "--project", "OR", "--date", "2026-09-01")
	if code != 0 {
		t.Fatalf("expected success, got exit %d: %s", code, out)
	}
	if len(f.markCalls) != 1 || f.markCalls[0]["releaseDate"] != "2026-09-01" {
		t.Errorf("--date was not honoured over the tag date: %v", f.markCalls)
	}
}

// Case 7 / 22: with no --date and no matching tag (or a tag whose date the
// stub can't produce, e.g. a name with no tag at all), the close falls back
// to today rather than blocking.
func TestCLINoDateAndNoTagFallsBackToToday(t *testing.T) {
	bin := orionBinary(t)
	f := &fakeJira{
		versions:    []map[string]any{{"id": "500", "name": "v9.9.9", "released": false}},
		issuesByFix: map[string][]map[string]any{"v9.9.9": {}},
	}
	srv := f.server(t)
	// A repo that exists but has no tag named v9.9.9 at all.
	dir := gitRepoWithTag(t, "unrelated-tag", "2026-08-29")

	out, _, code := runClose(t, bin, srv.URL, dir, "v9.9.9", "--project", "OR")
	if code != 0 {
		t.Fatalf("a missing tag date must fall back to today, not fail: exit %d: %s", code, out)
	}
	if len(f.markCalls) != 1 {
		t.Fatalf("expected one MarkReleased call, got %d", len(f.markCalls))
	}
	got, _ := f.markCalls[0]["releaseDate"].(string)
	if got == "" {
		t.Error("MarkReleased was not sent any releaseDate at all")
	}
}

// Case 8: a malformed --date is rejected before anything reaches Jira.
func TestCLIMalformedDateIsRejected(t *testing.T) {
	bin := orionBinary(t)
	f := &fakeJira{
		versions:    []map[string]any{{"id": "500", "name": "v1.0.0", "released": false}},
		issuesByFix: map[string][]map[string]any{"v1.0.0": {}},
	}
	srv := f.server(t)
	dir := gitRepoWithTag(t, "v1.0.0", "2026-08-29")

	out, errOut, code := runClose(t, bin, srv.URL, dir, "v1.0.0", "--project", "OR", "--date", "29-08-2026")
	if code == 0 {
		t.Fatalf("a malformed --date was accepted: %s", out)
	}
	if len(f.markCalls) != 0 {
		t.Error("MarkReleased was called despite a malformed --date")
	}
	if !strings.Contains(out+errOut, "29-08-2026") {
		t.Errorf("the validation error does not name the bad value: %s", out+errOut)
	}
}

// Case 9: a version name that doesn't exist is rejected, and the message
// points at `release list` rather than a bare "not found".
func TestCLIUnknownVersionSuggestsReleaseList(t *testing.T) {
	bin := orionBinary(t)
	f := &fakeJira{versions: []map[string]any{{"id": "500", "name": "v1.0.0"}}}
	srv := f.server(t)
	dir := gitRepoWithTag(t, "v1.0.0", "2026-08-29")

	out, errOut, code := runClose(t, bin, srv.URL, dir, "v9.9.9", "--project", "OR")
	if code == 0 {
		t.Fatalf("closing an unknown version succeeded: %s", out)
	}
	combined := out + errOut
	if !strings.Contains(combined, "v9.9.9") {
		t.Errorf("error does not name the version that was not found: %s", combined)
	}
	if !strings.Contains(combined, "release list") {
		t.Errorf("error does not point at `release list`: %s", combined)
	}
}

// Case 10: a missing version name argument shows a usage message rather than
// panicking or acting on a zero-value.
func TestCLIMissingVersionNameShowsUsage(t *testing.T) {
	bin := orionBinary(t)
	f := &fakeJira{}
	srv := f.server(t)
	dir := t.TempDir()

	out, errOut, code := runClose(t, bin, srv.URL, dir, "--project", "OR")
	if code == 0 {
		t.Fatalf("`release close` with no version name succeeded: %s", out)
	}
	if !strings.Contains(out+errOut, "release close") {
		t.Errorf("missing-argument message doesn't look like usage: %s", out+errOut)
	}
}

// Case 11: case-exact resolution -- V0.8.1 must not match v0.8.1.
func TestCLIVersionResolutionIsCaseExact(t *testing.T) {
	bin := orionBinary(t)
	f := &fakeJira{versions: []map[string]any{{"id": "500", "name": "v0.8.1"}}}
	srv := f.server(t)
	dir := t.TempDir()

	out, errOut, code := runClose(t, bin, srv.URL, dir, "V0.8.1", "--project", "OR")
	if code == 0 {
		t.Fatalf("V0.8.1 resolved to v0.8.1, a different milestone: %s", out)
	}
	if !strings.Contains(out+errOut, "V0.8.1") {
		t.Errorf("error does not name the exact (unmatched) input: %s", out+errOut)
	}
}

// Case 12: prefix resolution -- v0.8 must not match v0.8.1.
func TestCLIVersionResolutionIsNotPrefixMatched(t *testing.T) {
	bin := orionBinary(t)
	f := &fakeJira{versions: []map[string]any{{"id": "500", "name": "v0.8.1"}}}
	srv := f.server(t)
	dir := t.TempDir()

	out, errOut, code := runClose(t, bin, srv.URL, dir, "v0.8", "--project", "OR")
	if code == 0 {
		t.Fatalf("v0.8 resolved to v0.8.1 by prefix: %s", out)
	}
	if !strings.Contains(out+errOut, "v0.8") {
		t.Errorf("error does not name the requested (unmatched) version: %s", out+errOut)
	}
}

// Case 13: --project resolves the version against that project specifically,
// not whatever the registry would have defaulted to.
func TestCLIProjectFlagScopesTheLookup(t *testing.T) {
	bin := orionBinary(t)
	// The version exists, but the fake server only serves it for project OR;
	// asking under a different project must not find it.
	f := &fakeJira{versions: []map[string]any{{"id": "500", "name": "v1.0.0"}}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" && strings.Contains(r.URL.Path, "/versions") {
			if strings.Contains(r.URL.Path, "/project/OR/") {
				_ = json.NewEncoder(w).Encode(f.versions)
				return
			}
			_ = json.NewEncoder(w).Encode([]map[string]any{})
			return
		}
		w.WriteHeader(404)
	}))
	t.Cleanup(srv.Close)
	dir := t.TempDir()

	// Wrong project: must not find the version that only exists under OR.
	out, errOut, code := runClose(t, bin, srv.URL, dir, "v1.0.0", "--project", "OTHER")
	if code == 0 {
		t.Fatalf("found v1.0.0 under the wrong project: %s", out)
	}
	if !strings.Contains(out+errOut, "OTHER") {
		t.Errorf("error does not name the project actually queried: %s", out+errOut)
	}
}

// Case 18: Jira not being configured (no ORION_JIRA_URL etc.) fails the
// close rather than proceeding with a broken client.
func TestCLIFailsWhenJiraIsNotConfigured(t *testing.T) {
	bin := orionBinary(t)
	cmd := testproc.Command(t, bin, "release", "close", "v1.0.0", "--project", "OR")
	cmd.Dir = t.TempDir()
	cmd.Env = append(os.Environ(), "ORION_HOME="+t.TempDir())
	// Deliberately no ORION_JIRA_* vars: strip any that leaked from the
	// developer's own shell so the "not configured" path is genuinely hit.
	filtered := cmd.Env[:0]
	for _, e := range cmd.Env {
		if !strings.HasPrefix(e, "ORION_JIRA_") {
			filtered = append(filtered, e)
		}
	}
	cmd.Env = filtered
	var out, errOut strings.Builder
	cmd.Stdout = &out
	cmd.Stderr = &errOut
	err := cmd.Run()
	if err == nil {
		t.Fatalf("closing without Jira configured succeeded: %s", out.String())
	}
	if !strings.Contains(out.String()+errOut.String(), "Jira is not configured") {
		t.Errorf("failure does not explain that Jira is not configured: %s", out.String()+errOut.String())
	}
}

// Case 19: FindVersion's underlying lookup (ListVersions) failing fails the
// close with the underlying error, rather than treating it as not-found.
func TestCLIFailsWhenVersionLookupErrors(t *testing.T) {
	bin := orionBinary(t)
	f := &fakeJira{versionsErr: true}
	srv := f.server(t)
	dir := t.TempDir()

	out, errOut, code := runClose(t, bin, srv.URL, dir, "v1.0.0", "--project", "OR")
	if code == 0 {
		t.Fatalf("expected failure when the version listing errors, got success: %s", out)
	}
	if strings.Contains(out+errOut, "has no version named") {
		t.Error("a lookup error was reported as the version simply not existing")
	}
}

// Case 20: IssuesInVersion failing fails the close.
func TestCLIFailsWhenIssueRetrievalErrors(t *testing.T) {
	bin := orionBinary(t)
	f := &fakeJira{
		versions:  []map[string]any{{"id": "500", "name": "v1.0.0", "released": false}},
		searchErr: true,
	}
	srv := f.server(t)
	dir := t.TempDir()

	out, _, code := runClose(t, bin, srv.URL, dir, "v1.0.0", "--project", "OR")
	if code == 0 {
		t.Fatalf("expected failure when issue retrieval errors, got success: %s", out)
	}
	if len(f.markCalls) != 0 {
		t.Error("MarkReleased was called even though issue retrieval failed")
	}
}

// Case 21: the MarkReleased API call itself failing fails the close, even
// though the version was found and was eligible to close.
func TestCLIFailsWhenMarkReleasedErrors(t *testing.T) {
	bin := orionBinary(t)
	f := &fakeJira{
		versions:    []map[string]any{{"id": "500", "name": "v1.0.0", "released": false}},
		issuesByFix: map[string][]map[string]any{"v1.0.0": {}},
		markStatus:  500,
	}
	srv := f.server(t)
	dir := gitRepoWithTag(t, "v1.0.0", "2026-08-29")

	out, _, code := runClose(t, bin, srv.URL, dir, "v1.0.0", "--project", "OR")
	if code == 0 {
		t.Fatalf("expected failure when MarkReleased errors, got success: %s", out)
	}
	if strings.Contains(out, "closed") {
		t.Errorf("reported the version as closed despite MarkReleased failing: %s", out)
	}
}
