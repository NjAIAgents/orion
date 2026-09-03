package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/orion-sdlc/orion/internal/njagents"
)

func loadJSON(t *testing.T, body string) Config {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "orion.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return Load(dir)
}

// A project pointing Orion at its own skill repository must get every value
// back, unchanged -- that is the whole point of the block.
func TestToolkitBlockRoundTripsThroughLoad(t *testing.T) {
	cfg := loadJSON(t, `{"toolkit":{
		"repo":"https://github.com/github/spec-kit.git",
		"ref":"v2.1.0",
		"dir":"/opt/spec-kit",
		"stages":{"intent":"/specify","spec":"/plan","plan":"/tasks",
		          "decompose":"/breakdown","review":"/analyze"}}}`)
	if err := cfg.Validate(); err != nil {
		t.Fatalf("a valid toolkit block must load: %v", err)
	}
	if cfg.Toolkit.Repo != "https://github.com/github/spec-kit.git" {
		t.Errorf("repo = %q", cfg.Toolkit.Repo)
	}
	if cfg.Toolkit.Ref != "v2.1.0" || cfg.Toolkit.Dir != "/opt/spec-kit" {
		t.Errorf("ref = %q, dir = %q", cfg.Toolkit.Ref, cfg.Toolkit.Dir)
	}
	for stage, want := range map[string]string{
		"intent": "/specify", "spec": "/plan", "plan": "/tasks",
		"decompose": "/breakdown", "review": "/analyze",
	} {
		if got := cfg.Toolkit.Stage(stage); got != want {
			t.Errorf("stage %s = %q, want %q", stage, got, want)
		}
	}
}

// Absent block changes nothing: the toolkit Orion has always used, and the
// delegation spellings still read.
func TestAbsentToolkitBlockKeepsTodaysBehaviour(t *testing.T) {
	cfg := loadJSON(t, `{"delegation":{"nj_agents_dir":"/home/me/nj-agents","nj_agents_ref":"v1.4.0"}}`)
	if cfg.Toolkit.Repo != njagents.RepoURL {
		t.Errorf("repo = %q, want the nj-agents default %q", cfg.Toolkit.Repo, njagents.RepoURL)
	}
	if cfg.Toolkit.Dir != "/home/me/nj-agents" || cfg.Toolkit.Ref != "v1.4.0" {
		t.Errorf("delegation aliases must supply dir/ref: dir=%q ref=%q", cfg.Toolkit.Dir, cfg.Toolkit.Ref)
	}
	if len(cfg.Toolkit.Stages) != 0 {
		t.Errorf("stages = %v, want empty", cfg.Toolkit.Stages)
	}
	if cfg.ToolkitWarning != "" {
		t.Errorf("nothing was superseded, so nothing to warn about: %q", cfg.ToolkitWarning)
	}
}

// An empty toolkit block and a delegation-only config are the same config.
func TestEmptyToolkitAndDelegationOnlyAgree(t *testing.T) {
	delegationOnly := loadJSON(t, `{"delegation":{"nj_agents_dir":"/opt/kit","nj_agents_ref":"main"}}`)
	emptyToolkit := loadJSON(t, `{"toolkit":{},"delegation":{"nj_agents_dir":"/opt/kit","nj_agents_ref":"main"}}`)
	if emptyToolkit.Toolkit.Repo != delegationOnly.Toolkit.Repo ||
		emptyToolkit.Toolkit.Dir != delegationOnly.Toolkit.Dir ||
		emptyToolkit.Toolkit.Ref != delegationOnly.Toolkit.Ref ||
		len(emptyToolkit.Toolkit.Stages) != len(delegationOnly.Toolkit.Stages) {
		t.Errorf("empty toolkit = %+v, delegation-only = %+v",
			emptyToolkit.Toolkit, delegationOnly.Toolkit)
	}
}

// An empty stages map answers "" for every stage. The contract for callers
// is that "" means "use Orion's built-in prompt", asserted where the caller
// lives; here the contract is only that they get "" and not a panic.
func TestUnconfiguredStageIsEmptyString(t *testing.T) {
	cfg := loadJSON(t, `{"toolkit":{"repo":"https://example.com/kit.git"}}`)
	for _, stage := range []string{"intent", "spec", "plan", "build", "review", "pr", "no-such-stage"} {
		if got := cfg.Toolkit.Stage(stage); got != "" {
			t.Errorf("stage %s = %q, want \"\"", stage, got)
		}
	}
}

