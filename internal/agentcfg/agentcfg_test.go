package agentcfg

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/orion-sdlc/orion/internal/config"
)

// isolate cuts the test off from the machine it runs on. njagents.Discover
// resolves an installed skill's symlink back to its clone root under the
// user's home, so without this a developer's own nj-agents would answer for
// the fixture and the assertions would be about their laptop.
func isolate(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
}

// toolkit writes a minimal nj-agents checkout: what njagents.Validate needs
// to accept it as one, plus the skills and agents a run should end up with.
func toolkit(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "CONVENTIONS.md"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{
		"pre-push-review", "review-secrets", "review-tests-build",
		"pr-describe", "pm-plan", "scaffold-project",
	} {
		dir := filepath.Join(root, "skills", s)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(root, "agents"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "agents", "secrets-reviewer.md"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

func cfgWith(root string, inherit ...string) config.Config {
	cfg := config.Defaults()
	cfg.Toolkit.Dir = root
	cfg.Delegation.InheritOperatorConfig = inherit
	return cfg
}

// The heart of OR-213: a run's capabilities are Orion's decision. The
// directory exists, it holds the toolkit Orion depends on, and the flag that
// removes the operator's MCP servers is passed.
func TestACuratedRunGetsTheToolkitAndNoMCPServers(t *testing.T) {
	requireCuration(t)
	isolate(t)
	home := t.TempDir()

	r, err := For(home, cfgWith(toolkit(t)), "build", "implementer")
	if err != nil {
		t.Fatal(err)
	}

	if r.Inherited {
		t.Fatal("a run with no opt-in must not inherit the operator's configuration")
	}
	if want := filepath.Join(home, DirName); r.Dir != want {
		t.Errorf("Dir = %q, want %q", r.Dir, want)
	}
	// nj-agents still resolves: pre-push-review, pm-plan and pr-describe are
	// the ones Orion actually invokes, and a curated directory that dropped
	// them would break the stages this project delegates.
	for _, skill := range []string{"pre-push-review", "pm-plan", "pr-describe"} {
		link := filepath.Join(r.Dir, "skills", skill, "SKILL.md")
		if _, err := os.Stat(link); err != nil {
			t.Errorf("%s must resolve through the curated directory: %v", skill, err)
		}
	}
	if _, err := os.Stat(filepath.Join(r.Dir, "agents", "secrets-reviewer.md")); err != nil {
		t.Errorf("the toolkit's agents must resolve too: %v", err)
	}
	if got := r.Args(); len(got) != 1 || got[0] != "--strict-mcp-config" {
		t.Errorf("Args() = %v, want --strict-mcp-config so the run gets no MCP servers", got)
	}
	// The fixture isolates HOME and CLAUDE_CONFIG_DIR to empty temp dirs, so
	// there is legitimately no .credentials.json to carry over and OR-239's
	// warning about that fires. It is the one warning this fixture earns;
	// anything else is a real finding and still fails the test.
	for _, w := range r.Warnings {
		if strings.Contains(w, credentialsFile) {
			continue
		}
		t.Errorf("unexpected warning: %s", w)
	}
}

// An operator plugin installed for their own use must not appear in a run.
// The directory IS the control -- a plugin the CLI cannot find cannot be
// loaded however it is enabled -- so what matters is that nothing arrived
// there that Orion did not put there.
func TestTheCuratedDirectoryHoldsNothingOrionDidNotPutThere(t *testing.T) {
	requireCuration(t)
	isolate(t)
	home := t.TempDir()
	operator := t.TempDir()
	for _, d := range []string{"plugins", "skills", "agents", "commands"} {
		if err := os.MkdirAll(filepath.Join(operator, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(operator, "skills", "operators-own.md"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CLAUDE_CONFIG_DIR", operator)

	r, err := For(home, cfgWith(toolkit(t)), "build", "implementer")
	if err != nil {
		t.Fatal(err)
	}

	if _, err := os.Lstat(filepath.Join(r.Dir, "skills", "operators-own.md")); err == nil {
		t.Error("the operator's own skill reached the run")
	}
	if _, err := os.Lstat(filepath.Join(r.Dir, "plugins")); err == nil {
		t.Error("the operator's plugin directory reached the run")
	}
}

// The environment must carry exactly one CLAUDE_CONFIG_DIR. Two entries for
// one name resolve to whichever the C library finds first, which would make
// where the agent's capabilities come from a coin flip.
func TestEnvReplacesAnInheritedConfigDirRatherThanShadowingIt(t *testing.T) {
	r := &Run{Dir: "/orion/agent-config"}

	env := r.Env([]string{"PATH=/bin", "CLAUDE_CONFIG_DIR=/home/op/.claude", "HOME=/home/op"})

	var seen []string
	for _, kv := range env {
		if strings.HasPrefix(kv, "CLAUDE_CONFIG_DIR=") {
			seen = append(seen, kv)
		}
	}
	if len(seen) != 1 || seen[0] != "CLAUDE_CONFIG_DIR=/orion/agent-config" {
		t.Errorf("CLAUDE_CONFIG_DIR entries = %v, want exactly Orion's", seen)
	}
}

// The opt-in is what makes this a decision rather than a wall. It matches a
// stage or an actor, and it is recorded either way.
func TestAnOperatorCanOptOneStageOrOneActorIn(t *testing.T) {
	isolate(t)
	home := t.TempDir()
	root := toolkit(t)

	for _, tc := range []struct {
		name, inherit, stage, actor, want string
	}{
		{"by stage", "review", "review", "implementer", "stage:review"},
		{"by actor", "qa", "verify", "qa", "actor:qa"},
		{"case insensitively", "QA", "verify", "qa", "actor:qa"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, err := For(home, cfgWith(root, tc.inherit), tc.stage, tc.actor)
			if err != nil {
				t.Fatal(err)
			}
			if !r.Inherited || r.OptIn != tc.want {
				t.Fatalf("Run = %+v, want inherited via %q", r, tc.want)
			}
			if r.Dir != "" {
				t.Errorf("an inherited run must not redirect the config dir, got %q", r.Dir)
			}
			if got := r.Args(); len(got) != 0 {
				t.Errorf("Args() = %v, want none: the operator asked for their own MCP servers", got)
			}
			if !strings.Contains(r.Describe(), tc.want) {
				t.Errorf("Describe() = %q, must record the choice", r.Describe())
			}
		})
	}
}

// The default is off. An unrelated stage named in the list changes nothing
// for the one actually running.
func TestAnUnrelatedOptInLeavesTheRunCurated(t *testing.T) {
	requireCuration(t)
	isolate(t)
	r, err := For(t.TempDir(), cfgWith(toolkit(t), "review"), "build", "implementer")
	if err != nil {
		t.Fatal(err)
	}
	if r.Inherited {
		t.Errorf("opting the review stage in must not opt the build stage in: %+v", r)
	}
}

// A skill deleted from the toolkit must stop being offered; a note somebody
// left in the directory by hand must survive. Deleting a file Orion did not
// create is how a tool becomes one nobody can leave anything in.
func TestRebuildingDropsStaleLinksAndKeepsRealFiles(t *testing.T) {
	requireCuration(t)
	isolate(t)
	home := t.TempDir()
	root := toolkit(t)

	if _, err := For(home, cfgWith(root), "build", "implementer"); err != nil {
		t.Fatal(err)
	}
	skills := filepath.Join(home, DirName, "skills")
	note := filepath.Join(skills, "READ-ME-FIRST.txt")
	if err := os.WriteFile(note, []byte("mine"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(root, "skills", "pm-plan")); err != nil {
		t.Fatal(err)
	}

	if _, err := For(home, cfgWith(root), "build", "implementer"); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Lstat(filepath.Join(skills, "pm-plan")); err == nil {
		t.Error("a skill removed from the toolkit is still linked")
	}
	if _, err := os.Stat(note); err != nil {
		t.Errorf("a file Orion did not create was deleted: %v", err)
	}
}

// Missing nj-agents warns and keeps the isolation. Degrading to the
// operator's configuration here would silently restore the whole blast
// radius on the one machine least likely to be watching for it; `orion
// doctor` is what grades the missing toolkit, and it grades it FAIL.
func TestAMissingToolkitWarnsAndStaysCurated(t *testing.T) {
	requireCuration(t)
	isolate(t)
	home := t.TempDir()
	empty := t.TempDir()

	cfg := config.Defaults()
	cfg.Toolkit.Dir = filepath.Join(empty, "nowhere")

	r, err := For(home, cfg, "build", "implementer")
	if err != nil {
		t.Fatal(err)
	}
	if r.Inherited || r.Dir == "" {
		t.Fatalf("a missing toolkit must not hand the run the operator's configuration: %+v", r)
	}
	if len(r.Warnings) == 0 {
		t.Error("a run with no delegated skills must say so")
	}
}

// OR-239. A curated CLAUDE_CONFIG_DIR cannot authenticate on macOS: the CLI
// wants .claude.json INSIDE the directory while the operator's lives outside
// it, and the Keychain is not reached for a non-default directory. v0.8.3
// shipped the curated directory anyway and every supervised run on macOS
// failed at the first call.
//
// The contract is therefore platform-dependent, and the test asserts the
// contract rather than the platform: wherever curation cannot authenticate,
// For MUST inherit deliberately and MUST say so. A silent inherit would be
// the worse failure -- an operator believing their runs are curated when the
// whole plugin surface is in scope.
func TestForInheritsWhenACuratedDirectoryCannotAuthenticate(t *testing.T) {
	r, err := agentcfgFor(t)
	if err != nil {
		t.Fatalf("For() = %v, want a usable run", err)
	}
	if curationAuthenticates() {
		if r.Dir == "" {
			t.Fatal("a platform that CAN authenticate a curated directory must get one")
		}
		return
	}
	if r.Dir != "" {
		t.Errorf("Dir = %q, want empty: a curated directory that cannot log in is not a run", r.Dir)
	}
	if !r.Inherited {
		t.Error("Inherited = false; the operator's configuration was used and the record must say so")
	}
	if r.OptIn != "platform:"+runtime.GOOS {
		t.Errorf("OptIn = %q, want the platform recorded as the reason", r.OptIn)
	}
	if len(r.Warnings) == 0 {
		t.Fatal("no warning: a run that is NOT capability-curated must never be silent about it")
	}
}

func agentcfgFor(t *testing.T) (*Run, error) {
	t.Helper()
	return For(t.TempDir(), config.Defaults(), "implementing", "implementer")
}

// requireCuration skips a test that can only hold where a curated
// CLAUDE_CONFIG_DIR authenticates.
//
// A SKIP rather than a weakened assertion: "the run stays curated" is still
// exactly right on Linux and in CI, and softening it there to accommodate
// macOS would delete the coverage that OR-213 exists for. The platform gap
// is asserted by TestForInheritsWhenACuratedDirectoryCannotAuthenticate
// instead, so neither side of the branch is untested.
func requireCuration(t *testing.T) {
	t.Helper()
	if !curationAuthenticates() {
		t.Skipf("a curated config directory cannot authenticate on %s (OR-239)", runtime.GOOS)
	}
}
