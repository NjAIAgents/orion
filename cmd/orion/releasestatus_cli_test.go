package main

// End-to-end coverage of `orion release status` (OR-211): the reconciliation
// it prints and the exit code it returns must differ correctly between an
// UNCOLLATED version (notes still live only as .changelog.d fragments) and a
// COLLATED one (notes already folded into CHANGELOG.md, fragments deleted).
// Before OR-211's fix, a collated version reported every one of its done
// tickets as missing a fragment and exited 1 -- forever, since collation
// deletes the fragment that would have satisfied it.
//
// Same subprocess-against-a-fake-Jira approach as releaseclose_cli_test.go:
// runReleaseStatus calls os.Exit directly and has no injectable Jira client.
//
// No end-to-end suite was attempted: this repository has no non-production
// target configured for /e2e-run, and this coverage is about the command's
// own reconciliation logic, not a deployed environment.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// statusFakeJira answers just enough of the Jira REST surface for `release
// status`: searching issues by fixVersion, and the orphan query for issues
// carrying none.
type statusFakeJira struct {
	issuesByFix map[string][]map[string]any
}

func (f *statusFakeJira) server(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || !strings.HasSuffix(r.URL.Path, "/search/jql") {
			w.WriteHeader(404)
			return
		}
		jql := r.URL.Query().Get("jql")
		var issues []map[string]any
		if strings.Contains(jql, "fixVersion IS EMPTY") {
			issues = nil // no orphans: keep this test about collation, not the sample list
		} else {
			for fix, is := range f.issuesByFix {
				if strings.Contains(jql, `fixVersion = "`+fix+`"`) {
					issues = is
				}
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"issues": issues})
	}))
	t.Cleanup(srv.Close)
	return srv
}

func statusIssueStub(key, statusCategory string) map[string]any {
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

// runStatus invokes `orion release status <args>` as a subprocess, in
// workdir, against a fake Jira server.
func runStatus(t *testing.T, bin, jiraURL, workdir string, args ...string) (stdout string, exitCode int) {
	t.Helper()
	full := append([]string{"release", "status"}, args...)
	cmd := exec.Command(bin, full...)
	cmd.Dir = workdir
	cmd.Env = append(os.Environ(),
		"ORION_HOME="+t.TempDir(),
		"ORION_JIRA_URL="+jiraURL,
		"ORION_JIRA_EMAIL=qa@example.com",
		"ORION_JIRA_TOKEN=t",
	)
	var out strings.Builder
	cmd.Stdout = &out
	cmd.Stderr = &out
	err := cmd.Run()
	code := 0
	if exitErr, ok := err.(*exec.ExitError); ok {
		code = exitErr.ExitCode()
	} else if err != nil {
		t.Fatalf("running orion release status %v: %v", args, err)
	}
	return out.String(), code
}

// statusWorkdir writes a CHANGELOG.md (and optional fragments) into a fresh
// directory, the way runReleaseStatus reads it: relative to os.Getwd().
func statusWorkdir(t *testing.T, changelog string, fragments map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "CHANGELOG.md"), []byte(changelog), 0o644); err != nil {
		t.Fatal(err)
	}
	if len(fragments) > 0 {
		if err := os.MkdirAll(filepath.Join(dir, ".changelog.d"), 0o755); err != nil {
			t.Fatal(err)
		}
		for name, body := range fragments {
			if err := os.WriteFile(filepath.Join(dir, ".changelog.d", name+".md"), []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	return dir
}

const fragBody = "### Added\n\n- Shipped.\n"

// An uncollated version -- CHANGELOG.md has no section for it yet -- still
// demands a fragment for every done ticket, exactly as before OR-211.
func TestCLIStatusUncollatedVersionDemandsFragments(t *testing.T) {
	bin := orionBinary(t)
	f := &statusFakeJira{issuesByFix: map[string][]map[string]any{
		"v0.9.0": {statusIssueStub("OR-1", "Done"), statusIssueStub("OR-2", "Done")},
	}}
	srv := f.server(t)
	dir := statusWorkdir(t, "# Changelog\n\n## Unreleased\n", map[string]string{"OR-1": fragBody})

	out, code := runStatus(t, bin, srv.URL, dir, "v0.9.0", "--project", "OR")

	if code != 1 {
		t.Fatalf("expected exit 1 for a ticket missing its fragment, got %d:\n%s", code, out)
	}
	if !strings.Contains(out, "OR-2") || !strings.Contains(out, "no changelog fragment") {
		t.Errorf("did not report the fragment-less ticket: %s", out)
	}
}

// The same tickets, but the version is now collated into CHANGELOG.md with
// both keys named and its fragments deleted (the ordinary post-collation
// state). This must pass and exit 0 -- the exact regression OR-211 fixes:
// before the fix this reported both tickets as missing a fragment forever.
func TestCLIStatusCollatedVersionFindsNotesInChangelogAndPasses(t *testing.T) {
	bin := orionBinary(t)
	f := &statusFakeJira{issuesByFix: map[string][]map[string]any{
		"v0.9.0": {statusIssueStub("OR-1", "Done"), statusIssueStub("OR-2", "Done")},
	}}
	srv := f.server(t)
	dir := statusWorkdir(t, `# Changelog

## Unreleased

## v0.9.0

### Added

- First change (OR-1).
- Second change (OR-2).
`, nil) // fragments deleted by collation, as the real pipeline does

	out, code := runStatus(t, bin, srv.URL, dir, "v0.9.0", "--project", "OR")

	if code != 0 {
		t.Fatalf("a fully-collated, fully-named version was not clean, exit %d:\n%s", code, out)
	}
	if strings.Contains(out, "no changelog fragment") {
		t.Errorf("a collated version was still asked for fragments collation deletes: %s", out)
	}
	if !strings.Contains(out, "collated into CHANGELOG.md") {
		t.Errorf("did not report the collated-specific reconciled message: %s", out)
	}
}

// Same collated version, but the section does not name one of the two done
// tickets by key -- folded into another entry's bullet, as OR-105 was. This
// must warn, not block: the version still exits 0.
func TestCLIStatusCollatedVersionWarnsWithoutBlockingWhenATicketGoesUnnamed(t *testing.T) {
	bin := orionBinary(t)
	f := &statusFakeJira{issuesByFix: map[string][]map[string]any{
		"v0.9.0": {statusIssueStub("OR-1", "Done"), statusIssueStub("OR-2", "Done")},
	}}
	srv := f.server(t)
	dir := statusWorkdir(t, `# Changelog

## v0.9.0

### Added

- First change (OR-1).
- Something folded in without naming its ticket.
`, nil)

	out, code := runStatus(t, bin, srv.URL, dir, "v0.9.0", "--project", "OR")

	if code != 0 {
		t.Fatalf("a collated version with an unnamed-but-documented ticket blocked, exit %d:\n%s", code, out)
	}
	if !strings.Contains(out, "OR-2") || !strings.Contains(out, "does not name it") {
		t.Errorf("did not report the unnamed ticket as worth a look: %s", out)
	}
}
