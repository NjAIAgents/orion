package changelog

import (
	"os/exec"
	"regexp"
	"sort"
	"strings"
)

// A key as it is written in a commit message (OR-113), and as it is written in
// the branch name Orion cuts for it (orion/or-113). Both are searched: the key
// appears in the subject only when whoever wrote the message put it there, and
// a merge commit names the branch whether or not anyone remembered.
var (
	keyInText   = regexp.MustCompile(`\b([A-Z][A-Z0-9]+-[0-9]+)\b`)
	keyInBranch = regexp.MustCompile(`(?i)\borion/([a-z][a-z0-9]+-[0-9]+)\b`)
)

// TicketKeys returns the ticket keys referenced since the last tag.
//
// This is the input to the one question a release has to answer about
// fragments: did a ticket ship without one. Without it a missing entry is
// invisible -- the release simply does not mention the change, and nothing
// says a fragment was expected. A key here is a prompt to look, not a verdict:
// a commit that merely mentions another ticket is indistinguishable from one
// that implements it, and reporting a key too many costs a glance while
// reporting one too few is the failure this exists to catch.
//
// Best effort. A repository with no tags, or no git at all, yields nothing
// rather than an error: a changelog must not be blocked by the absence of a
// history to check it against.
func TicketKeys(root string) []string {
	rng := "HEAD"
	if tag, err := git(root, "describe", "--tags", "--abbrev=0"); err == nil && tag != "" {
		rng = tag + "..HEAD"
	}
	out, err := git(root, "log", "--format=%s%n%b", rng)
	if err != nil {
		return nil
	}

	seen := map[string]bool{}
	for _, m := range keyInText.FindAllStringSubmatch(out, -1) {
		seen[strings.ToUpper(m[1])] = true
	}
	for _, m := range keyInBranch.FindAllStringSubmatch(out, -1) {
		seen[strings.ToUpper(m[1])] = true
	}

	keys := make([]string, 0, len(seen))
	for k := range seen {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// Unrecorded returns the keys among seen that no fragment covers.
func Unrecorded(seen, collated []string) []string {
	have := map[string]bool{}
	for _, k := range collated {
		have[strings.ToUpper(k)] = true
	}
	var out []string
	for _, k := range seen {
		if !have[strings.ToUpper(k)] {
			out = append(out, k)
		}
	}
	return out
}

func git(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	b, err := cmd.Output()
	return strings.TrimSpace(string(b)), err
}
