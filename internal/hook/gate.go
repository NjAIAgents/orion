package hook

import (
	"os"
	"os/exec"
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

// isInertCommand reports whether a segment merely prints, reads or searches.
// These cannot deploy anything, and blocking them makes the gate look stupid,
// which is how a gate gets disabled.
//
// SEARCHING FOR THE WORD IS NOT DOING THE THING. The list below covers read
// and search tools as well as printers, because the deploy vocabulary is
// matched anywhere in a segment: `grep -rn "production deploy blocked"` has
// both a prod word and a deploy verb in its ARGUMENT and was blocked. So was
// every attempt to grep for the gate's own message while working on the gate,
// which is exactly when it is needed.
//
// Safe because none of these executes what it finds. A pipeline that feeds
// one into something that does -- `grep ... | sh` -- is split on the pipe
// first, so the `sh` segment is still judged on its own.
func isInertCommand(seg string) bool {
	fields := strings.Fields(seg)
	if len(fields) == 0 {
		return true
	}
	switch strings.TrimPrefix(fields[0], "\\") {
	case "echo", "printf", "cat", "true", "false", ":", "#",
		// Search and read. They report what a file says; they do not run it.
		"grep", "egrep", "fgrep", "rg", "ag", "ack",
		"find", "fd", "ls", "tree", "stat", "file",
		"head", "tail", "less", "more", "wc", "sed", "awk", "cut", "sort", "uniq",
		"diff", "cmp", "jq", "yq", "basename", "dirname", "realpath", "readlink":
		return true
	}
	// `git log --grep "deploy to prod"` and `git diff -- deploy/prod.yaml`
	// read history and working tree. Only the read-only subcommands: `git
	// push` is judged elsewhere, and this must not become a hole for it.
	if strings.TrimPrefix(fields[0], "\\") == "git" && len(fields) > 1 {
		switch fields[1] {
		case "log", "diff", "show", "status", "grep", "blame", "ls-files", "cat-file":
			return true
		}
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
	// Explicit refspec naming the branch, e.g.
	//   git push origin main
	//   git push origin HEAD:main
	//   git push origin feature:main
	db := regexp.QuoteMeta(defaultBranch)
	patterns := []string{
		`\bgit\s+push\b[^|;&]*\s` + db + `\s*($|[|;&])`, // ... origin main
		`\bgit\s+push\b[^|;&]*:` + db + `\b`,            // ... HEAD:main
	}
	named := false
	for _, p := range patterns {
		if regexp.MustCompile(p).MatchString(cmd) {
			named = true
			break
		}
	}
	// A push with no refspec goes to the CURRENT branch, which the command
	// text never mentions -- so standing on a protected branch and running
	// `git push --force` would otherwise sail past a check that only reads
	// the command.
	if !named && !hasRefspec(cmd) && strings.EqualFold(currentBranch(), defaultBranch) {
		named = true
	}
	if !named {
		// Not this branch. A force push somewhere else is the author's
		// business: main and develop reach their state through a reviewed
		// pull request, and every other branch is the work in progress
		// that leads to one -- rebasing and amending it is the normal way
		// to arrive at a reviewable history.
		//
		// This used to block EVERY force push, before looking at where it
		// went, and then reported "main is protected" whatever the target
		// was. So force-pushing a feature branch was refused for a reason
		// that named a branch the command never mentioned, which sends the
		// reader looking in the wrong place (OR-420).
		return ""
	}
	if reForcePush.MatchString(cmd) {
		return "force push to " + defaultBranch + " blocked."
	}
	return "direct push to " + defaultBranch + " blocked."
}

// hasRefspec reports whether a push names a branch to push, rather than
// relying on the current one. Anything after the remote that is not a flag
// is a refspec.
func hasRefspec(cmd string) bool {
	fields := strings.Fields(cmd)
	for i, f := range fields {
		if f != "push" {
			continue
		}
		n := 0
		for _, a := range fields[i+1:] {
			if strings.HasPrefix(a, "-") {
				continue
			}
			if a == "|" || a == ";" || a == "&&" {
				break
			}
			n++ // first is the remote, a second is the refspec
			if n > 1 {
				return true
			}
		}
		return false
	}
	return false
}

// currentBranch is the branch HEAD is on, or "" when there is no answer --
// no repository, or a detached HEAD. Empty never matches a protected name,
// so an unreadable HEAD leaves the command to the rules that read the text.
func currentBranch() string {
	out, err := exec.Command("git", "rev-parse", "--abbrev-ref", "HEAD").Output()
	if err != nil {
		return ""
	}
	if b := strings.TrimSpace(string(out)); b != "HEAD" {
		return b
	}
	return ""
}
