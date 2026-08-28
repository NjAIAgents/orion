package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/orion-sdlc/orion/internal/actors"
	"github.com/orion-sdlc/orion/internal/adopt"
	"github.com/orion-sdlc/orion/internal/config"
	"github.com/orion-sdlc/orion/internal/creds"
)

// modelChoices and effortChoices are the only values this wizard will ever
// write for the two fields. Both are numbered menus, never free text: the
// model list mirrors what `claude --model` accepts as an alias, and the
// effort list is `claude --effort`'s own enum (grounded against `claude
// --help` rather than guessed -- OR-131). A hand-edited orion.json can still
// hold something else; `claude` itself is what validates that. Nothing this
// wizard writes can be a typo.
var (
	modelChoices  = []string{"sonnet", "opus", "haiku", "fable"}
	effortChoices = []string{"low", "medium", "high", "xhigh", "max"}
)

// runConfigAgents is `orion config agents`: walks the configurable roster
// and asks, for each actor, a name (free text -- the one field a select
// menu cannot cover) and a model and an effort (both numbered menus). It
// reads and writes the `agents` block of orion.json, the same block
// actors.Configure() already knows how to merge onto the shipped roster.
func runConfigAgents(args []string) {
	root, err := config.FindRoot(rootOrCwd())
	exitOn(err)
	path := filepath.Join(root, "orion.json")

	if hasFlag(args, "--reset") {
		resetAgents(path, positional(args))
		return
	}

	if !creds.Interactive() {
		fmt.Fprintln(os.Stderr, "orion config agents needs a terminal.\n"+
			"  Run it from a shell, edit the \"agents\" block in orion.json by hand,\n"+
			"  or reset it non-interactively: orion config agents --reset [id...]")
		os.Exit(1)
	}

	cfg := config.Load(root)
	if err := actors.Configure(cfg.Agents); err != nil {
		fmt.Fprintf(os.Stderr, "orion: existing orion.json agents block: %v\n", err)
		os.Exit(1)
	}

	next := map[string]config.Agent{}
	for id, a := range cfg.Agents {
		next[id] = a
	}

	in := bufio.NewReader(os.Stdin)
	fmt.Println("Orion agent roster")
	fmt.Println("  Enter keeps a value as it is. Model and effort are numbered menus --")
	fmt.Println("  there is nothing to type for them, so there is nothing to mistype.")
	fmt.Println()

	for _, id := range actors.ConfigurableIDs() {
		a := actors.Get(id)
		fmt.Printf("%s -- %s\n", id, a.Display())

		cur := next[id]

		name := promptName(in, cur.Name, a.Name)
		if name != nil {
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

	writeAgentsBlock(path, next)
}

// agentIsZero reports whether an override says nothing at all -- every
// field absent or cleared -- so the wizard does not litter orion.json with
// "id": {} for an actor the operator only looked at and left alone.
func agentIsZero(a config.Agent) bool {
	return a.Name == nil && a.Designation == "" && a.Model == "" && a.Effort == ""
}

// promptName asks for a free-text name: the one field in this wizard that
// is not a menu, because names are open-ended and a fixed list would just
// be wrong for most teams.
//
// Enter keeps whatever is already in orion.json (returns nil: no change).
// A bare "-" clears it explicitly, which is how a team turns a persona off
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

// writeAgentsBlock renders `agents` as JSON keyed by actor id, in id order
// so a re-run produces a stable diff, and patches it into orion.json with
// adopt.SetBlock -- everything else in the file is left exactly as it was.
func writeAgentsBlock(path string, agents map[string]config.Agent) {
	b, err := os.ReadFile(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "orion: could not read %s: %v\n", path, err)
		os.Exit(1)
	}

	ids := make([]string, 0, len(agents))
	for id := range agents {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	entries := make([]string, 0, len(ids))
	for _, id := range ids {
		entries = append(entries, "    "+jsonStr(id)+": "+marshalAgent(agents[id]))
	}
	body := strings.Join(entries, ",\n")

	out, changed := adopt.SetBlock(string(b), "agents", body)
	if !changed {
		fmt.Println("nothing changed.")
		return
	}
	if err := os.WriteFile(path, []byte(out), 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "orion: could not write %s: %v\n", path, err)
		os.Exit(1)
	}
	fmt.Printf("saved the agent roster to %s\n", path)
}

// marshalAgent renders one actor's override, only the fields actually set --
// an omitted field keeps the shipped default, and writing it out anyway
// (e.g. `"designation": ""`) would silently mean something different: an
// explicitly empty designation, which for every actor but the name field
// has no such "clear it" meaning.
func marshalAgent(a config.Agent) string {
	var fields []string
	if a.Name != nil {
		fields = append(fields, `"name": `+jsonStr(*a.Name))
	}
	if a.Designation != "" {
		fields = append(fields, `"designation": `+jsonStr(a.Designation))
	}
	if a.Model != "" {
		fields = append(fields, `"model": `+jsonStr(a.Model))
	}
	if a.Effort != "" {
		fields = append(fields, `"effort": `+jsonStr(a.Effort))
	}
	if len(fields) == 0 {
		return "{}"
	}
	indented := make([]string, len(fields))
	for i, f := range fields {
		indented[i] = "      " + f
	}
	return "{\n" + strings.Join(indented, ",\n") + "\n    }"
}

func jsonStr(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// resetAgents restores the shipped defaults, either for the whole roster
// (ids empty) or for just the named ones -- the non-interactive equivalent
// of walking the wizard and choosing "shipped default" for every field of
// every actor, for a script or a person in a hurry.
func resetAgents(path string, ids []string) {
	b, err := os.ReadFile(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "orion: could not read %s: %v\n", path, err)
		os.Exit(1)
	}

	if len(ids) == 0 {
		out, changed := adopt.SetBlock(string(b), "agents", "")
		if !changed {
			fmt.Println("nothing changed.")
			return
		}
		exitOn(os.WriteFile(path, []byte(out), 0o644))
		fmt.Println("reset the whole agent roster to its shipped defaults.")
		return
	}

	root := filepath.Dir(path)
	cfg := config.Load(root)
	next := map[string]config.Agent{}
	for id, a := range cfg.Agents {
		next[id] = a
	}
	for _, id := range ids {
		delete(next, id)
	}
	writeAgentsBlock(path, next)
	fmt.Printf("reset %s to its shipped default%s.\n", strings.Join(ids, ", "), plural(len(ids)))
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}
