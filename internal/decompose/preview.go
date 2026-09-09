package decompose

// The preview, and why it is the whole tree rather than a count.
//
// /pm-plan's guarantee is that nothing is created until a human has seen
// everything that would be created, once, and said yes. That guarantee is
// the reason a tree can be trusted at all: a sandboxed workspace can be
// deleted, but issues in a shared tracker are seen by other people and
// cannot be cleanly withdrawn. So this prints every item, marks which are
// new and which a previous run already made, and the caller asks once.

import (
	"fmt"
	"io"
	"strings"
)

// Preview writes the whole tree, new versus existing marked.
func Preview(w io.Writer, p *Plan) {
	fmt.Fprintf(w, "%s -> %s (%s)\n\n", p.Tree.Source, p.Project, p.Backend)

	// Blank lines between the story groups: 94 items in one block is a wall,
	// and the groups are what a reader is deciding about.
	lastStory := ""
	for _, s := range p.Steps {
		indent := "  "
		switch s.Item.Kind {
		case KindStory:
			indent = "    "
			if lastStory != "" {
				fmt.Fprintln(w)
			}
			lastStory = s.Item.Summary
		case KindTask:
			indent = "      "
			if s.Parent != nil && s.Parent.Kind == KindEpic {
				// An ungrouped task hangs off the epic, so it is drawn at
				// the level it will actually sit at rather than under the
				// last story printed.
				indent = "    "
				if lastStory != "" {
					fmt.Fprintln(w)
					lastStory = ""
				}
			}
		}
		mark, key := "+", ""
		if !s.New() {
			mark, key = "=", " ("+s.ExistingKey+")"
		}
		fmt.Fprintf(w, "%s%s %s%s%s%s\n", indent, mark, s.Item.Summary, key, humanNote(s.Item), labelNote(s.Item))
	}

	fmt.Fprintf(w, "\n  %d to create, %d already in %s  (%s)\n",
		p.NewCount(), p.ExistingCount(), p.Project, p.Tree.Label())
	if n := humanCount(p); n > 0 {
		fmt.Fprintf(w, "  %d of them are [human]: created as tickets, never offered to an agent.\n", n)
	}
	previewBlocks(w, p.Tree.Blocks)
	previewCoupled(w, p.Tree.Coupled)
}

// previewCoupled names the siblings that declared the same ground, at the
// point a human is deciding whether to create the tree (OR-260).
//
// BEFORE CREATION IS THE ONLY USEFUL MOMENT. Once these exist as siblings the
// queue will refuse to admit them together for the rest of their lives, and no
// downstream stage can undo a decomposition. It is stated, not blocked on: a
// coupled pair is sometimes exactly right, and a parser cannot tell which of
// merge, sequence or accept the reader wants.
func previewCoupled(w io.Writer, coupled []Coupling) {
	if len(coupled) == 0 {
		return
	}
	fmt.Fprintf(w, "\n  %d coupled pair(s): these declare the same ground and will not be\n"+
		"  admitted to one batch. Merge them, order them with a blocking link, or\n"+
		"  accept the coupling -- but decide it now rather than at merge time.\n",
		len(coupled))
	for _, c := range coupled {
		fmt.Fprintf(w, "    ! %s  +  %s\n      %s\n",
			c.A, c.B, sharedNote(c.Shared))
	}
}

// labelNote shows the routing marker, which is the part of an item a reader
// cannot infer from its summary and the part that decides who works it.
func labelNote(it *Item) string {
	var markers []string
	for _, l := range it.Labels {
		if !strings.HasPrefix(l, "orion-spec-") {
			markers = append(markers, l)
		}
	}
	if len(markers) == 0 {
		return ""
	}
	return "  [" + strings.Join(markers, " ") + "]"
}

// sharedNote names the ground two stories share, at a length a reader
// takes in: the first few files, then how many more.
func sharedNote(shared []string) string {
	const show = 3
	if len(shared) <= show {
		return strings.Join(shared, ", ")
	}
	return fmt.Sprintf("%s, and %d more", strings.Join(shared[:show], ", "), len(shared)-show)
}

// humanNote marks an item no agent will be offered, so a reader sees before
// creating the tree which work is waiting on a person.
func humanNote(it *Item) string {
	if it.Human {
		return "  [human]"
	}
	return ""
}

func humanCount(p *Plan) int {
	n := 0
	for _, s := range p.Steps {
		if s.Item.Human {
			n++
		}
	}
	return n
}

// previewBlocks says what the queue will be able to read, before anything
// is created: which task waits on which, from the artifact's own
// Dependencies section.
func previewBlocks(w io.Writer, edges []Edge) {
	if len(edges) == 0 {
		fmt.Fprintf(w, "\n  No ordering links: the task list states no dependencies, so every\n"+
			"  item is startable at once.\n")
		return
	}
	// Grouped by blocker, which is how a reader thinks about it: "what does
	// T012 hold up".
	order := []string{}
	by := map[string][]string{}
	for _, e := range edges {
		if _, ok := by[e.Blocker]; !ok {
			order = append(order, e.Blocker)
		}
		by[e.Blocker] = append(by[e.Blocker], e.Blocked)
	}
	fmt.Fprintf(w, "\n  %d ordering link(s), from the task list's Dependencies section --\n"+
		"  the queue will not start a task while its blocker is open:\n", len(edges))
	for _, blocker := range order {
		fmt.Fprintf(w, "    %s blocks %s\n", blocker, joinCapped(by[blocker], 6))
	}
}

// joinCapped lists ids, naming a few and counting the rest.
func joinCapped(ids []string, n int) string {
	if len(ids) <= n {
		return strings.Join(ids, ", ")
	}
	return fmt.Sprintf("%s and %d more", strings.Join(ids[:n], ", "), len(ids)-n)
}
