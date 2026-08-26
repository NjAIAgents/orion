package hook

import (
	"encoding/json"
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
		"git push --force origin feature",
		"git push -f origin feature",
		"git push --force-with-lease origin feature",
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
