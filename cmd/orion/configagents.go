package main

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/orion-sdlc/orion/internal/actors"
	"github.com/orion-sdlc/orion/internal/config"
	"github.com/orion-sdlc/orion/internal/creds"
	"github.com/orion-sdlc/orion/internal/workspace"
)

// modelChoices and effortChoices are the only values this wizard will ever
// write for the two fields. Both are numbered menus, never free text: the
// model list mirrors what `claude --model` accepts as an alias, and the
// effort list is `claude --effort`'s own enum (grounded against `claude
// --help` rather than guessed -- OR-131). A hand-edited agents.json can
// still hold something else; `claude` itself is what validates that.
// Nothing this wizard writes can be a typo.
var (
	modelChoices  = []string{"sonnet", "opus", "haiku", "fable"}
	effortChoices = []string{"low", "medium", "high", "xhigh", "max"}
)

// runConfigAgents is `orion config agents`: walks the configurable roster
// and asks, for each actor, a name (free text -- the one field a select
// menu cannot cover) and a model and an effort (both numbered menus).
//
// Global, not per-project (OR-132): who the implementer is and what it
// runs on is an operator preference, the same on every checkout, so this
// reads and writes ~/.orion/agents.json (config.LoadAgents/SaveAgents)
// rather than anything under the current repository. That is also why,
// unlike `orion config` for credentials, this subcommand needs no project
// root at all -- it works from any directory.
func runConfigAgents(args []string) {
	home := workspace.Home()

	if hasFlag(args, "--reset") {
		resetAgents(home, positional(args))
		return
	}

	if !creds.Interactive() {
		fmt.Fprintln(os.Stderr, "orion config agents needs a terminal.\n"+
			"  Run it from a shell, edit "+config.AgentsPath(home)+" by hand,\n"+
			"  or reset it non-interactively: orion config agents --reset [id...]")
		os.Exit(1)
	}

	next, err := config.LoadAgents(home)
	if err != nil {
		fmt.Fprintf(os.Stderr, "orion: %v\n", err)
		os.Exit(1)
	}
	if err := actors.Configure(next); err != nil {
		fmt.Fprintf(os.Stderr, "orion: existing %s: %v\n", config.AgentsPath(home), err)
		os.Exit(1)
	}

	in := bufio.NewReader(os.Stdin)
	fmt.Println("Orion agent roster (global: " + config.AgentsPath(home) + ")")
	fmt.Println("  Enter keeps a value as it is. Model and effort are numbered menus --")
	fmt.Println("  there is nothing to type for them, so there is nothing to mistype.")
	fmt.Println()

	for _, id := range actors.ConfigurableIDs() {
		a := actors.Get(id)
		fmt.Printf("%s -- %s\n", id, a.Display())

		cur := next[id]

		if name := promptName(in, cur.Name, a.Name); name != nil {
			cur.Name = name
		}
		if model := promptSelect(in, "model", modelChoices, a.Model); model != nil {
			cur.Model = *model
		}
		if effort := promptSelect(in, "effort", effortChoices, a.Effort); effort != nil {
			cur.Effort = *effort
		}

		if agentIsZero(cur) {
			delete(next, id)
		} else {
			next[id] = cur
		}
		fmt.Println()
	}

	if err := actors.Configure(next); err != nil {
		fmt.Fprintf(os.Stderr, "orion: %v\n  nothing was written.\n", err)
		os.Exit(1)
	}
	exitOn(config.SaveAgents(home, next))
	fmt.Printf("saved the agent roster to %s\n", config.AgentsPath(home))
}

// agentIsZero reports whether an override says nothing at all -- every
// field absent or cleared -- so the wizard does not litter agents.json
// with "id": {} for an actor the operator only looked at and left alone.
func agentIsZero(a config.Agent) bool {
	return a.Name == nil && a.Designation == "" && a.Model == "" && a.Effort == ""
}

// promptName asks for a free-text name: the one field in this wizard that
// is not a menu, because names are open-ended and a fixed list would just
// be wrong for most teams.
//
// Enter keeps whatever is already configured (returns nil: no change). A
// bare "-" clears it explicitly, which is how a team turns a persona off
// and the actor renders as its job title alone -- the same explicit-empty
// convention config.Agent.Name documents. Anything else becomes the name.
func promptName(in *bufio.Reader, current *string, shipped string) *string {
	shown := shipped
	verb := "shipped default"
	if current != nil {
		shown = *current
		verb = "current"
		if shown == "" {
			shown = "(cleared; renders as job title alone)"
		}
	}
	fmt.Printf("  name [%s: %s]\n", verb, shown)
	fmt.Print("  (Enter keeps it, \"-\" clears it, or type a new name) > ")
	line, _ := in.ReadString('\n')
	line = strings.TrimRight(line, "\r\n")
	if line == "" {
		return nil
	}
	if line == "-" {
		empty := ""
		return &empty
	}
	return &line
}

// promptSelect shows a numbered menu for one field and returns nil if the
// operator kept the current value, a pointer to "" if they chose the
// shipped default (clearing any override), or a pointer to the chosen
// value otherwise. There is no path from this menu to an unrecognized
// value: every option printed is one this function itself offered.
func promptSelect(in *bufio.Reader, label string, choices []string, current string) *string {
	shown := current
	if shown == "" {
		shown = "shipped default"
	}
	fmt.Printf("  %s [%s]\n", label, shown)
	fmt.Println("    0) keep as-is")
	fmt.Println("    1) shipped default")
	for i, c := range choices {
		fmt.Printf("    %d) %s\n", i+2, c)
	}
	for {
		fmt.Print("  > ")
		line, _ := in.ReadString('\n')
		line = strings.TrimSpace(line)
		if line == "" || line == "0" {
			return nil
		}
		if line == "1" {
			empty := ""
			return &empty
		}
		n, err := strconv.Atoi(line)
		if err != nil || n < 2 || n > len(choices)+1 {
			fmt.Printf("  %q is not one of the numbers above; try again\n", line)
			continue
		}
		v := choices[n-2]
		return &v
	}
}

// resetAgents restores the shipped defaults, either for the whole roster
// (ids empty) or for just the named ones -- the non-interactive equivalent
// of walking the wizard and choosing "shipped default" for every field of
// every actor, for a script or a person in a hurry.
func resetAgents(home string, ids []string) {
	if len(ids) == 0 {
		if err := config.SaveAgents(home, map[string]config.Agent{}); err != nil {
			fmt.Fprintf(os.Stderr, "orion: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("reset the whole agent roster to its shipped defaults.")
		return
	}

	next, err := config.LoadAgents(home)
	if err != nil {
		fmt.Fprintf(os.Stderr, "orion: %v\n", err)
		os.Exit(1)
	}
	for _, id := range ids {
		delete(next, id)
	}
	exitOn(config.SaveAgents(home, next))
	fmt.Printf("reset %s to its shipped default%s.\n", strings.Join(ids, ", "), plural(len(ids)))
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}
