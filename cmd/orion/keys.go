package main

import (
	"fmt"
	"regexp"
	"strings"
)

// A ticket key is PROJ-123: a project key, a hyphen, an issue number.
// A project key is PROJ alone -- the same prefix, no number.
var (
	ticketKeyRe  = regexp.MustCompile(`^[A-Z][A-Z0-9]*-[0-9]+$`)
	projectKeyRe = regexp.MustCompile(`^[A-Z][A-Z0-9]*$`)
)

// ticketKeys upcases and validates the positional arguments of a command
// that takes ticket keys, rejecting the whole invocation on the first token
// that is not one.
//
// Rejecting at parse time is the point. `orion collect or or-39` used to
// take "or" as a key, upcase it to OR, look for a branch named orion/or, and
// warn that no pull request was found for it -- an accurate sentence about a
// key that is structurally incapable of ever having one, printed two screens
// after the mistake it describes. Worse, it did not fail: the rest of the
// pass ran and the exit code said success, so a typo in a cron line would
// warn once per tick forever with nothing downstream noticing.
//
// All or nothing: one bad key stops the run rather than quietly doing the
// part that parsed.
func ticketKeys(cmd, argShape string, args []string) ([]string, error) {
	keys := make([]string, 0, len(args))
	for _, a := range args {
		k := strings.ToUpper(strings.TrimSpace(a))
		if !ticketKeyRe.MatchString(k) {
			return nil, fmt.Errorf("orion %s: %q is not a ticket key (expected PROJ-123)\n"+
				"usage: orion %s %s\n"+
				"       keys only; the project is inferred from the working directory",
				cmd, a, cmd, argShape)
		}
		keys = append(keys, k)
	}
	return keys, nil
}

// projectKeys is the same guard for `orion watch`, whose positionals are
// PROJECT keys and not ticket keys -- it scopes a queue to a project, so
// FCIA is the valid form and FCIA-6 is the mistake.
func projectKeys(args []string) ([]string, error) {
	keys := make([]string, 0, len(args))
	for _, a := range args {
		k := strings.ToUpper(strings.TrimSpace(a))
		if !projectKeyRe.MatchString(k) {
			return nil, fmt.Errorf("orion watch: %q is not a project key (expected PROJ)\n"+
				"usage: orion watch [PROJECT...]\n"+
				"       project keys only; a ticket key like PROJ-123 is not a project",
				a)
		}
		keys = append(keys, k)
	}
	return keys, nil
}
