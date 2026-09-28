package work

import "strings"

// Notes from a person (OR-571).
//
// An operator who diagnoses a failed ticket writes it where a person
// would: a comment. The prompts carried only the summary and description,
// so on 2026-09-28 LTA-141 and LTA-150 were re-queued with a written fix and
// both re-runs started blind. The recent comments a PERSON wrote now ride
// along with the description, into the implement, QA and case prompts alike.

const (
	maxNotes       = 3    // the most recent; an older note is usually superseded
	maxNoteRunes   = 2000 // one note, so a pasted log cannot crowd out the ticket
	notesHeading   = "NOTES FROM A PERSON ON THIS TICKET (newest last)"
	orionAgentLine = ", an Orion agent:"
)

// orionWrote reports whether a comment is Orion's own. Every comment Orion
// posts opens with its author and a colon on a line of its own (actors.Comment):
// "Orion:", "ci:", or "<name> · <role>, an Orion agent:".
func orionWrote(comment string) bool {
	first, _, _ := strings.Cut(strings.TrimSpace(comment), "\n")
	first = strings.TrimSpace(first)
	return first == "Orion:" || first == "ci:" || strings.HasSuffix(first, orionAgentLine)
}

// withPersonNotes returns the description with the most recent notes a
// person left, or the description unchanged when there are none.
func withPersonNotes(description string, comments []string) string {
	var notes []string
	for _, c := range comments {
		if !orionWrote(c) {
			notes = append(notes, c)
		}
	}
	if len(notes) == 0 {
		return description
	}
	if len(notes) > maxNotes {
		notes = notes[len(notes)-maxNotes:]
	}
	var b strings.Builder
	b.WriteString(strings.TrimRight(description, "\n"))
	b.WriteString("\n\n" + notesHeading + "\n")
	for _, n := range notes {
		if r := []rune(n); len(r) > maxNoteRunes {
			n = string(r[:maxNoteRunes]) + "…"
		}
		b.WriteString("\n- " + strings.ReplaceAll(strings.TrimSpace(n), "\n", "\n  ") + "\n")
	}
	return b.String()
}
