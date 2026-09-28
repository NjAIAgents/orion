package tracker

import "testing"

// OR-558. The marker is a line anchor, not a word search: it must fire only
// at the start of a line (optional leading whitespace), only on the exact
// case decompose writes, and only on the whole word HUMAN -- not a prefix of
// a longer one. Get any of these wrong and either a person's task closes as
// delivered, or an ordinary one is held forever.
func TestHumanMarkerBoundaryRules(t *testing.T) {
	for _, tc := range []struct {
		name string
		desc string
		want bool
	}{
		{"uppercase at line start matches", "HUMAN: register the OIDC client.", true},
		{"leading spaces before the marker still match", "   HUMAN: register the OIDC client.", true},
		{"leading tab before the marker still match", "\tHUMAN: register the OIDC client.", true},
		{"trailing content on the same line is fine", "HUMAN: the client id is recorded in the runbook.", true},
		{"no colon or text after HUMAN still matches (word boundary alone)", "HUMAN", true},
		{"marker on its own line inside a longer description matches", "Run quickstart scenario 1.\n\nHUMAN: a person picks it up.\n", true},
		{"lowercase does not match", "human: register the OIDC client.", false},
		{"mixed case does not match", "Human: register the OIDC client.", false},
		{"mid-line HUMAN does not match", "Please note, HUMAN: register the client.", false},
		{"HUMANOID is a different word and must not match", "HUMANOID protocol version negotiation.", false},
		{"HUMANE is a different word and must not match", "HUMANE, the vendor, publishes the schema.", false},
		{"empty description does not match", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := HumanOnly(Issue{Description: tc.desc}); got != tc.want {
				t.Errorf("HumanOnly(%q) = %v, want %v", tc.desc, got, tc.want)
			}
		})
	}
}

// A sub-task Orion never wrote a description for (a tracker default, or an
// issue created by hand with an empty body) must read as an ordinary task,
// not crash and not read as a person's task by accident.
func TestHumanOnlyOnAnIssueWithNoDescriptionDoesNotCrashAndIsNotHuman(t *testing.T) {
	if HumanOnly(Issue{Key: "OR-1"}) {
		t.Error("an issue with a zero-value (empty) description read as HUMAN-marked")
	}
}

// The regex must not depend on the byte width of what surrounds it: a
// description containing multi-byte Unicode text elsewhere must not throw
// off the line anchor or the case-sensitive match for the marker itself.
func TestHumanMarkerDetectionIsUnaffectedByUnicodeElsewhereInTheDescription(t *testing.T) {
	human := Issue{Description: "Contacter l'équipe pour la clé API : 🔑\n\n" +
		"HUMAN: the task list marks this as work no agent can do.\n" +
		"Confirmer avec le réviseur (日本語のメモ) avant de fermer.\n"}
	if !HumanOnly(human) {
		t.Errorf("Unicode content elsewhere in the description broke marker detection:\n%s", human.Description)
	}

	// A line that LOOKS like the marker but is prefixed by a non-ASCII
	// character is not a match at the start of that line -- \s in this
	// pattern is ASCII whitespace, and a full-width space is not leading
	// whitespace by this rule.
	notAtLineStart := Issue{Description: "日本語　HUMAN: this is not at a line start.\n"}
	if HumanOnly(notAtLineStart) {
		t.Errorf("a marker preceded by non-whitespace text on the same line matched:\n%s",
			notAtLineStart.Description)
	}
}

// The regex is a package-level var, compiled once, so this only proves
// correctness scales with input size rather than blowing up on it --
// exactly what an O(n) line-anchored match should do on a long description.
func TestHumanMarkerDetectionHandlesALongDescriptionWithoutMisbehaving(t *testing.T) {
	body := ""
	for i := 0; i < 5000; i++ {
		body += "An ordinary line of work, not a marker, number filler here.\n"
	}
	if HumanOnly(Issue{Description: body}) {
		t.Error("a long description with no marker line was read as HUMAN-marked")
	}
	body += "HUMAN: found at the very end of a long description.\n"
	if !HumanOnly(Issue{Description: body}) {
		t.Error("a marker at the end of a long description was missed")
	}
}
