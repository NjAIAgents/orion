package hook

import (
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/orion-sdlc/orion/internal/config"
)

func bash(cmd string) Input {
	b, _ := json.Marshal(map[string]string{"command": cmd})
	return Input{HookEventName: "PreToolUse", ToolName: "Bash", ToolInput: b}
}

func TestGateProductionDeploy(t *testing.T) {
	cfg := config.Defaults()
	t.Setenv("ORION_RELEASE_APPROVAL", "")
	t.Setenv("RELEASE_APPROVAL", "")

	blocked := []string{
		"./deploy.sh production",
		"kubectl apply -f k8s/production/",
		"helm upgrade myapp ./chart --namespace production",
		"terraform apply -var-file=prod.tfvars",
		"npm run deploy:prod",
		"aws ecs update-service --cluster prod --service api",
		"vercel --prod",
		"fly deploy --app myapp-production",
	}
	for _, c := range blocked {
		t.Run("blocks/"+c, func(t *testing.T) {
			d := Gate(bash(c), cfg)
			if !d.Blocked() {
				t.Fatalf("%q should be blocked without an authorization", c)
			}
			if !strings.Contains(d.Msg, "ORION_RELEASE_APPROVAL") {
				t.Error("block must name the route to approval, not just refuse")
			}
		})
	}

	allowed := []string{
		"./deploy.sh staging",
		"kubectl apply -f k8s/dev/",
		"npm run build",
		"git status",
		"echo 'deploying to production tomorrow'", // no deploy verb executed

		// SEARCHING for the word is not doing the thing. Every one of these
		// was blocked in practice, and the first is how it was found: a grep
		// for the gate's own message could not be run while working ON the
		// gate, which is exactly when it is needed. A tool that only reads
		// cannot deploy anything.
		`grep -rn "production deploy blocked" internal/`,
		`rg "prod deploy" --files-with-matches`,
		`find . -name "*prod-deploy*"`,
		`ls scripts/deploy-prod/`,
		`git log --grep "deploy to prod"`,
		`git diff HEAD~1 -- deploy/production.yaml`,
		`head -50 scripts/deploy-production.sh`,
		`wc -l deploy/prod.tf`,
		`sed -n 1,40p deploy-prod.sh`,
	}

	// The inert list must not become a way THROUGH the gate. A read verb that
	// feeds something which executes is still blocked, because segments are
	// split on the pipe and the executing half is judged on its own.
	stillBlocked := []string{
		`grep -l prod deploy.sh | xargs ./deploy.sh production`,
		`cat deploy.sh && ./deploy.sh production`,
		`ls scripts/ ; kubectl apply -f k8s/production/`,
		`find . -name "*.tf" && terraform apply -var-file=prod.tfvars`,
		// git's WRITE subcommands are not on the read-only list.
		`git log --oneline && npm run deploy:prod`,
	}
	for _, c := range stillBlocked {
		t.Run("still-blocks/"+c, func(t *testing.T) {
			if d := Gate(bash(c), cfg); !d.Blocked() {
				t.Fatalf("%q reached a real deploy through an inert prefix", c)
			}
		})
	}
	for _, c := range allowed {
		t.Run("allows/"+c, func(t *testing.T) {
			if d := Gate(bash(c), cfg); d.Blocked() {
				t.Fatalf("%q should be allowed, got: %s", c, d.Msg)
			}
		})
	}
}

func TestGateProductionDeployWithApproval(t *testing.T) {
	cfg := config.Defaults()
	t.Setenv("ORION_RELEASE_APPROVAL", "CHG-1234")
	if d := Gate(bash("./deploy.sh production"), cfg); d.Blocked() {
		t.Fatalf("an authorized deploy must proceed, got: %s", d.Msg)
	}
}

