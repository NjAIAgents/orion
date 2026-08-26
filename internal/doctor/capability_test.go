package doctor

import (
	"strings"
	"testing"

	"github.com/orion-sdlc/orion/internal/creds"
)

// The failure this guards against is not a wrong verdict but a wrong REMEDY.
// A stale export shadows a correct config.env; if the message only names the
// variables and says "run orion config", the user edits the file, the file
// still loses to the environment, and nothing changes. The loop has no exit.
func TestJiraCredSourceNamesTheEnvironmentAndHowToClearIt(t *testing.T) {
	home := t.TempDir()
	t.Setenv("ORION_HOME", home)
	if err := creds.Save(home, map[string]string{
		creds.JiraURL:   "https://right.atlassian.net",
		creds.JiraEmail: "right@example.com",
		creds.JiraToken: "stored-token",
	}); err != nil {
		t.Fatal(err)
	}
	t.Setenv(creds.JiraEmail, "stale@example.com")

	src, hint := jiraCredSource()

	if !strings.Contains(src, "environment") {
		t.Errorf("source = %q, must say the environment is in play", src)
	}
	if !strings.Contains(hint, "unset "+creds.JiraEmail) {
		t.Errorf("hint must give the exact unset command, got:\n%s", hint)
	}
	if !strings.Contains(hint, "being ignored") {
		t.Errorf("hint must say the stored value is shadowed, got:\n%s", hint)
	}
	// Sending the user to edit the file is the wrong remedy in this state.
	if strings.Contains(hint, "--only") {
		t.Errorf("must not point at `orion config --only` while the env wins:\n%s", hint)
	}
}

// With nothing exported the file IS the thing to fix, so the remedy flips.
func TestJiraCredSourceFallsBackToTheFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("ORION_HOME", home)
	for _, k := range []string{creds.JiraURL, creds.JiraEmail, creds.JiraToken} {
		t.Setenv(k, "")
	}
	if err := creds.Save(home, map[string]string{creds.JiraToken: "t"}); err != nil {
		t.Fatal(err)
	}

	src, hint := jiraCredSource()

	if !strings.Contains(src, "config file") {
		t.Errorf("source = %q, want the config file", src)
	}
	if strings.Contains(hint, "unset") {
		t.Errorf("nothing is exported; telling the user to unset is noise:\n%s", hint)
	}
	if !strings.Contains(hint, "orion config") {
		t.Errorf("hint should point at orion config, got:\n%s", hint)
	}
}
