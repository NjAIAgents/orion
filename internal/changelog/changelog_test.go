package changelog

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// repo builds a repository with a changelog and the given fragments.
func repo(t *testing.T, doc string, frags map[string]string) string {
	t.Helper()
	d := t.TempDir()
	if err := os.WriteFile(filepath.Join(d, "CHANGELOG.md"), []byte(doc), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(d, Dir), 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range frags {
		if err := os.WriteFile(filepath.Join(d, Dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return d
}

const existing = `# Changelog

Notable changes per release.

## Unreleased

## v0.6.0

### Added

- The previous release.
`

// The point of the whole mechanism: two tickets, two paths, one changelog.
// Both entries survive, and neither file was ever touched by the other ticket.
func TestCollateMergesTwoTicketsWithoutTouchingOneAnother(t *testing.T) {
	d := repo(t, existing, map[string]string{
		"OR-89.md": "### Changed\n\n- Work items are claimed atomically.\n",
		"OR-99.md": "### Added\n\n- Lessons are proposed rather than dictated.\n",
	})

	res, err := Collate(d, "v0.7.0")
	if err != nil {
		t.Fatalf("collate: %v", err)
	}

	b, err := os.ReadFile(filepath.Join(d, "CHANGELOG.md"))
	if err != nil {
		t.Fatal(err)
	}
	got := string(b)
	for _, want := range []string{
		"## v0.7.0",
		"- Lessons are proposed rather than dictated.",
		"- Work items are claimed atomically.",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("collated changelog is missing %q:\n%s", want, got)
		}
	}

	// Deleted in the same change. A fragment that survives collation is an
	// entry that ships again in the next release.
	for _, f := range res.Removed {
		if _, err := os.Stat(f); !os.IsNotExist(err) {
			t.Errorf("%s survived collation", f)
		}
	}
	left, err := Load(d)
	if err != nil || len(left) != 0 {
		t.Errorf("fragments left behind: %v (%v)", left, err)
	}
}

// keepachangelog order is the reason collation exists rather than being left
// to whoever wrote the entry: Added before Changed before Fixed, whatever
// order the fragments were read in.
func TestCollateEmitsSectionsInKeepachangelogOrder(t *testing.T) {
	d := repo(t, existing, map[string]string{
		"AAA-1.md": "### Fixed\n\n- a fix\n",
		"BBB-2.md": "### Added\n\n- an addition\n",
		"CCC-3.md": "### Security\n\n- a hardening\n",
		"DDD-4.md": "### Changed\n\n- a change\n",
	})
	if _, err := Collate(d, "v1.0.0"); err != nil {
		t.Fatalf("collate: %v", err)
	}
	b, _ := os.ReadFile(filepath.Join(d, "CHANGELOG.md"))
	got := string(b)

	last := -1
	for _, sec := range []string{"### Added", "### Changed", "### Fixed", "### Security"} {
		i := strings.Index(got, sec)
		if i < 0 {
			t.Fatalf("%s missing:\n%s", sec, got)
		}
		if i < last {
			t.Errorf("%s is out of keepachangelog order:\n%s", sec, got)
		}
		last = i
	}
}

// One section, one heading, however many tickets wrote into it. Two `###
// Changed` headings under one version is precisely the defect that reached
// main from a hand-resolved conflict.
func TestCollateMergesSameNamedSections(t *testing.T) {
	d := repo(t, existing, map[string]string{
		"OR-1.md": "### Changed\n\n- first\n",
		"OR-2.md": "### Changed\n\n- second\n",
	})
	if _, err := Collate(d, "v0.7.0"); err != nil {
		t.Fatalf("collate: %v", err)
	}
	b, _ := os.ReadFile(filepath.Join(d, "CHANGELOG.md"))
	section := versionSection(t, string(b), "v0.7.0")
	if n := strings.Count(section, "### Changed"); n != 1 {
		t.Errorf("want one ### Changed heading, got %d:\n%s", n, section)
	}
	if !strings.Contains(section, "- first") || !strings.Contains(section, "- second") {
		t.Errorf("an entry was dropped while merging sections:\n%s", section)
	}
}

// The new version goes where a reader looks for it: under Unreleased, above
// the previous release.
func TestCollateInsertsBelowUnreleasedAndAboveThePreviousRelease(t *testing.T) {
	d := repo(t, existing, map[string]string{"OR-5.md": "### Added\n\n- new\n"})
	if _, err := Collate(d, "v0.7.0"); err != nil {
		t.Fatalf("collate: %v", err)
	}
	b, _ := os.ReadFile(filepath.Join(d, "CHANGELOG.md"))
	got := string(b)
	un, nw, old := strings.Index(got, "## Unreleased"), strings.Index(got, "## v0.7.0"), strings.Index(got, "## v0.6.0")
	if !(un < nw && nw < old) {
		t.Errorf("wrong placement (unreleased %d, new %d, previous %d):\n%s", un, nw, old, got)
	}
}

// A section nobody recognises must stop the release, not be silently dropped.
// The ticket did the work of writing an entry; a release that simply does not
// have it, with nothing saying so, is the failure this mechanism exists to
// prevent.
func TestCollateRefusesAnUnknownSection(t *testing.T) {
	d := repo(t, existing, map[string]string{"OR-7.md": "### Improvements\n\n- something\n"})

	_, err := Collate(d, "v0.7.0")
	if err == nil {
		t.Fatal("an unknown section was accepted")
	}
	if !strings.Contains(err.Error(), "Improvements") {
		t.Errorf("the error does not name the offending section: %v", err)
	}

	// And nothing was written or deleted on the way to failing.
	b, _ := os.ReadFile(filepath.Join(d, "CHANGELOG.md"))
	if string(b) != existing {
		t.Error("CHANGELOG.md was modified by a failed collation")
	}
	if _, err := os.Stat(filepath.Join(d, Dir, "OR-7.md")); err != nil {
		t.Error("the fragment was deleted by a failed collation")
	}
}

// A fragment with no heading cannot be placed. Guessing a section for it would
// put an entry somewhere nobody chose.
func TestParseRefusesContentWithNoSection(t *testing.T) {
	if _, err := Parse("OR-8.md", "- an entry with no section\n"); err == nil {
		t.Fatal("a sectionless fragment was accepted")
	}
}

// .gitkeep keeps the directory in git while it is empty. It is not an entry,
// and reading it as one would fail every collation in a repository that has
// just adopted Orion.
func TestLoadIgnoresNonMarkdown(t *testing.T) {
	d := repo(t, existing, map[string]string{"OR-9.md": "### Fixed\n\n- a fix\n"})
	if err := os.WriteFile(filepath.Join(d, Dir, ".gitkeep"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	frags, err := Load(d)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(frags) != 1 || frags[0].Key != "OR-9" {
		t.Errorf("want just OR-9, got %+v", frags)
	}
}

// Collating the same version twice would append a second section while the
// fragments that produced the first are already gone.
func TestCollateRefusesAVersionAlreadyPresent(t *testing.T) {
	d := repo(t, existing, map[string]string{"OR-3.md": "### Added\n\n- new\n"})
	if _, err := Collate(d, "v0.6.0"); err == nil {
		t.Fatal("collated over an existing version section")
	}
}

func TestCollateRequiresAVersion(t *testing.T) {
	d := repo(t, existing, map[string]string{"OR-4.md": "### Added\n\n- new\n"})
	if _, err := Collate(d, "  "); err == nil {
		t.Fatal("collated without a version")
	}
}

// Multi-paragraph prose is the norm in this changelog -- an entry says what
// changed and what it now refuses to do -- so a fragment must survive
// collation whole.
func TestCollatePreservesMultiParagraphEntries(t *testing.T) {
	body := "### Changed\n\n- The first paragraph of the entry.\n\n  The second, indented under it.\n"
	d := repo(t, existing, map[string]string{"OR-6.md": body})
	if _, err := Collate(d, "v0.7.0"); err != nil {
		t.Fatalf("collate: %v", err)
	}
	b, _ := os.ReadFile(filepath.Join(d, "CHANGELOG.md"))
	if !strings.Contains(string(b), "  The second, indented under it.") {
		t.Errorf("the entry lost a paragraph:\n%s", b)
	}
}

// A ticket that shipped without a fragment has to be visible at release.
func TestUnrecordedNamesTicketsWithNoFragment(t *testing.T) {
	got := Unrecorded([]string{"OR-89", "OR-90", "OR-99"}, []string{"OR-89", "or-99"})
	if len(got) != 1 || got[0] != "OR-90" {
		t.Errorf("want [OR-90], got %v", got)
	}
}

func TestPath(t *testing.T) {
	if got, want := Path("/repo", " or-113 "), filepath.Join("/repo", Dir, "OR-113.md"); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// versionSection returns the text under one `## version` heading.
func versionSection(t *testing.T, doc, version string) string {
	t.Helper()
	start := strings.Index(doc, "## "+version)
	if start < 0 {
		t.Fatalf("no %s section in:\n%s", version, doc)
	}
	rest := doc[start+len("## "+version):]
	if end := strings.Index(rest, "\n## "); end >= 0 {
		return rest[:end]
	}
	return rest
}
