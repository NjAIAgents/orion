package config

import (
	"os"
	"path/filepath"
	"testing"
)

// OR-546: the CI fix loop is on unless a project turns it off.
func TestAutoFixIsOnByDefaultAndAnExplicitOffIsKept(t *testing.T) {
	for name, c := range map[string]struct {
		file string
		want bool
	}{
		"no ci block":    {`{"version": 1}`, true},
		"explicitly off": {`{"version": 1, "ci": {"auto_fix": false}}`, false},
		"explicitly on":  {`{"version": 1, "ci": {"auto_fix": true}}`, true},
		"no orion.json":  {"", true},
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			if c.file != "" {
				if err := os.WriteFile(filepath.Join(dir, "orion.json"), []byte(c.file), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if got := Load(dir).CI.AutoFix; got != c.want {
				t.Errorf("auto_fix = %v, want %v", got, c.want)
			}
		})
	}
}
