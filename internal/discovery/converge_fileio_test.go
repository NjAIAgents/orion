package discovery

// The two file-shape cases addQuestions has to handle that converge_test.go's
// "no section" / "empty file" / "appended inside section" cases do not:
// a path with no file at all (as opposed to an empty file that exists), and a
// section heading with nothing under it yet.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A path that has never been written to has no file to read -- addQuestions
// must create it rather than treat the missing file as an error, since the
// discovery round has to be able to write its first question before anything
// else has touched the intent file.
func TestIntentFileIsCreatedWhenMissing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "intent.md")
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("precondition: %s must not exist yet", path)
	}

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
		t.Errorf("the question was not written into the created file:\n%s", body)
	}
}

// A heading that exists with no bullets under it yet -- the section was
// created (by a person, or by a prior round that then had its question
// answered and removed) but is currently empty. A new question must land
// inside it, not push a duplicate heading or drop the question elsewhere.
func TestQuestionsAppendUnderAnEmptyOpenQuestionsSection(t *testing.T) {
	path := write(t, `# Intent
Something.

## Open questions

## Notes
- unrelated bullet
`)
	c := Converge(path, 1, func(int) []Added {
		return []Added{{Agent: "analyst", Questions: []string{"who are the users?"}}}
	}, nil)

	if c.Open != 1 {
		t.Fatalf("Open = %d, want 1:\n%s", c.Open, readIntent(t, path))
	}
	body := readIntent(t, path)
	if strings.Count(body, "## Open questions") != 1 {
		t.Errorf("a second Open questions section was created:\n%s", body)
	}
	if strings.Index(body, "who are the users?") > strings.Index(body, "## Notes") {
		t.Errorf("the question was written outside the section:\n%s", body)
	}
	if strings.Count(body, "unrelated bullet") != 1 {
		t.Errorf("the rest of the file was not left alone:\n%s", body)
	}
}
