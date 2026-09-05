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

	for _, s := range p.Steps {
		indent := "  "
		switch s.Item.Kind {
		case KindStory:
			indent = "    "
		case KindTask:
			indent = "      "
			if s.Parent != nil && s.Parent.Kind == KindEpic {
				// An ungrouped task hangs off the epic, so it is drawn at
				// the level it will actually sit at rather than under the
				// last story printed.
				indent = "    "
			}
		}
		mark, key := "+", ""
		if !s.New() {
			mark, key = "=", " ("+s.ExistingKey+")"
		}
		fmt.Fprintf(w, "%s%s %-5s %s%s%s\n", indent, mark, s.Item.Kind, s.Item.Summary, key, labelNote(s.Item))
	}

	fmt.Fprintf(w, "\n  %d to create, %d already in %s\n",
		p.NewCount(), p.ExistingCount(), p.Project)
	fmt.Fprintf(w, "  identity label: %s\n", p.Tree.Label())
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
		fmt.Fprintf(w, "    ! %s\n      %s\n      both declare: %s\n",
			c.A, c.B, strings.Join(c.Shared, ", "))
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
