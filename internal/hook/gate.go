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
func Gate(in Input, cfg config.Config) Decision {
	if in.HookEventName != "PreToolUse" {
		return Allow("")
	}
	cmd := in.Command()
	if strings.TrimSpace(cmd) == "" {
		return Allow("")
	}
	low := strings.ToLower(cmd)

	// 1. Production deploys require a named authorization.
	if cfg.Gates.ProductionRequiresAuth && anySegmentIsProdDeploy(low) {
		if approval() == "" {
			return Block("gate: production deploy blocked.\n" +
				"  A production release needs a named authorization. Orion does not grant one.\n" +
				"  Route: ask the release manager to authorize, then re-run with\n" +
				"  ORION_RELEASE_APPROVAL set to the approval reference (change ticket or release ID).\n" +
				"  Do not attempt to deploy by another path.")
		}
	}

	// 2. Never push directly to a long-lived branch, and never force push.
	//
	// Both main and develop are protected. Protecting only main would make the
	// pull request into develop optional, and an optional review gate is not a
	// gate. Feature branches are cut from develop and merge back into it.
	if cfg.Gates.BlockDirectPushToDefaultBranch {
		for _, branch := range cfg.VCS.ProtectedBranches {
			if branch == "" {
				continue
			}
			if reason := badPush(cmd, branch); reason != "" {
				return Block("gate: %s\n"+
					"  %s is protected. Work reaches it only through a reviewed pull request.\n"+
					"  Cut a branch from %s, push that, and open a PR:\n"+
					"    git switch -c %s<name> %s\n"+
					"    git push -u origin %s<name>\n"+
					"  Then use the PR helper rather than opening it by hand.",
					reason, branch, cfg.VCS.WorkBranch,
					cfg.VCS.BranchPrefix, cfg.VCS.WorkBranch, cfg.VCS.BranchPrefix)
			}
		}
	}

	// 3. History rewrites on shared refs.
	if reHardReset.MatchString(cmd) && strings.Contains(low, "origin/") {
		return Block("gate: refusing to hard-reset onto a remote ref.\n" +
			"  This discards local work irrecoverably. If that is genuinely intended,\n" +
			"  a human should run it.")
	}

	return Allow("")
}

func approval() string {
	for _, k := range []string{"ORION_RELEASE_APPROVAL", "RELEASE_APPROVAL"} {
		if v := strings.TrimSpace(os.Getenv(k)); v != "" {
			return v
		}
	}
	return ""
}

// anySegmentIsProdDeploy splits on shell separators and asks the question of
// each part.
//
// Two reasons for the split. A command like `echo hi && ./deploy.sh production`
// must still be caught, so scanning only the first token is not enough. And
// `echo 'deploying to production tomorrow'` must not be caught, so scanning
// the whole string is too much. Judging each segment by its own leading verb
// does both.
func anySegmentIsProdDeploy(low string) bool {
	for _, seg := range splitShellSegments(low) {
		seg = strings.TrimSpace(seg)
		if seg == "" || isInertCommand(seg) {
			continue
		}
		if looksLikeProdDeploy(seg) {
			return true
		}
	}
	return false
}

func splitShellSegments(s string) []string {
	return strings.FieldsFunc(s, func(r rune) bool {
		return r == ';' || r == '&' || r == '|' || r == '\n'
	})
}

// isInertCommand reports whether a segment merely prints or comments. These
// cannot deploy anything, and blocking them makes the gate look stupid, which
// is how a gate gets disabled.
func isInertCommand(seg string) bool {
	fields := strings.Fields(seg)
	if len(fields) == 0 {
		return true
	}
	switch strings.TrimPrefix(fields[0], "\\") {
	case "echo", "printf", "cat", "true", "false", ":", "#":
		return true
	}
	return strings.HasPrefix(fields[0], "#")
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
