package main

// `orion routes` prints the routing table.
//
// The table decides which actor works a ticket, from markers something else
// wrote at creation time. For as long as it was a private constant in
// internal/work, nothing that CREATES a ticket knew the vocabulary existed,
// so the metadata routing depends on was set by luck -- and in practice
// never. Printing it is what turns it into a contract a planner can read
// instead of a second copy it keeps and lets drift (OR-191).
//
// Read-only, and needs no project: the table is the same everywhere, and a
// command a planner has to be inside a configured repository to ask is a
// command it will not ask.

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/orion-sdlc/orion/internal/actors"
	"github.com/orion-sdlc/orion/internal/config"
	"github.com/orion-sdlc/orion/internal/ui"
	"github.com/orion-sdlc/orion/internal/work"
	"github.com/orion-sdlc/orion/internal/workspace"
)

func runRoutes() {
	// The globally configured roster (OR-132), so the table names the actors
	// by whatever the operator called them rather than by the shipped
	// defaults. A table that names an actor nobody recognises is a table a
	// planner cannot act on.
	agents, err := config.LoadAgents(workspace.Home())
	exitOn(err)
	exitOn(actors.Configure(agents))

	printRoutes(os.Stdout)
}

func printRoutes(w io.Writer) {
	rules := work.Rules()

	fmt.Fprintf(w, "%s\n", ui.Heading(w, "routing table"))
	fmt.Fprintf(w, "  %s\n\n", ui.Dim(w,
		"The first rule below whose keyword EQUALS the ticket's issue type, one of its\n"+
			"  components, or one of its labels wins. Case-insensitive, and equality rather\n"+
			"  than containment: a component named docsite-infra is not a documentation ticket."))

	// One column width across both blocks. Two blocks aligned to two
	// different widths read as two unrelated tables, and they are one
	// decision: who is routable and who is not.
	ids := []string{work.DefaultActor}
	for _, r := range rules {
		ids = append(ids, r.Actor)
	}
	for _, o := range work.OtherPaths() {
		ids = append(ids, o.Actor)
	}
	width := 0
	for _, id := range ids {
		if n := len([]rune(actors.Display(id))); n > width {
			width = n
		}
	}

	for i, r := range rules {
		fmt.Fprintf(w, "  %d. %-*s  %s\n", i+1, width, actors.Display(r.Actor),
			strings.Join(r.Keywords, ", "))
	}
	fmt.Fprintf(w, "     %-*s  %s\n", width, actors.Display(work.DefaultActor),
		ui.Dim(w, "the default: a ticket carrying none of the above"))

	fmt.Fprintf(w, "\n%s\n", ui.Heading(w, "reached another way, deliberately not routable"))
	for _, o := range work.OtherPaths() {
		fmt.Fprintf(w, "  %-*s  %s\n", width, actors.Display(o.Actor), ui.Dim(w, o.Why))
	}
	fmt.Fprintf(w, "\n  %s\n", ui.Dim(w,
		"A label that reached one of these would be a SECOND way to invoke something\n"+
			"  that already has one, and the two drift (OR-176)."))

	fmt.Fprintf(w, "\n  %s\n", ui.Dim(w,
		"Set the marker when the ticket is created. Routing reads what planning wrote\n"+
			"  and infers nothing from the summary; an unmarked ticket takes the default."))
}
