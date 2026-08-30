package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/orion-sdlc/orion/internal/actors"
	"github.com/orion-sdlc/orion/internal/config"
	"github.com/orion-sdlc/orion/internal/creds"
	"github.com/orion-sdlc/orion/internal/ui"
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

	// Answered before the terminal check below and before anything reads
	// stdin, which is the whole point of it -- see listAgents.
	if hasFlag(args, "--list") {
		exitOn(listAgents(home, os.Stdout))
		return
	}

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

// listAgents is `orion config agents --list`: the read-only twin of the
// wizard, printing every actor with the values a run will actually use and
// which of them agents.json decided.
//
// WHY A FLAG ON THE EXISTING SUBCOMMAND, not a top-level `orion agents`.
// The precedent is already set one level up: `orion config show` is the
// read-only twin of `orion config`, not `orion show`. The roster is
// configuration, the command that edits it lives under `orion config`, and
// a second top-level noun would leave two places to look for one thing --
// with the top-level one, inevitably, the one somebody finds first and then
// wonders why it cannot edit anything. Discoverability is bought by naming
// it in `orion config --help` instead, which is where a reader looking for
// the roster is already standing.
//
// WHY IT EXISTS. agents.json holds only OVERRIDES, so most of the roster is
// absent from it; the effective model for an actor nobody has overridden is
// in Go source. That makes the one question worth asking before an
// unattended run -- which agent runs on which model, at what effort --
// answerable only by reading two files and merging them by hand. Since
// OR-184 the watcher works several tickets at once, so the roster is the
// cost model multiplied by the concurrency cap.
//
// IT NEVER PROMPTS. --list is answered before the wizard's terminal check
// and before anything reads stdin. That ordering is the point, not an
// accident of layout: runConfig documents a real bug where `--help` fell
// through to the wizard's default case and blocked on stdin with Ctrl-C the
// only way out. A listing flag must be incapable of starting a prompt,
// including when stdout is redirected -- which is how anyone captures this
// to share it.
func listAgents(home string, out io.Writer) error {
	over, err := config.LoadAgents(home)
	if err != nil {
		return err
	}
	roster := actors.Roster(over)

	path := config.AgentsPath(home)
	fmt.Fprintln(out, ui.Heading(out, "Orion agent roster"))
	fmt.Fprintln(out, ui.Dim(out, "effective values -- the shipped defaults with "+path+" applied"))
	if len(over) == 0 {
		fmt.Fprintln(out, ui.Dim(out, "nothing is overridden there; every value below is a shipped default"))
	}
	fmt.Fprintln(out)

	head := []string{"id", "name", "designation", "model", "effort", "overridden"}
	rows := make([][]string, 0, len(roster))
	for _, e := range roster {
		rows = append(rows, []string{
			e.ID,
			orNotSet(e.Name),
			orNotSet(e.Designation),
			orNotSet(e.Model),
			orNotSet(e.Effort),
			orNotSet(strings.Join(e.Overridden.Fields(), ", ")),
		})
	}

	w := make([]int, len(head))
	for i, h := range head {
		w[i] = utf8.RuneCountInString(h)
	}
	for _, r := range rows {
		for i, c := range r {
			if n := utf8.RuneCountInString(c); n > w[i] {
				w[i] = n
			}
		}
	}

	fmt.Fprintln(out, ui.Heading(out, strings.TrimRight(joinColumns(head, w), " ")))
	for i, r := range rows {
		// Only the two identity columns are coloured, and only from the
		// non-semantic palette ui.Identity draws on. Model is deliberately
		// left plain: painting opus red to mean "expensive" would collide
		// with failure and make a correct roster read as a fault. Cost is
		// carried by the model's own name, which is a word.
		id := roster[i].ID
		line := ui.Identity(out, id, column(r[0], w[0])) + "  " +
			ui.Identity(out, id, column(r[1], w[1])) + "  " +
			joinColumns(r[2:], w[2:])
		fmt.Fprintln(out, strings.TrimRight(line, " "))
	}

	fmt.Fprintln(out)
	fmt.Fprintln(out, ui.Dim(out, "overridden  the fields "+path+" decides for that actor;"))
	fmt.Fprintln(out, ui.Dim(out, "            every other value on the row is a shipped default"))
	fmt.Fprintln(out, ui.Dim(out, "-           not set. No name renders the actor as its job title alone;"))
	fmt.Fprintln(out, ui.Dim(out, "            no model means Orion itself, which is Go rather than an agent;"))
	fmt.Fprintln(out, ui.Dim(out, "            no effort leaves the claude CLI's own level in force"))
	return nil
}

// orNotSet renders an absent value as a word-free marker that still reads
// when the colour is stripped. The legend under the table says what "not
// set" means for each column, because it means something different in each.
func orNotSet(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func joinColumns(cells []string, w []int) string {
	parts := make([]string, len(cells))
	for i, c := range cells {
		parts[i] = column(c, w[i])
	}
	return strings.TrimRight(strings.Join(parts, "  "), " ")
}

// column pads to n display columns counting RUNES rather than bytes, and is
// applied BEFORE any colour: an escape code has a byte length and no width,
// so padding a painted string leaves every coloured cell short of its
// column.
func column(s string, n int) string {
	if d := n - utf8.RuneCountInString(s); d > 0 {
		return s + strings.Repeat(" ", d)
	}
	return s
}
