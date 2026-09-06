package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/orion-sdlc/orion/internal/tracker"
	"github.com/orion-sdlc/orion/internal/ui"
)

// `orion idea` -- reading and filling a discovery idea's own fields.
//
// A Jira Product Discovery idea carries a theme, a roadmap horizon, a state,
// a short description, a documents link. Orion filed ideas with a summary and
// a description and left the rest blank, so the board showed a row of empty
// forms.
//
// The judgement fields are not filled by `orion new`, which makes no model
// call and has read nothing but the sentences typed at it. They are filled by
// the INTENT STAGE, after it has researched whatever the idea pointed at --
// which is why this is a command an agent can run rather than a flag on
// something else.
//
// TWO COMMANDS, AND THE ORDER MATTERS. `fields` prints what a project
// actually has and what each option field will accept; `set` writes one.
// Every one of these is a custom field whose id and options differ per Jira
// instance, so a value that is not offered is refused -- reading first is the
// difference between filling the form and losing the write.

func runIdea(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: orion idea fields <KEY>\n"+
			"       orion idea set <KEY> --field \"Name\" --value \"Value\" [...]")
		os.Exit(64)
	}
	switch args[0] {
	case "fields":
		mustArg(args, 1, "orion idea fields <KEY>")
		runIdeaFields(args[1])
	case "set":
		mustArg(args, 1, "orion idea set <KEY> --field \"Name\" --value \"Value\"")
		runIdeaSet(args[1], args[2:])
	default:
		fmt.Fprintf(os.Stderr, "orion idea: unknown subcommand %q (fields, set)\n", args[0])
		os.Exit(64)
	}
}

// runIdeaFields prints what this idea's project offers.
//
// Grouped by whether a field takes free text or one of a fixed set, because
// that is the only distinction that changes what the caller may write.
func runIdeaFields(key string) {
	j, err := tracker.NewJiraFromEnv()
	exitOn(err)
	w := os.Stdout

	project, typeID, err := ideaContext(j, key)
	exitOn(err)

	fields, err := j.IdeaFields(project, typeID)
	exitOn(err)

	fmt.Fprintln(w, ui.Heading(w, key+" -- fields in "+project))
	var free, options []tracker.IdeaFieldInfo
	for _, f := range fields {
		if !f.Writable {
			continue
		}
		if len(f.Options) > 0 {
			options = append(options, f)
			continue
		}
		free = append(free, f)
	}

	fmt.Fprintln(w, "\n  Free text:")
	for _, f := range free {
		fmt.Fprintf(w, "    %s\n", f.Name)
	}
	fmt.Fprintln(w, "\n  One of these values only:")
	for _, f := range options {
		fmt.Fprintf(w, "    %-26s %s\n", f.Name, strings.Join(f.Options, " | "))
	}
	fmt.Fprintf(w, "\n  %s\n", ui.Dim(w,
		"orion idea set "+key+" --field \"Theme\" --value \"Delight users\""))
}

// runIdeaSet writes the named fields.
//
// Repeatable --field/--value pairs rather than one call per field: they are
// one PUT to Jira, and an agent setting five fields should not make five
// round trips or leave a half-filled form when the third is rejected.
func runIdeaSet(key string, rest []string) {
	want, err := parseIdeaFields(rest)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(64)
	}
	if len(want) == 0 {
		fmt.Fprintln(os.Stderr, "orion idea set: nothing to set\n"+
			"  e.g. orion idea set "+key+" --field \"Theme\" --value \"Delight users\"")
		os.Exit(64)
	}

	j, err := tracker.NewJiraFromEnv()
	exitOn(err)
	w := os.Stdout

	project, typeID, err := ideaContext(j, key)
	exitOn(err)

	skipped, err := j.SetIdeaFields(key, project, typeID, want)
	exitOn(err)

	set := len(want) - len(skipped)
	if set > 0 {
		ui.Ok(w, "set", "%d field(s) on %s", set, key)
	}
	// A skipped field is named with the reason, and the command EXITS
	// NON-ZERO. A caller that asked for five fields and got three needs to
	// know from the exit code, not from reading the output it did not print.
	if len(skipped) > 0 {
		for _, s := range skipped {
			ui.Warn(w, "skipped %s", s)
		}
		fmt.Fprintf(w, "  %s\n", ui.Dim(w, "orion idea fields "+key+" lists what this project accepts"))
		os.Exit(1)
	}
}

// parseIdeaFields reads repeated --field/--value pairs.
//
// Each --field must be followed by its --value before the next --field, so a
// mismatched pair is caught here rather than silently pairing the wrong name
// with the wrong value.
func parseIdeaFields(args []string) ([]tracker.IdeaField, error) {
	var out []tracker.IdeaField
	var pending string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--field":
			if pending != "" {
				return nil, fmt.Errorf("orion idea set: --field %q has no --value", pending)
			}
			i++
			if i >= len(args) {
				return nil, fmt.Errorf("orion idea set: --field needs a name")
			}
			pending = args[i]
		case "--value":
			if pending == "" {
				return nil, fmt.Errorf("orion idea set: --value with no --field before it")
			}
			i++
			if i >= len(args) {
				return nil, fmt.Errorf("orion idea set: --value needs a value")
			}
			out = append(out, tracker.IdeaField{Name: pending, Value: args[i]})
			pending = ""
		default:
			return nil, fmt.Errorf("orion idea set: unexpected %q", args[i])
		}
	}
	if pending != "" {
		return nil, fmt.Errorf("orion idea set: --field %q has no --value", pending)
	}
	return out, nil
}

// ideaContext resolves the project and issue type an idea belongs to.
//
// The project comes from the key's own prefix, which is what a Jira project
// key IS, and the type is matched by the name the issue reports against the
// project's own type list. Both derived rather than asked for: an agent knows
// the key it was given and should not have to be told what that key implies.
func ideaContext(j *tracker.Jira, key string) (project, typeID string, err error) {
	i := strings.LastIndex(key, "-")
	if i <= 0 {
		return "", "", fmt.Errorf("%q is not a tracker key, e.g. PRIOR-3", key)
	}
	project = strings.ToUpper(key[:i])

	is, err := j.GetIssue(key)
	if err != nil {
		return "", "", err
	}
	types, err := j.IssueTypes(project)
	if err != nil {
		return "", "", err
	}
	for _, t := range types {
		if strings.EqualFold(strings.TrimSpace(t.Name), strings.TrimSpace(is.IssueType)) {
			return project, t.ID, nil
		}
	}
	return "", "", fmt.Errorf("%s has no issue type named %q", project, is.IssueType)
}
