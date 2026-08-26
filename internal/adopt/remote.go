package adopt

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"unicode"
)

// This file covers the two things adoption does OUTSIDE the working tree:
// the work branch, and the tracker/chat resources for the project.
//
// Both are held to the same rule: describe first, act second, and never
// create something twice. A Jira project in particular cannot be deleted
// without admin rights, so a command people re-run casually must not be able
// to litter a shared tracker by accident.

// ---------- work branch ----------

// EnsureWorkBranch creates the configured work branch when it is missing and
// switches to it, so the two-branch model is real rather than aspirational.
//
// orion.json can name `develop` as the base for every task branch while no
// such branch exists; nothing notices until the first PR has nowhere to go.
func EnsureWorkBranch(dir, branch string) (created bool, warnings []string, err error) {
	if strings.TrimSpace(branch) == "" {
		return false, nil, nil
	}
	git := func(args ...string) (string, error) {
		out, e := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
		return strings.TrimSpace(string(out)), e
	}

	if _, e := git("rev-parse", "--git-dir"); e != nil {
		return false, nil, nil // not a repo; Run already warned
	}
	// An unborn HEAD has no commit to branch from. Creating one here would
	// mean committing on the user's behalf, which is not adoption's business.
	if _, e := git("rev-parse", "--verify", "--quiet", "HEAD"); e != nil {
		return false, []string{
			"no commits yet, so " + branch + " was not created; make the first commit, then re-run"}, nil
	}
	if _, e := git("rev-parse", "--verify", "--quiet", "refs/heads/"+branch); e == nil {
		return false, nil, nil // already there; leave the user where they are
	}

	if out, e := git("checkout", "-b", branch); e != nil {
		return false, nil, fmt.Errorf("creating %s: %v\n%s", branch, e, out)
	}
	created = true

	// Push is best-effort. No remote, no network or no permission are all
	// ordinary, and none of them should fail an otherwise good adoption.
	if _, e := git("remote", "get-url", "origin"); e == nil {
		if out, e := git("push", "-u", "origin", branch); e != nil {
			warnings = append(warnings, fmt.Sprintf(
				"created %s locally but could not push it: %v\n         %s\n"+
					"         Push it before opening a PR against it: git push -u origin %s",
				branch, e, firstLine(out), branch))
		}
	} else {
		warnings = append(warnings, "created "+branch+" locally; no origin remote to push it to")
	}
	return created, warnings, nil
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// ---------- tracker and chat ----------

// RemotePlan is what provisioning WOULD do, rendered for a human before
// anything is created. Nothing here has side effects.
type RemotePlan struct {
	ProjectName string

	JiraKey     string
	JiraSite    string
	JiraExists  bool
	JiraSkip    string // why it will not happen, when it will not
	SlackName   string
	SlackTeam   string
	SlackSkip   string
	SlackIsPriv bool
	SlackExists bool
}

// Nothing reports whether the plan would create anything at all.
func (p RemotePlan) Nothing() bool {
	return (p.JiraKey == "" || p.JiraExists || p.JiraSkip != "") &&
		(p.SlackName == "" || p.SlackExists || p.SlackSkip != "")
}

// Describe renders the plan, leading with the consequence that cannot be
// undone. A confirmation prompt that does not say what is irreversible is
// not really a confirmation.
func (p RemotePlan) Describe() string {
	var b strings.Builder
	b.WriteString("Orion will create:\n")
	switch {
	case p.JiraSkip != "":
		fmt.Fprintf(&b, "  Jira           skipped: %s\n", p.JiraSkip)
	case p.JiraExists:
		fmt.Fprintf(&b, "  Jira project   %s already exists on %s; it will be bound, not created\n", p.JiraKey, p.JiraSite)
	case p.JiraKey != "":
		fmt.Fprintf(&b, "  Jira project   %s  %q  on %s\n", p.JiraKey, p.ProjectName, p.JiraSite)
	}
	switch {
	case p.SlackSkip != "":
		fmt.Fprintf(&b, "  Slack          skipped: %s\n", p.SlackSkip)
	case p.SlackExists:
		fmt.Fprintf(&b, "  Slack channel  #%s already exists in %s; it will be bound, not created\n",
			p.SlackName, p.SlackTeam)
	case p.SlackName != "":
		vis := "public"
		if p.SlackIsPriv {
			vis = "private"
		}
		fmt.Fprintf(&b, "  Slack channel  #%s (%s)  in %s\n", p.SlackName, vis, p.SlackTeam)
	}
	if p.JiraKey != "" && !p.JiraExists && p.JiraSkip == "" {
		b.WriteString("\nA Jira project cannot be deleted without admin rights.\n")
	}
	return b.String()
}

// DeriveProjectName is the repo's directory name: the one label already
// agreed by everyone looking at the checkout.
func DeriveProjectName(dir string) string {
	abs, err := filepath.Abs(dir)
	if err != nil {
		abs = dir
	}
	return filepath.Base(abs)
}

// DeriveJiraKey builds a Jira key from a name: initials of each word, or the
// leading letters of a single word. Keys are uppercase, letters only, and
// capped at 10 characters by Jira.
func DeriveJiraKey(name string) string {
	var words []string
	for _, w := range strings.FieldsFunc(name, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	}) {
		if w != "" {
			words = append(words, w)
		}
	}
	var key string
	if len(words) > 1 {
		for _, w := range words {
			for _, r := range w {
				if unicode.IsLetter(r) {
					key += string(unicode.ToUpper(r))
					break
				}
			}
		}
	} else if len(words) == 1 {
		// Keep digits: Jira allows them after the first character, and
		// dropping them turns "2fa" into "FA", which names a different thing.
		for _, r := range words[0] {
			key += string(unicode.ToUpper(r))
			if len(key) == 4 {
				break
			}
		}
	}
	if key == "" {
		key = "ORION"
	}
	if len(key) > 10 {
		key = key[:10]
	}
	// Jira requires the key to start with a letter.
	if !unicode.IsLetter(rune(key[0])) {
		key = "P" + key
	}
	return key
}

// ---------- orion.json patching ----------

// SetBlockField flips one field inside one top-level block of orion.json,
// in place, as text.
//
// Deliberately NOT a JSON round trip. orion.json carries "_comment_*" keys
// explaining why each limit exists, and Go marshals maps in key order, so
// unmarshalling and re-marshalling would scatter those comments away from
// the settings they document. The file is meant to be read by people; a
// patch that shuffles it is a regression even though the JSON stays valid.
func SetBlockField(src, block, field, newValue string) (string, bool) {
	start := regexp.MustCompile(`(?m)^\s*"` + regexp.QuoteMeta(block) + `"\s*:\s*\{`).FindStringIndex(src)
	if start == nil {
		return src, false
	}
	depth := 0
	end := -1
	for i := start[1] - 1; i < len(src); i++ {
		switch src[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				end = i
			}
		}
		if end >= 0 {
			break
		}
	}
	if end < 0 {
		return src, false
	}
	body := src[start[1]:end]
	re := regexp.MustCompile(`("` + regexp.QuoteMeta(field) + `"\s*:\s*)([^,\n}]*)`)
	loc := re.FindStringSubmatchIndex(body)
	if loc == nil {
		return src, false
	}
	if strings.TrimSpace(body[loc[4]:loc[5]]) == newValue {
		return src, false // already set; not a change
	}
	patched := body[:loc[4]] + newValue + body[loc[5]:]
	return src[:start[1]] + patched + src[end:], true
}
