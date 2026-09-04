package main

import (
	"strings"
	"testing"
)

type runRow = struct {
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
	Name       string `json:"name"`
}

// A rollup of successes is not a pass while a run for the commit is still
// queued or running, or before any run exists (OR-327). A batch was landed
// on develop two minutes before its run was queued because the rollup held
// only the fast jobs.
func TestARollupIsNotAPassWhileARunIsQueued(t *testing.T) {
	if why := unfinishedRuns(nil); !strings.Contains(why, "no workflow run") {
		t.Errorf("no runs yet must not be a pass: %q", why)
	}
	why := unfinishedRuns([]runRow{{Status: "completed", Conclusion: "success", Name: "Analyze"},
		{Status: "queued", Name: "go"}})
	if !strings.Contains(why, "go (queued)") {
		t.Errorf("a queued run must hold the verdict: %q", why)
	}
	if why := unfinishedRuns([]runRow{{Status: "completed", Conclusion: "success", Name: "go"}}); why != "" {
		t.Errorf("every run completed must clear the hold: %q", why)
	}
}