// toolkit.dir is the spelling a project chose deliberately, so it wins --
// but silently dropping the older key would leave it in the file forever.
func TestToolkitDirWinsAndNamesTheDeprecatedKey(t *testing.T) {
	cfg := loadJSON(t, `{"toolkit":{"dir":"/new/kit","ref":"v2"},
	                     "delegation":{"nj_agents_dir":"/old/kit","nj_agents_ref":"v1"}}`)
	if cfg.Toolkit.Dir != "/new/kit" || cfg.Toolkit.Ref != "v2" {
		t.Fatalf("toolkit.* must win: dir=%q ref=%q", cfg.Toolkit.Dir, cfg.Toolkit.Ref)
	}
	if !strings.Contains(cfg.ToolkitWarning, "delegation.nj_agents_dir") ||
		!strings.Contains(cfg.ToolkitWarning, "delegation.nj_agents_ref") {
		t.Errorf("warning must name the superseded keys, got %q", cfg.ToolkitWarning)
	}
}

// Both spellings of a stage reach the same stage.
func TestStageAliasesResolveToTheSameStage(t *testing.T) {
	for _, pair := range [][2]string{{"design", "spec"}, {"implement", "build"}, {"test", "verify"}, {"ship", "pr"}} {
		alias, canonical := pair[0], pair[1]
		cfg := loadJSON(t, `{"toolkit":{"stages":{"`+alias+`":"/run-it"}}}`)
		if err := cfg.Validate(); err != nil {
			t.Fatalf("%s is a valid spelling: %v", alias, err)
		}
		if got := cfg.Toolkit.Stage(canonical); got != "/run-it" {
			t.Errorf("%s declared, %s reads %q", alias, canonical, got)
		}
		if got := cfg.Toolkit.Stage(alias); got != "/run-it" {
			t.Errorf("%s must also read back by its own spelling, got %q", alias, got)
		}
	}
}

func TestToolkitRejections(t *testing.T) {
	cases := []struct {
		name, body string
		wants      []string
	}{
		{
			"an unknown stage is a typo, not a stage",
			`{"toolkit":{"stages":{"deploy":"/ship-it"}}}`,
			[]string{"deploy", "not a stage"},
		},
		{
			"an order key is sequencing, which is Orion's",
			`{"toolkit":{"order":["spec","plan"],"stages":{"spec":"/plan"}}}`,
			[]string{"decisions/0001", "order"},
		},
		{
			"a sequence key is the same thing under another name",
			`{"toolkit":{"stages":{"spec":"/plan"},"sequence":["spec"]}}`,
			[]string{"decisions/0001"},
		},
		{
			"an array of stages expresses order by position",
			`{"toolkit":{"stages":[{"spec":"/plan"},{"plan":"/tasks"}]}}`,
			[]string{"decisions/0001"},
		},
		{
			"a toolkit list expresses order too",
			`{"toolkit":[{"repo":"https://example.com/a.git"}]}`,
			[]string{"decisions/0001"},
		},
		{
			"two spellings of one stage, two commands, is ambiguous",
			`{"toolkit":{"stages":{"spec":"/plan","design":"/design-it"}}}`,
			[]string{"spec", "design"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := loadJSON(t, tc.body).Validate()
			if err == nil {
				t.Fatal("must be reported as a config error, not ignored")
			}
			for _, want := range tc.wants {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error must name %q, got: %v", want, err)
				}
			}
		})
	}
}

// The same stage twice with the SAME command is not ambiguous -- there is
// nothing to pick between.
func TestSameStageTwiceWithOneCommandIsAccepted(t *testing.T) {
	cfg := loadJSON(t, `{"toolkit":{"stages":{"verify":"/e2e-suite","test":"/e2e-suite"}}}`)
	if err := cfg.Validate(); err != nil {
		t.Fatalf("identical commands are not a collision: %v", err)
	}
	if got := cfg.Toolkit.Stage("verify"); got != "/e2e-suite" {
		t.Errorf("verify = %q", got)
	}
}

// A bad toolkit block must not put every OTHER control back on its default:
// that would hide the real complaint behind a generic decode failure.
func TestABadToolkitBlockDoesNotDegradeTheRestOfTheConfig(t *testing.T) {
	cfg := loadJSON(t, `{"toolkit":{"stages":["spec"]},"limits":{"max_tool_calls":17}}`)
	if cfg.Degraded {
		t.Error("the rest of the file parsed; degrading it hides the toolkit error")
	}
	if cfg.Limits.MaxToolCalls != 17 {
		t.Errorf("max_tool_calls = %d, want the configured 17", cfg.Limits.MaxToolCalls)
	}
	if cfg.Validate() == nil {
		t.Error("the toolkit block is still refused")
	}
}
