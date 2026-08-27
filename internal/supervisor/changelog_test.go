package supervisor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/orion-sdlc/orion/internal/changelog"
)

func withFragmentDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, changelog.Dir), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

// The whole change: the implementer is told to write ONE file named for the
// ticket, and told not to edit CHANGELOG.md. Two branches in flight both
// editing that file conflict every time, whatever code they touch.
func TestThePromptAsksForAFragmentRatherThanAChangelogEdit(t *testing.T) {
	p := TicketPrompt("OR-113", "s", "d", "u", withFragmentDir(t), nil)

	if !strings.Contains(p, changelog.Dir+"/OR-113.md") {
		t.Errorf("the prompt does not name this ticket's fragment path:\n%s", p)
	}
	if !strings.Contains(p, "Do not edit CHANGELOG.md") {
		t.Errorf("the prompt does not tell the agent to leave CHANGELOG.md alone:\n%s", p)
	}
}

// The valid sections have to be in the prompt, because a name outside them
// fails at collation -- and a fragment that fails the release is worse than
// one written into the wrong section.
func TestThePromptNamesTheValidSections(t *testing.T) {
	p := TicketPrompt("OR-113", "s", "d", "u", withFragmentDir(t), nil)

	for _, sec := range changelog.Sections {
		if !strings.Contains(p, sec) {
			t.Errorf("the prompt does not name the %s section:\n%s", sec, p)
		}
	}
}

// A repository with no fragment directory must not be told to write into one:
// naming a mechanism that does not exist teaches the agent to distrust the
// instruction and go exploring, which is the cost these conditional lines
// exist to avoid.
func TestNoFragmentIsAskedForWhenTheDirectoryIsAbsent(t *testing.T) {
	p := TicketPrompt("OR-113", "s", "d", "u", t.TempDir(), nil)

	if strings.Contains(p, changelog.Dir) {
		t.Errorf("asked for a fragment in a repository that has no %s/:\n%s", changelog.Dir, p)
	}
}

// COMMITS must still start its own section, with or without the changelog
// block in front of it.
func TestCommitsStillStartsOnItsOwnAfterTheChangelogBlock(t *testing.T) {
	p := TicketPrompt("OR-113", "s", "d", "u", withFragmentDir(t), nil)

	if !strings.Contains(p, "\n\nCOMMITS") {
		t.Errorf("COMMITS lost its blank line:\n%s", p)
	}
}
