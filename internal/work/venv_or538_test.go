package work

import (
	"strings"
	"testing"

	"github.com/orion-sdlc/orion/internal/ciscaffold"
)

// OR-538: every ticket run prepares the sandbox clone's virtualenv before an
// agent starts, so QA can run the suite in a project `orion plan` created --
// `orion init` was the only thing that ever built it.
func TestEveryTicketRunPreparesTheSandboxVirtualenv(t *testing.T) {
	var dirs []string
	orig := ensureVenvFn
	ensureVenvFn = func(dir string) (ciscaffold.VenvResult, error) {
		dirs = append(dirs, dir)
		return ciscaffold.VenvResult{Action: "created", Path: dir + "/.venv"}, nil
	}
	t.Cleanup(func() { ensureVenvFn = orig })

	home := project(t, qaCfg)
	f := &qaFake{t: t, qaReplies: []string{"QA CLEAN"}}
	var out strings.Builder
	Run(Options{Keys: []string{"FCIA-6"}, Out: &out, Home: home},
		Deps{
			Jira: &fakeJira{}, Supervise: f.run,
			Push:   func(string, string) error { return nil },
			OpenPR: func(string, string, string, string, string) (string, error) { return "https://pr/1", nil },
		})

	if len(dirs) != 1 {
		t.Fatalf("the virtualenv was prepared %d time(s), want once per ticket run", len(dirs))
	}
	if !strings.Contains(out.String(), "sandbox virtualenv created") {
		t.Errorf("a new virtualenv was not reported:\n%s", out.String())
	}
}
