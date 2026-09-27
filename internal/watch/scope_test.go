package watch

import (
	"slices"
	"testing"

	"github.com/orion-sdlc/orion/internal/collect"
)

// OR-539: `orion watch LTA` collected every project's tickets, so a
// Continuity batch ran inside the LTA watcher and no LTA ticket landed.
func TestTheWatcherCollectsOnlyItsOwnProjects(t *testing.T) {
	s := &spy{}
	d := s.deps()
	var got []string
	inner := d.Collect
	d.Collect = func(o collect.Options) []collect.Result {
		got = o.Projects
		return inner(o)
	}
	t.Setenv("COLUMNS", "")
	stopping.Store(false)
	o := Options{Once: true, MaxConcurrent: 1, Projects: []string{"LTA"},
		Out: &discard{}, Home: t.TempDir(), Interval: 1}
	_ = Run(o, d)
	if !slices.Equal(got, []string{"LTA"}) {
		t.Errorf("collect was asked for projects %v, want [LTA]", got)
	}
}

type discard struct{}

func (discard) Write(p []byte) (int, error) { return len(p), nil }
