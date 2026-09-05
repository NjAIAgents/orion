package doctor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/orion-sdlc/orion/internal/config"
)

// OR-297: doctor validates what THIS project's stages name, and says which
// stage named each one. A failure that reads "missing skills/their-review"
// sends someone hunting through a toolkit; naming the stage sends them to the
// one config line they can change.

func foreignToolkit(t *testing.T, ships ...string) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "skills"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, s := range ships {
		dir := filepath.Join(root, "skills", s)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func isolate(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ORION_HOME", t.TempDir())
	t.Setenv("ORION_NJ_AGENTS_DIR", "")
}

func TestDoctorNamesTheStageThatRequiredAMissingSkill(t *testing.T) {
	isolate(t)
	tk := config.Toolkit{
		Repo: "https://github.com/acme/house-skills.git",
		Dir:  foreignToolkit(t, "their-capture"), // ships intent's skill, not review's
		Stages: map[string]string{
			"intent": "/their-capture",
			"review": "/their-review",
		},
	}

	c := checkNJAgents(tk, false)

	if c.grade != fail {
		t.Fatalf("a missing configured skill graded %v, want fail: %+v", c.grade, c)
	}
	if !strings.Contains(c.fix, "skills/their-review (required by the review stage)") {
		t.Errorf("fix = %q, want the missing skill named with the stage that required it", c.fix)
	}
	if strings.Contains(c.fix, "their-capture") {
		t.Errorf("fix = %q, reported a skill the toolkit actually ships", c.fix)
	}
	// The line must not accuse nj-agents of being incomplete when the
	// toolkit under test is somebody else's.
	if !strings.Contains(c.name, "house-skills") {
		t.Errorf("check name = %q, want it to name the configured toolkit", c.name)
	}
}

// The whole point of the story: a healthy foreign toolkit stops failing
// validation for six nj-agents skills the project never invokes.
func TestAForeignToolkitShippingItsOwnSkillsIsHealthy(t *testing.T) {
	isolate(t)
	tk := config.Toolkit{
		Repo:   "https://github.com/acme/house-skills.git",
		Dir:    foreignToolkit(t, "their-capture", "their-review"),
		Stages: map[string]string{"intent": "/their-capture", "review": "/their-review"},
	}

	c := checkNJAgents(tk, false)

	if c.grade != ok {
		t.Errorf("a toolkit shipping every skill it was asked for graded %v: %+v\n%s",
			c.grade, c, c.fix)
	}
}

// An unconfigured machine's doctor line must not move: same name, same
// verdict, same six skills.
func TestTheDefaultToolkitIsStillCheckedAsNJAgents(t *testing.T) {
	isolate(t)
	c := checkNJAgents(config.Toolkit{Dir: filepath.Join(t.TempDir(), "nowhere")}, false)

	if c.name != "nj-agents" {
		t.Errorf("check name = %q, want the unchanged \"nj-agents\"", c.name)
	}
	if c.grade != fail {
		t.Errorf("missing nj-agents graded %v, want fail: %+v", c.grade, c)
	}
}

// --fix must not fetch an arbitrary configured URL unasked. A non-interactive
// doctor answers no, and answering no leaves the machine untouched.
func TestAutoFixDoesNotCloneAForeignToolkitWithoutConsent(t *testing.T) {
	isolate(t)
	home := os.Getenv("ORION_HOME")
	tk := config.Toolkit{
		Repo:   "https://github.com/acme/house-skills.git",
		Stages: map[string]string{"review": "/their-review"},
	}

	c := checkNJAgents(tk, true)

	if c.grade != fail {
		t.Errorf("an unfetched toolkit graded %v, want fail: %+v", c.grade, c)
	}
	if !strings.Contains(c.fix, "git clone https://github.com/acme/house-skills.git") {
		t.Errorf("fix = %q, want the manual git command for the configured repository", c.fix)
	}
	if _, err := os.Stat(filepath.Join(home, "vendor")); err == nil {
		t.Error("doctor --fix created a vendor directory for a URL nobody confirmed")
	}
}
