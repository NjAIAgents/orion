package collect

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Collect is the step that merges, so it is the last place a branch model
// with no integration branch can be caught before agent work lands on the
// release branch. It must refuse rather than merge and report it.
func TestCollectRefusesToActOnACollapsedBranchModel(t *testing.T) {
	home, source := bound(t)
	if err := os.WriteFile(filepath.Join(source, "orion.json"),
		[]byte(`{"vcs":{"default_branch":"main","work_branch":"main"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	jira := newTracker()

	res, out, c := run(t, home, jira, PR{Verdict: VerdictMerged, URL: "u"}, Options{})

	if res[0].Err == nil {
		t.Fatal("a merge into the release branch must be refused, not reported")
	}
	if res[0].Changed {
		t.Error("nothing may be treated as landed")
	}
	if len(jira.transitions) != 0 || c.refreshed != 0 || c.pruned != 0 {
		t.Errorf("nothing should have moved: %+v %+v", jira, c)
	}
	if !strings.Contains(out, "work_branch") || !strings.Contains(out, "orion init") {
		t.Errorf("the refusal must name the setting and the remedy:\n%s", out)
	}
}

// The terminal line, not only the Slack message. "OR-99 merged" says what
// happened but not where, which is the half that was wrong.
func TestTheTerminalMergeLineNamesTheRoleAndTheBranch(t *testing.T) {
	home, _ := bound(t)
	_, out, _ := run(t, home, newTracker(),
		PR{Verdict: VerdictMerged, URL: "u", BaseRef: "develop"}, Options{})

	if !strings.Contains(out, "merged into the integration branch develop") {
		t.Errorf("the merge line must say where the work landed and what that branch is for:\n%s", out)
	}
}

// "The work is on `main`" was a true sentence that read as routine, which
// is exactly why a repository merging agent work into its release branch
// looked healthy for several releases. Naming the ROLE as well as the name
// makes the same fact impossible to skim past -- and reads correctly in a
// project whose branches are called something else entirely.
func TestTheMergedMessageNamesTheBranchByRole(t *testing.T) {
	_, body := msgMerged("OR-99", PR{URL: "https://pr/1"}, "/repo", true,
		"fetched and fast-forwarded develop", "develop", "main")

	if !strings.Contains(body, "integration branch `develop`") {
		t.Errorf("a merge into the integration branch must say so:\n%s", body)
	}

	// The misconfigured case: work_branch and default_branch collapsed. The
	// message must make that obvious rather than plausible.
	_, body = msgMerged("OR-99", PR{URL: "https://pr/1"}, "/repo", true,
		"fetched and fast-forwarded main", "main", "main")

	if !strings.Contains(body, "release branch `main`") {
		t.Errorf("a merge into the release branch must say WHICH branch that is:\n%s", body)
	}
	if strings.Contains(body, "integration branch") {
		t.Errorf("it did not land on an integration branch; there isn't one:\n%s", body)
	}
}

func TestBranchRoleReadsFromTheProjectsOwnNames(t *testing.T) {
	if got := branchRole("integration", "release"); got != "integration branch" {
		t.Errorf("branchRole = %q, want integration branch", got)
	}
	if got := branchRole("release", "release"); got != "release branch" {
		t.Errorf("branchRole = %q, want release branch", got)
	}
	// No release branch configured is not the same as landing on one.
	if got := branchRole("trunk", ""); got != "integration branch" {
		t.Errorf("branchRole = %q, want integration branch", got)
	}
}