func TestGatePushProtection(t *testing.T) {
	cfg := config.Defaults()

	// Both long-lived branches are protected. Protecting only main would
	// leave the pull request into develop optional, and an optional gate is not
	// a gate.
	blocked := []string{
		"git push origin main",
		"git push origin HEAD:main",
		"git push origin feature:main",
		"git push origin develop",
		"git push origin HEAD:develop",
		"git push origin orion/thing:develop",
		// Forcing AT a protected branch is still refused -- that is what
		// protection means.
		"git push --force origin main",
		"git push -f origin develop",
		"git push --force-with-lease origin HEAD:main",
		"git push --force origin feature:develop",
	}
	for _, c := range blocked {
		t.Run("blocks/"+c, func(t *testing.T) {
			d := Gate(bash(c), cfg)
			if !d.Blocked() {
				t.Fatalf("%q should be blocked", c)
			}
			if !strings.Contains(d.Msg, "pull request") {
				t.Error("block must point at the PR route")
			}
			if !strings.Contains(d.Msg, "git switch -c") {
				t.Error("block must give the exact command to cut a branch instead")
			}
		})
	}

	allowed := []string{
		"git push -u origin feature/thing",
		"git push -u origin orion/claim-status",
		"git push origin my-branch",
		"git push",
		"git commit -m 'main change'",
		"git log --oneline main",
		"git push origin developer-notes", // near-miss on "develop"
		"git push origin dev-tools",
		// A FORCE PUSH TO A FEATURE BRANCH IS THE AUTHOR'S BUSINESS.
		// main and develop reach their state through a reviewed pull
		// request; every other branch is the work in progress that leads
		// to one, and rebasing or amending it is how a reviewable history
		// gets made. This used to be blocked, and the refusal read "main
		// is protected" whatever branch the command actually named, which
		// sends the reader looking in the wrong place (OR-420).
		"git push --force origin feature",
		"git push -f origin orion/or-347-release-notes",
		"git push --force-with-lease origin orion/thing",
		"git push --force-with-lease=orion/x:abc123 origin orion/x",
	}
	for _, c := range allowed {
		t.Run("allows/"+c, func(t *testing.T) {
			if d := Gate(bash(c), cfg); d.Blocked() {
				t.Fatalf("%q should be allowed, got: %s", c, d.Msg)
			}
		})
	}
}

func TestGateRespectsDisabledControls(t *testing.T) {
	cfg := config.Defaults()
	cfg.Gates.ProductionRequiresAuth = false
	cfg.Gates.BlockDirectPushToDefaultBranch = false
	t.Setenv("ORION_RELEASE_APPROVAL", "")

	for _, c := range []string{"./deploy.sh production", "git push origin main"} {
		if d := Gate(bash(c), cfg); d.Blocked() {
			t.Errorf("%q must be allowed when its gate is disabled", c)
		}
	}
}

func TestGateIgnoresNonPreToolUse(t *testing.T) {
	cfg := config.Defaults()
	in := bash("./deploy.sh production")
	in.HookEventName = "PostToolUse"
	if d := Gate(in, cfg); d.Blocked() {
		t.Error("gate is a PreToolUse control; blocking after the fact is meaningless")
	}
}

// A push with no refspec goes to the branch you are standing on, and the
// command text never says which that is. Standing on a protected branch it
// is still a push at that branch; standing anywhere else it is not (OR-420).
func TestGateReadsTheCurrentBranchForARefspecLessPush(t *testing.T) {
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		c := exec.Command("git", append([]string{"-C", dir}, args...)...)
		c.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=o", "GIT_AUTHOR_EMAIL=o@l",
			"GIT_COMMITTER_NAME=o", "GIT_COMMITTER_EMAIL=o@l")
		if b, err := c.CombinedOutput(); err != nil {
			t.Skipf("git unavailable: %v\n%s", err, b)
		}
	}
	run("init", "-b", "main")
	run("commit", "--allow-empty", "-m", "x")

	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(wd) })
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}

	cfg := config.Defaults()
	forced := "git " + "push --force"
	if d := Gate(bash(forced), cfg); !d.Blocked() {
		t.Error("a bare force push while standing on main was allowed")
	}

	run("checkout", "-q", "-b", "orion/thing")
	if d := Gate(bash(forced), cfg); d.Blocked() {
		t.Errorf("a bare force push on a feature branch was blocked: %s", d.Msg)
	}
}
