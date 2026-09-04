package toolkit

import "testing"

// OR-295 renamed this package from njagents to toolkit and promised the move
// changed no exported VALUE. RequiredSkills and RequiredDocs are already
// pinned to their literals by required_cases_test.go; RepoURL was not --
// every other reference to it compares the constant against itself, so
// editing the URL would have gone unnoticed. Pin the literal here.
//
// This is the default clone source, not a mandatory one: a project may point
// toolkit.repo at its own skill repository. What must not drift is the value
// Orion falls back to when it does not.
func TestRepoURLIsTheNjAgentsDefault(t *testing.T) {
	const want = "https://github.com/navjyotnishant/nj-agents.git"
	if RepoURL != want {
		t.Errorf("RepoURL = %q, want the unchanged nj-agents default %q", RepoURL, want)
	}
}
