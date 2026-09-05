package discovery

// Cases assigned under OR-152: where a question actually lands in the intent
// file -- inside "## Open questions" specifically, created if missing, after
// the section's own bullets rather than after a later heading, and correctly
// even when the file already has real content but no section yet.

import (
	"strings"
	"testing"
)

// A question lands under "## Open questions" and nowhere else, even when the
// file has other headings both before and after that section.
func TestQuestionLandsUnderOpenQuestionsHeadingSpecifically(t *testing.T) {
	path := write(t, `# Intent

## Background
- some context, not a question

## Open questions
- What is the retention period?

## Constraints
- Must not change the public API
`)
	c := Converge(path, 1, func(int) []Added {
		return []Added{{Agent: "analyst", Questions: []string{"who are the users?"}}}
	}, nil)

	if c.Open != 2 {
		t.Fatalf("Open = %d, want 2", c.Open)
	}
	body := readIntent(t, path)
	openIdx := strings.Index(body, "## Open questions")
	constraintsIdx := strings.Index(body, "## Constraints")
	newQIdx := strings.Index(body, "who are the users?")
	if newQIdx < openIdx || newQIdx > constraintsIdx {
		t.Errorf("question was not written inside the Open questions section:\n%s", body)
	}
	if strings.Count(body, "Must not change the public API") != 1 {
		t.Errorf("an unrelated section was disturbed:\n%s", body)
	}
}

// A file with no "## Open questions" section at all gets one created, rather
// than the question being dropped or written somewhere Assess never looks.
func TestOpenQuestionsSectionIsCreatedWhenMissing(t *testing.T) {
	path := write(t, "# Intent\n\nSome existing content about the feature.\n")
	c := Converge(path, 1, func(int) []Added {
		return []Added{{Agent: "analyst", Questions: []string{"who are the users?"}}}
	}, nil)

	if c.Open != 1 {
		t.Fatalf("Open = %d, want 1", c.Open)
	}
	body := readIntent(t, path)
	if !strings.Contains(body, "## Open questions") {
		t.Errorf("no Open questions section was created:\n%s", body)
	}
	if !strings.Contains(body, "- who are the users?") {
		t.Errorf("the question is not in the created section:\n%s", body)
	}
}

// New questions go after the section's own last bullet, not after a later
// heading -- a question written past the section boundary is one Assess
// never counts, which is the gate silently letting something unanswered
// through.
func TestQuestionsAppendAfterExistingBulletsNotAfterLaterHeadings(t *testing.T) {
	path := write(t, `# Intent

## Open questions
- What is the retention period?
- Which tenant model?

## Notes
- unrelated bullet
- another unrelated bullet
`)
	c := Converge(path, 1, func(int) []Added {
		return []Added{{Agent: "analyst", Questions: []string{"who are the users?"}}}
	}, nil)

	if c.Open != 3 {
		t.Fatalf("Open = %d, want 3", c.Open)
	}
	body := readIntent(t, path)
	lastExistingIdx := strings.Index(body, "Which tenant model?")
	newQIdx := strings.Index(body, "who are the users?")
	notesIdx := strings.Index(body, "## Notes")
	if newQIdx < lastExistingIdx {
		t.Errorf("new question was written before the section's existing bullets:\n%s", body)
	}
	if newQIdx > notesIdx {
		t.Errorf("new question was written after the later heading, not inside the section:\n%s", body)
	}
	if strings.Count(body, "- unrelated bullet\n") != 1 || strings.Count(body, "- another unrelated bullet\n") != 1 {
		t.Errorf("bullets under a later heading were disturbed:\n%s", body)
	}
}

// A file that has real content -- prose, an existing heading -- but no Open
// questions section still gets the section appended correctly, with the
// existing content left exactly as it was.
func TestFileWithContentButNoSectionGetsSectionAppendedCorrectly(t *testing.T) {
	path := write(t, `# Intent

The feature lets customers see their claim status in the portal.

## Constraints
- Must not introduce new PII
- Use the existing authentication only
`)
	c := Converge(path, 1, func(int) []Added {
		return []Added{{Agent: "analyst", Questions: []string{"who are the users?"}}}
	}, nil)

	if c.Open != 1 {
		t.Fatalf("Open = %d, want 1", c.Open)
	}
	body := readIntent(t, path)
	if !strings.Contains(body, "The feature lets customers see their claim status in the portal.") {
		t.Errorf("existing prose was not preserved:\n%s", body)
	}
	if !strings.Contains(body, "Must not introduce new PII") || !strings.Contains(body, "Use the existing authentication only") {
		t.Errorf("existing constraints were not preserved:\n%s", body)
	}
	if !strings.Contains(body, "## Open questions") {
		t.Errorf("no Open questions section was appended:\n%s", body)
	}
	if !strings.Contains(body, "- who are the users?") {
		t.Errorf("the question is not in the appended section:\n%s", body)
	}
}
