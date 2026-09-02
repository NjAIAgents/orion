package njagents

import (
	"path/filepath"
	"testing"
)

// OR-296: with a project able to declare its own toolkit repository, a fixed
// vendor leaf means the second clone lands on the first -- same path,
// different repository, no error.
func TestVendorDirIsDerivedFromTheRepoName(t *testing.T) {
	home := filepath.Join("home", "orion")

	if got, want := VendorDir(home), filepath.Join(home, "vendor", "nj-agents"); got != want {
		t.Errorf("the default toolkit must not move: %q, want %q", got, want)
	}

	cases := map[string]string{
		"https://github.com/github/spec-kit.git": "spec-kit",
		"https://github.com/github/spec-kit":     "spec-kit",
		"git@github.com:acme/house-skills.git":   "house-skills",
		"https://example.com/kits/kit/":          "kit",
		"":                                       "toolkit", // never the vendor directory itself, which would make it a repo
	}
	for repo, leaf := range cases {
		want := filepath.Join(home, "vendor", leaf)
		if got := VendorDirFor(home, repo); got != want {
			t.Errorf("VendorDirFor(%q) = %q, want %q", repo, got, want)
		}
	}

	if VendorDirFor(home, RepoURL) == VendorDirFor(home, "https://github.com/github/spec-kit.git") {
		t.Error("two different toolkits must never share a clone directory")
	}
}
