package doctor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/orion-sdlc/orion/internal/config"
	"github.com/orion-sdlc/orion/internal/toolkit"
)

// OR-297: dedicated checkNJAgents coverage for the assigned case list.
// foreignToolkit/isolate are shared with toolkit_test.go in this package.
//
// Two cases from the assignment are not exercised here as literal
// checkNJAgents scenarios, and that is deliberate rather than an omission:
//
//   - "--fix asks before cloning foreign repo": checkNJAgents hardcodes
//     toolkit.ConfirmOnStdin, which decides whether to ask by checking
//     whether os.Stdin is a real character device. A go test process has no
//     tty on stdin, so ConfirmOnStdin always answers "no" without prompting
//     -- there is no way, short of attaching a real pty, to observe it
//     actually asking from inside this test binary. What IS testable at
//     this level is that a foreign repo reaches the confirm gate and is
//     declined non-interactively; TestFixDoesNotCloneForeignRepoWithoutTTY
//     below covers exactly that. The "asks" half of the contract is covered
//     at the toolkit.Clone level instead (see clonecommand_cases_test.go /
//     required_test.go), where the Confirm callback is injectable and the
//     "asked" flag is directly observable.
//   - "Incomplete clone is reported as failed with location" is covered
//     below as the general "an incomplete install is reported as failed
//     with its location" case: whether that incompleteness came from a
//     partial git clone or a pre-existing partial checkout, checkNJAgents
//     takes the same len(inst.Missing) > 0 branch and reports the same way.

func TestCheckNJAgentsMissingConfiguredSkillNamesSkillAndStage(t *testing.T) {
	isolate(t)
	tk := config.Toolkit{
		Repo:   "https://github.com/acme/house-skills.git",
		Dir:    foreignToolkit(t), // ships nothing
		Stages: map[string]string{"review": "/their-review"},
	}

	c := checkNJAgents(tk, false)

	if c.grade != fail {
		t.Fatalf("grade = %v, want fail: %+v", c.grade, c)
	}
	if !strings.Contains(c.fix, "skills/their-review (required by the review stage)") {
		t.Errorf("fix = %q, want the missing skill named with the stage that required it", c.fix)
	}
}

func TestCheckNJAgentsDefaultToolkitMissingSkillNamesSkillOnlyNoStage(t *testing.T) {
	isolate(t)
	// Default toolkit (no Stages configured) shipping only some of the
	// built-in six, so at least one is reported missing with no stage to
	// attribute it to.
	tk := config.Toolkit{Dir: foreignToolkit(t, "pre-push-review", "review-secrets")}

	c := checkNJAgents(tk, false)

	if c.grade != fail {
		t.Fatalf("grade = %v, want fail: %+v", c.grade, c)
	}
	if !strings.Contains(c.fix, "skills/review-tests-build") {
		t.Errorf("fix = %q, want it to name the missing default skill", c.fix)
	}
	if strings.Contains(c.fix, "required by") {
		t.Errorf("fix = %q, want no stage annotation: nothing configured named these skills", c.fix)
	}
}

func TestCheckNJAgentsForeignToolkitShippingAllConfiguredSkillsIsHealthy(t *testing.T) {
	isolate(t)
	tk := config.Toolkit{
		Repo:   "https://github.com/acme/house-skills.git",
		Dir:    foreignToolkit(t, "their-review", "their-build"),
		Stages: map[string]string{"review": "/their-review", "build": "/their-build"},
	}

	c := checkNJAgents(tk, false)

	if c.grade != ok {
		t.Errorf("grade = %v, want ok for a toolkit shipping everything it was asked for: %+v", c.grade, c)
	}
}

func TestCheckNJAgentsCheckNameIsNJAgentsForDefaultToolkit(t *testing.T) {
	isolate(t)
	c := checkNJAgents(config.Toolkit{Dir: foreignToolkit(t, "pre-push-review", "review-secrets",
		"review-tests-build", "pr-describe", "pm-plan", "scaffold-project")}, false)

	if c.name != "nj-agents" {
		t.Errorf("name = %q, want %q", c.name, "nj-agents")
	}
}

func TestCheckNJAgentsCheckNameIncludesForeignRepoLeafForNonDefault(t *testing.T) {
	isolate(t)
	tk := config.Toolkit{
		Repo:   "https://github.com/acme/house-skills.git",
		Dir:    foreignToolkit(t, "their-review"),
		Stages: map[string]string{"review": "/their-review"},
	}

	c := checkNJAgents(tk, false)

	if !strings.Contains(c.name, "house-skills") {
		t.Errorf("name = %q, want it to name the foreign repo's leaf %q", c.name, "house-skills")
	}
}

// A non-interactive process (this test binary) has no tty on stdin, so the
// hardcoded toolkit.ConfirmOnStdin declines without prompting -- exactly
// the behavior a real `orion doctor --fix` run in CI must have.
func TestCheckNJAgentsFixDoesNotCloneForeignRepoWithoutTTY(t *testing.T) {
	isolate(t)
	home := os.Getenv("ORION_HOME")
	tk := config.Toolkit{
		Repo:   "https://github.com/acme/house-skills.git",
		Stages: map[string]string{"review": "/their-review"},
	}

	c := checkNJAgents(tk, true)

	if c.grade != fail {
		t.Errorf("grade = %v, want fail: %+v", c.grade, c)
	}
	if !strings.Contains(c.fix, "git clone https://github.com/acme/house-skills.git") {
		t.Errorf("fix = %q, want the manual clone command for the configured repo", c.fix)
	}
	if _, err := os.Stat(filepath.Join(home, "vendor")); err == nil {
		t.Error("a declined foreign clone must leave the machine untouched")
	}
}

// The default repo is a decision Orion already made on the user's behalf,
// so --fix proceeds straight to fetching it -- no confirm gate at all,
// regardless of whether stdin is a tty. An occupied, non-toolkit vendor
// directory forces Clone to fail deterministically right after that gate,
// without ever reaching the network, so this asserts "no gate was checked"
// rather than depending on git or connectivity.
func TestCheckNJAgentsFixProceedsWithoutAskingForDefaultRepo(t *testing.T) {
	isolate(t)
	home := os.Getenv("ORION_HOME")
	if err := os.MkdirAll(toolkit.VendorDir(home), 0o755); err != nil {
		t.Fatal(err)
	}

	c := checkNJAgents(config.Toolkit{}, true)

	if c.grade != fail {
		t.Fatalf("grade = %v, want fail: %+v", c.grade, c)
	}
	if strings.Contains(c.fix, "declined") {
		t.Errorf("fix = %q, the default repo must never be gated behind a confirm", c.fix)
	}
}

// Whether the incompleteness came from a fresh clone or a pre-existing
// partial checkout, checkNJAgents takes the same fail-with-location branch.
func TestCheckNJAgentsIncompleteInstallReportedAsFailedWithLocation(t *testing.T) {
	isolate(t)
	root := foreignToolkit(t, "their-review") // ships one of two configured skills
	tk := config.Toolkit{
		Repo:   "https://github.com/acme/house-skills.git",
		Dir:    root,
		Stages: map[string]string{"review": "/their-review", "build": "/their-build"},
	}

	c := checkNJAgents(tk, false)

	if c.grade != fail {
		t.Fatalf("grade = %v, want fail: %+v", c.grade, c)
	}
	if !strings.Contains(c.detail, root) {
		t.Errorf("detail = %q, want it to name the incomplete checkout's location %q", c.detail, root)
	}
	if !strings.Contains(c.detail, "incomplete at") {
		t.Errorf("detail = %q, want it to say the checkout is incomplete", c.detail)
	}
}
