package collect

import (
	"slices"
	"strings"
	"testing"

	"github.com/orion-sdlc/orion/internal/registry"
)

// OR-539: the search covers the projects asked for, and only those.
func TestTheSearchCoversOnlyTheProjectsAskedFor(t *testing.T) {
	home := t.TempDir()
	for _, k := range []string{"LTA", "CONTINUI"} {
		if err := registry.Bind(home, registry.Entry{Key: k, Source: t.TempDir(), Workspace: strings.ToLower(k)}); err != nil {
			t.Fatal(err)
		}
	}
	j := &jqlTracker{}
	if _, err := waiting(j, home, []string{"lta"}); err != nil {
		t.Fatal(err)
	}
	if q := j.queries[0]; !strings.Contains(q, "LTA") || strings.Contains(q, "CONTINUI") {
		t.Errorf("a watcher for LTA searched: %s", q)
	}
	if _, err := waiting(j, home, nil); err != nil {
		t.Fatal(err)
	}
	if q := j.queries[1]; !strings.Contains(q, "LTA") || !strings.Contains(q, "CONTINUI") {
		t.Errorf("an unscoped collect must still sweep every project: %s", q)
	}
}

// OR-539: a mixed pass is split by project, each in first-seen order.
func TestAMixedPassIsSplitByProject(t *testing.T) {
	got := byProject([]string{"CONTINUI-38", "LTA-2", "CONTINUI-45", "LTA-110"})
	want := [][]string{{"CONTINUI-38", "CONTINUI-45"}, {"LTA-2", "LTA-110"}}
	if len(got) != len(want) || !slices.Equal(got[0], want[0]) || !slices.Equal(got[1], want[1]) {
		t.Errorf("byProject = %v, want %v", got, want)
	}
}

// And Run takes that split: each project is reconciled in its own pass, so
// the second project's tickets are not read through the first's workspace.
func TestRunReconcilesEachProjectSeparately(t *testing.T) {
	home := t.TempDir()
	t.Setenv("ORION_HOME", home)
	var passes [][]string
	orig := runPassHook
	runPassHook = func(pass []string) { passes = append(passes, append([]string(nil), pass...)) }
	t.Cleanup(func() { runPassHook = orig })
	_ = Run(Options{Keys: []string{"CONTINUI-38", "LTA-2"}, Home: home, Out: &strings.Builder{}, Unattended: true},
		Deps{Jira: &jqlTracker{}})
	if len(passes) != 2 || !slices.Equal(passes[0], []string{"CONTINUI-38"}) || !slices.Equal(passes[1], []string{"LTA-2"}) {
		t.Errorf("passes = %v, want one per project", passes)
	}
}
