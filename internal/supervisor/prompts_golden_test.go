package supervisor

import (
	"flag"
	"os"
	"path/filepath"
	"testing"

	"github.com/orion-sdlc/orion/internal/config"
)

// updateGolden rewrites the snapshots instead of asserting against them.
//
//	go test ./internal/supervisor/ -run TestStagePromptGolden -update
//
// Regenerating is a deliberate act with a diff a reviewer can read, which is
// the whole value of the snapshot: a golden file quietly rewritten by the
// change it is meant to police proves nothing.
var updateGolden = flag.Bool("update", false, "rewrite the stage-prompt snapshots")

// goldenStages is every stage stagePrompt builds text for. `ticket` is absent
// on purpose: it errors by design, because inventing a task from the
// workspace idea would be the agent working on something nobody asked for.
var goldenStages = []string{
	"intent", "spec", "plan", "scaffold", "decompose",
	"build", "verify", "review", "pr",
}

// TestStagePromptGolden pins the exact bytes of every stage prompt under a
// configuration that declares no toolkit stages.
//
// Captured BEFORE toolkit.stages was threaded into stagePrompt (OR-298), so
// it is a backward-compatibility proof rather than a blessing of whatever the
// refactor produced. An absent toolkit block is the configuration almost
// every project runs, and decisions/0019 promises it is a zero-change one:
// this is where that promise is checked, byte for byte.
func TestStagePromptGolden(t *testing.T) {
	w := ws(t, "")
	for _, stage := range goldenStages {
		got, err := stagePrompt(w, stage, config.Toolkit{})
		if err != nil {
			t.Errorf("%s: %v", stage, err)
			continue
		}
		path := filepath.Join("testdata", "stageprompt", stage+".golden")
		if *updateGolden {
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
				t.Fatal(err)
			}
			continue
		}
		want, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("%s: %v (regenerate with -update)", stage, err)
		}
		if got != string(want) {
			t.Errorf("the %s prompt changed under an empty toolkit config.\n"+
				"An absent toolkit block must produce the prompt it always did "+
				"(decisions/0019). If the change is intended, regenerate with "+
				"-update so the diff is reviewed.\n--- want ---\n%s\n--- got ---\n%s",
				stage, want, got)
		}
	}
}
