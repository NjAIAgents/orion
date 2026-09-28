package main

import "testing"

// OR-570: the batch ref's newest run was the green secret scan; the failed
// tests run behind it is the one whose log the fix agent needs.
func TestTheFixLogComesFromTheNewestFailedRun(t *testing.T) {
	list := []byte(`[{"databaseId":36478507210,"conclusion":"success"},
		{"databaseId":36478506969,"conclusion":"failure"},
		{"databaseId":36472717790,"conclusion":"failure"}]`)
	if got := newestFailedRun(list); got != "36478506969" {
		t.Fatalf("newestFailedRun = %q, want the newest failed run 36478506969", got)
	}
	if got := newestFailedRun([]byte(`[{"databaseId":1,"conclusion":"success"}]`)); got != "" {
		t.Fatalf("no failed run should give no log, got %q", got)
	}
}
