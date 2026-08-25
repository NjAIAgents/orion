package hook

import (
	"os"
	"regexp"
	"strings"

	"github.com/orion-sdlc/orion/internal/config"
)

// Gate guards shell commands. It is the deterministic layer behind the
// dhal security skill: the skill makes a violation unlikely, the gate
// makes it impossible.
//
// Wired to PreToolUse on Bash. Everything it blocks is something no
// amount of model persuasion should be able to talk past, so the checks
// are literal string and pattern matching, never a judgement call.
func Gate(in Input, cfg config.Config) {
	if in.HookEventName != "PreToolUse" {
		Allow("")
	}
	cmd := in.Command()
	if strings.TrimSpace(cmd) == "" {
		Allow("")
	}
	low := strings.ToLower(cmd)

	// 1. Production deploys require a named authorization.
	if cfg.Gates.ProductionRequiresAuth && looksLikeProdDeploy(low) {
		if approval() == "" {
			Block("gate: production deploy blocked.\n" +
				"  A production release needs a named authorization. Orion does not grant one.\n" +
				"  Route: ask the release manager to authorize, then re-run with\n" +
				"  ORION_RELEASE_APPROVAL set to the approval reference (change ticket or release ID).\n" +
				"  Do not attempt to deploy by another path.")
		}
	}

	// 2. Never push to the default branch, and never force push anywhere.
	// The whole separation-of-duties story rests on the agent's work
	// arriving as a pull request. A direct push erases it.
	if cfg.Gates.BlockDirectPushToDefaultBranch {
		if reason := badPush(cmd, cfg.VCS.DefaultBranch); reason != "" {
			Block("gate: %s\n"+
				"  Orion's changes reach %s only through a reviewed pull request.\n"+
				"  Push your branch and open a PR instead:\n"+
				"    git push -u origin <branch> && gh pr create --fill",
				reason, cfg.VCS.DefaultBranch)
		}
	}

	// 3. History rewrites on shared refs.
	if reHardReset.MatchString(cmd) && strings.Contains(low, "origin/") {
		Block("gate: refusing to hard-reset onto a remote ref.\n" +
			"  This discards local work irrecoverably. If that is genuinely intended,\n" +
			"  a human should run it.")
	}

	Allow("")
}

func approval() string {
	for _, k := range []string{"ORION_RELEASE_APPROVAL", "RELEASE_APPROVAL"} {
		if v := strings.TrimSpace(os.Getenv(k)); v != "" {
			return v
		}
	}
	return ""
}

// looksLikeProdDeploy matches the deploy vocabularies in common use.
// False positives here cost one env var; false negatives cost a
// production incident, so the list errs wide.
func looksLikeProdDeploy(low string) bool {
	prodWords := []string{"prod", "production"}
	hasProd := false
	for _, w := range prodWords {
		if strings.Contains(low, w) {
			hasProd = true
			break
		}
	}
	if !hasProd {
		return false
	}
	for _, verb := range []string{
		"deploy", "release", "rollout", "promote", "publish",
		"kubectl apply", "kubectl set image", "helm upgrade", "helm install",
		"terraform apply", "serverless deploy", "sls deploy",
		"aws ecs update-service", "aws lambda update-function-code",
		"flyctl deploy", "fly deploy", "vercel --prod", "netlify deploy",
		"gcloud run deploy", "az webapp",
	} {
		if strings.Contains(low, verb) {
			return true
		}
	}
	return false
}

var (
	reForcePush = regexp.MustCompile(`git\s+push\b[^|;&]*(--force\b|--force-with-lease\b|\s-f\b)`)
	reHardReset = regexp.MustCompile(`git\s+reset\s+--hard`)
)

// badPush returns a reason string when the command pushes somewhere it
// should not, or "" when the push is fine.
func badPush(cmd, defaultBranch string) string {
	if !regexp.MustCompile(`\bgit\s+push\b`).MatchString(cmd) {
		return ""
	}
	if reForcePush.MatchString(cmd) {
		return "force push blocked."
	}
	// Explicit refspec naming the default branch, e.g.
	//   git push origin main
	//   git push origin HEAD:main
	//   git push origin feature:main
	db := regexp.QuoteMeta(defaultBranch)
	patterns := []string{
		`\bgit\s+push\b[^|;&]*\s` + db + `\s*($|[|;&])`, // ... origin main
		`\bgit\s+push\b[^|;&]*:` + db + `\b`,            // ... HEAD:main
	}
	for _, p := range patterns {
		if regexp.MustCompile(p).MatchString(cmd) {
			return "direct push to " + defaultBranch + " blocked."
		}
	}
	return ""
}
