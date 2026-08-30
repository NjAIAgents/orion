package main

import (
	"fmt"
	"regexp"
	"strconv"
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

// A range is written PROJ-140..PROJ-145 and is INCLUSIVE of both ends.
//
// Inclusive because the operator is reading the ends off a board, not writing
// a loop: "OR-49 through OR-83" means both of those tickets, and a
// half-open range would silently drop the last one -- the single ticket
// nobody would think to check.
const keyRangeSep = ".."

// maxExpandedKeys caps one invocation.
//
// tracker.Search caps a query at a hundred results for the same reason: past
// that it is a runaway, not a workload. Here the runaway is a typo -- OR-1..OR-99999
// is four keystrokes away from OR-1..OR-9 and would write to every ticket in
// the project. Refusing loudly costs a second invocation and re-running is a
// no-op, so splitting a genuinely large batch is cheap.
const maxExpandedKeys = 100

// expandTicketKeys parses the positional ticket arguments of a command that
// accepts bare keys, comma- OR space-separated, and INCLUSIVE ranges.
//
// Ranges are the reason this exists rather than ticketKeys: attaching the
// thirty-six consecutive tickets of one work block to a milestone meant a
// scripted REST loop, because naming them one at a time is what the Jira UI
// already makes you do (OR-222).
//
// Shared deliberately. `release remove` is the obvious sibling of `release
// add` and has to accept exactly the same argument shapes, so the parsing
// lives here next to ticketKeys rather than inside either command.
//
// All or nothing, like ticketKeys: one unparseable token stops the run. A
// range that expanded to nothing because its ends were reversed is the worst
// possible outcome -- it looks like success and writes to no ticket.
func expandTicketKeys(cmd, argShape string, args []string) ([]string, error) {
	var out []string
	seen := map[string]bool{}
	add := func(k string) {
		if !seen[k] {
			seen[k] = true
			out = append(out, k)
		}
	}
	for _, a := range args {
		// Commas as well as spaces: a list pasted out of a ticket, a
		// spreadsheet or another tool arrives comma-separated, and quietly
		// treating "OR-1,OR-2" as one malformed key would reject a paste that
		// says exactly what it means.
		for _, tok := range strings.Split(a, ",") {
			tok = strings.ToUpper(strings.TrimSpace(tok))
			if tok == "" {
				continue
			}
			if !strings.Contains(tok, keyRangeSep) {
				if !ticketKeyRe.MatchString(tok) {
					return nil, fmt.Errorf("orion %s: %q is not a ticket key or range "+
						"(expected PROJ-123 or PROJ-123..PROJ-130)\n"+
						"usage: orion %s %s", cmd, tok, cmd, argShape)
				}
				add(tok)
				continue
			}
			expanded, err := expandKeyRange(cmd, tok)
			if err != nil {
				return nil, err
			}
			for _, k := range expanded {
				add(k)
			}
		}
	}
	if len(out) > maxExpandedKeys {
		return nil, fmt.Errorf("orion %s: that expands to %d tickets, more than the %d "+
			"one invocation will write to.\n"+
			"       Split it into smaller ranges; re-running is a no-op, so overlap is harmless.",
			cmd, len(out), maxExpandedKeys)
	}
	return out, nil
}

// expandKeyRange turns PROJ-140..PROJ-145 into the six keys it names.
//
// Every refusal names the reason rather than returning nothing. A range whose
// ends are reversed, or that spans two projects, is a mistake the operator can
// only correct if they are told which one they made -- and silently expanding
// it to the empty set would report "0 tickets" as though the range were
// simply empty.
func expandKeyRange(cmd, tok string) ([]string, error) {
	ends := strings.Split(tok, keyRangeSep)
	if len(ends) != 2 || ends[0] == "" || ends[1] == "" {
		return nil, fmt.Errorf("orion %s: %q is not a range; a range is two keys "+
			"joined by %q, as in PROJ-140..PROJ-145", cmd, tok, keyRangeSep)
	}
	fromProject, fromNum, ok := splitTicketKey(ends[0])
	if !ok {
		return nil, fmt.Errorf("orion %s: %q starts at %q, which is not a ticket key "+
			"(expected PROJ-123)", cmd, tok, ends[0])
	}
	toProject, toNum, ok := splitTicketKey(ends[1])
	if !ok {
		return nil, fmt.Errorf("orion %s: %q ends at %q, which is not a ticket key "+
			"(expected PROJ-123)", cmd, tok, ends[1])
	}
	if fromProject != toProject {
		return nil, fmt.Errorf("orion %s: %q spans two projects, %s and %s; "+
			"a range must stay inside one project", cmd, tok, fromProject, toProject)
	}
	if toNum < fromNum {
		return nil, fmt.Errorf("orion %s: %q ends before it starts (%d is below %d); "+
			"write the lower key first", cmd, tok, toNum, fromNum)
	}
	keys := make([]string, 0, toNum-fromNum+1)
	for n := fromNum; n <= toNum; n++ {
		keys = append(keys, fmt.Sprintf("%s-%d", fromProject, n))
	}
	return keys, nil
}

// splitTicketKey separates PROJ-123 into its project key and issue number.
func splitTicketKey(key string) (project string, num int, ok bool) {
	if !ticketKeyRe.MatchString(key) {
		return "", 0, false
	}
	i := strings.LastIndex(key, "-")
	n, err := strconv.Atoi(key[i+1:])
	if err != nil {
		// Unreachable while ticketKeyRe requires digits after the hyphen, but
		// a number longer than an int is not the caller's fault to discover
		// as a panic.
		return "", 0, false
	}
	return key[:i], n, true
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
