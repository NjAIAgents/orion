package config

import (
	"os"
	"path/filepath"
	"testing"
)

// Absent and false mean different things for the QA stage. Verification a
// project never asked to switch off must not be silently missing, so absent
// runs it; an explicit false is a project declining the spend.
func TestQAIsOnUnlessAProjectSaysOtherwise(t *testing.T) {
	dir := t.TempDir()
	write := func(body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, "orion.json"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	write(`{"version":1}`)
	if cfg := Load(dir); !cfg.QA.On() {
		t.Error("a config that says nothing about QA must still run it")
	}
	write(`{"qa":{"enabled":false}}`)
	if cfg := Load(dir); cfg.QA.On() {
		t.Error("an explicit false did not switch the stage off")
	}

	// Zero rounds in JSON is indistinguishable from absent, and no rounds at
	// all would mean the first finding escalates with nobody having tried to
	// fix it -- so zero restores the default rather than removing the loop.
	write(`{"qa":{"max_rounds":0}}`)
	if got, want := Load(dir).QA.Rounds(), Defaults().QA.Rounds(); got != want {
		t.Errorf("max_rounds 0 gave %d rounds, want the default %d", got, want)
	}
	write(`{"qa":{"max_rounds":4}}`)
	if got := Load(dir).QA.Rounds(); got != 4 {
		t.Errorf("max_rounds = %d, want the configured 4", got)
	}
}
