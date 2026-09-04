package decompose

import (
	"bytes"
	"strings"
	"testing"
)

// The preview is what the one confirmation is given against, so it has to
// show every item and which of them already exist. A count would be a
// promise nobody can check.
func TestPreviewShowsTheWholeTreeAndWhatIsAlreadyThere(t *testing.T) {
	f := newFake()
	f.have = map[string]string{
		"T001 Create the project structure per implementation plan": "CAT-9",
	}
	tree := parseFixture(t)
	p, err := Build(tree, f, "CAT")
	if err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	Preview(&out, p)
	got := out.String()

	lines := 0
	_ = tree.Walk(func(it, _ *Item) error {
		if !strings.Contains(got, it.Summary) {
			t.Errorf("the preview omits %q; a tree nobody saw is not a tree they approved", it.Summary)
		}
		lines++
		return nil
	})
	if lines != 8 {
		t.Fatalf("walked %d items", lines)
	}

	if !strings.Contains(got, "= task  T001 Create the project structure per implementation plan (CAT-9)") {
		t.Errorf("an item already in the tracker must be marked as linked, with its key:\n%s", got)
	}
	if !strings.Contains(got, "7 to create, 1 already in CAT") {
		t.Errorf("the counts must state both halves:\n%s", got)
	}
	if !strings.Contains(got, "identity label: orion-spec-product-catalogue") {
		t.Errorf("the identity label is what makes a re-run safe; show it:\n%s", got)
	}
	if !strings.Contains(got, "[documentation]") {
		t.Errorf("the routing marker decides who works an item and cannot be inferred from\n"+
			"its summary, so the preview must show it:\n%s", got)
	}
	if strings.Contains(got, "[orion-spec-") {
		t.Errorf("the identity label is noise beside every row; show it once:\n%s", got)
	}
	if !strings.Contains(got, "specs/003-product-catalogue/tasks.md -> CAT (fake)") {
		t.Errorf("the preview must name the source, the destination and the tracker:\n%s", got)
	}
}
