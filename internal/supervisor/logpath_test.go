package supervisor

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/orion-sdlc/orion/internal/agentcfg"
	"testing"
)

// A fan-out's children share a stage and start inside the same second, so a
// log path built from stamp + stage + attempt names ONE file for all of them
// and os.Create truncates it N-1 times. `orion fan` runs every child as stage
// "fan" and `orion explore` runs every question as "explore", so this is the
// common case rather than a corner of it.
//
// The children's own contexts are gone the moment they exit -- their logs are
// all that ever existed of them, which is why losing N-1 of them matters more
// than a tidy filename.
func TestConcurrentRunsOfTheSameStageGetDistinctLogs(t *testing.T) {
	ws := ws(t, "")

	const children = 6
	paths := make([]string, children)
	done := make(chan struct{})
	for i := range paths {
		go func(i int) {
			defer func() { done <- struct{}{} }()
			res, _ := runOnce(ws, "true", "p",
				Options{Stage: "explore", DryRun: true, MaxMinutes: 1, MaxTurns: 1}, 1, &agentcfg.Run{})
			paths[i] = res.LogPath
		}(i)
	}
	for range paths {
		<-done
	}

	seen := map[string]bool{}
	for i, p := range paths {
		if p == "" {
			t.Fatalf("child %d got no log path at all", i)
		}
		if seen[p] {
			t.Fatalf("two children of the same stage were given the same log path %q; "+
				"os.Create truncates, so one of them loses the only account of what it did", p)
		}
		seen[p] = true
	}
}

// The path still says what it is: a reader looking for an explore run's log
// must not have to know the counter to find it.
func TestALogPathStillNamesItsStageAndAttempt(t *testing.T) {
	ws := ws(t, "")
	res, _ := runOnce(ws, "true", "p",
		Options{Stage: "qa-cases", DryRun: true, MaxMinutes: 1, MaxTurns: 1}, 2, &agentcfg.Run{})

	base := filepath.Base(res.LogPath)
	if !strings.Contains(base, "qa-cases") {
		t.Errorf("log %q does not name its stage", base)
	}
	if !strings.Contains(base, "-a2-") {
		t.Errorf("log %q does not name its attempt", base)
	}
	if filepath.Dir(res.LogPath) != ws.LogsDir() {
		t.Errorf("log went to %q, not the workspace's logs dir", filepath.Dir(res.LogPath))
	}
	if _, err := os.Stat(ws.LogsDir()); err != nil {
		t.Fatal(err)
	}
}
