package adopt

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/orion-sdlc/orion/internal/changelog"
)

// Adoption scaffolds the changelog fragment directory.
//
// Without it the implementer prompt says nothing about fragments -- it is
// conditional on the directory existing, so it never names a mechanism the
// repository lacks -- every ticket goes back to editing CHANGELOG.md, and two
// branches in flight conflict there again.
//
// The .gitkeep is what carries it: fragments are meant to be COMMITTED, since
// a fragment IS the changelog entry, and an empty directory does not reach the
// remote on its own.
func TestAdoptScaffoldsTheChangelogFragmentDirectory(t *testing.T) {
	d := repo(t)
	res, err := Run(Options{Dir: d, Binary: "/usr/local/bin/orion"})
	if err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(filepath.Join(d, changelog.Dir)); err != nil || !fi.IsDir() {
		t.Fatalf("%s/ was not created: %v", changelog.Dir, err)
	}
	if _, err := os.Stat(filepath.Join(d, changelog.Dir, ".gitkeep")); err != nil {
		t.Errorf("%s/.gitkeep is missing, so git will not track the directory", changelog.Dir)
	}
	if !strings.Contains(strings.Join(res.Created, " "), changelog.Dir) {
		t.Errorf("the directory was created without saying so: %v", res.Created)
	}

	// It must not be ignored. An ignored fragment is a changelog entry that
	// never reaches the release.
	b, _ := os.ReadFile(filepath.Join(d, ".gitignore"))
	for _, line := range strings.Split(string(b), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.Contains(trimmed, changelog.Dir) && !strings.HasPrefix(trimmed, "!") {
			t.Errorf(".gitignore ignores the fragment directory: %q", line)
		}
	}
}

// A re-run must not report it again, the same as every other artifact
// directory.
func TestAdoptIsIdempotentAboutTheFragmentDirectory(t *testing.T) {
	d := repo(t)
	if _, err := Run(Options{Dir: d, Binary: "/usr/local/bin/orion"}); err != nil {
		t.Fatal(err)
	}
	res, err := Run(Options{Dir: d, Binary: "/usr/local/bin/orion"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.Join(res.Created, " "), changelog.Dir) {
		t.Errorf("a second run reported the directory as created: %v", res.Created)
	}
}
