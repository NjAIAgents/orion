package njagents

import (
	"path/filepath"
	"testing"
)

// OR-296: the vendor directory is derived from the repository name so a
// second toolkit never lands on the first clone. These cases exercise
// VendorDir/VendorDirFor/repoLeaf directly, one behaviour per test.

func TestVendorDirDefaultsToNJAgents(t *testing.T) {
	home := filepath.Join("home", "orion")
	want := filepath.Join(home, "vendor", "nj-agents")
	if got := VendorDir(home); got != want {
		t.Errorf("VendorDir(%q) = %q, want %q", home, got, want)
	}
}

func TestVendorDirForeignRepoResolvesToItsOwnLeaf(t *testing.T) {
	home := filepath.Join("home", "orion")
	want := filepath.Join(home, "vendor", "spec-kit")
	if got := VendorDirFor(home, "https://github.com/github/spec-kit.git"); got != want {
		t.Errorf("VendorDirFor(spec-kit) = %q, want %q", got, want)
	}
}

func TestVendorDirScpLikeRepoExtractsNameAfterColon(t *testing.T) {
	home := filepath.Join("home", "orion")
	want := filepath.Join(home, "vendor", "house-skills")
	if got := VendorDirFor(home, "git@github.com:acme/house-skills.git"); got != want {
		t.Errorf("VendorDirFor(scp-like) = %q, want %q", got, want)
	}
}

func TestVendorDirTwoDifferentReposNeverCollide(t *testing.T) {
	home := filepath.Join("home", "orion")
	a := VendorDirFor(home, RepoURL)
	b := VendorDirFor(home, "https://github.com/github/spec-kit.git")
	if a == b {
		t.Errorf("nj-agents and spec-kit both resolved to %q", a)
	}
}

func TestVendorDirTrailingSlashIsHandled(t *testing.T) {
	home := filepath.Join("home", "orion")
	want := filepath.Join(home, "vendor", "kit")
	if got := VendorDirFor(home, "https://example.com/kits/kit/"); got != want {
		t.Errorf("VendorDirFor(trailing slash) = %q, want %q", got, want)
	}
}

func TestVendorDirGitSuffixIsStripped(t *testing.T) {
	home := filepath.Join("home", "orion")
	withSuffix := VendorDirFor(home, "https://github.com/github/spec-kit.git")
	withoutSuffix := VendorDirFor(home, "https://github.com/github/spec-kit")
	if withSuffix != withoutSuffix {
		t.Errorf(".git suffix changed the leaf: %q vs %q", withSuffix, withoutSuffix)
	}
	want := filepath.Join(home, "vendor", "spec-kit")
	if withSuffix != want {
		t.Errorf("VendorDirFor(.git) = %q, want %q", withSuffix, want)
	}
}

func TestVendorDirMultiplePathSegmentsTakesTheLastOne(t *testing.T) {
	home := filepath.Join("home", "orion")
	want := filepath.Join(home, "vendor", "name")
	if got := VendorDirFor(home, "https://example.com/kits/kit/name"); got != want {
		t.Errorf("VendorDirFor(multi-segment) = %q, want %q", got, want)
	}
}

func TestVendorDirEmptyRepoFallsBackToToolkitLeaf(t *testing.T) {
	home := filepath.Join("home", "orion")
	want := filepath.Join(home, "vendor", "toolkit")
	if got := VendorDirFor(home, ""); got != want {
		t.Errorf("VendorDirFor(\"\") = %q, want %q -- an empty leaf would make the vendor dir itself a repo", got, want)
	}
}

func TestVendorDirInvalidRepoFallsBackToToolkitLeaf(t *testing.T) {
	home := filepath.Join("home", "orion")
	want := filepath.Join(home, "vendor", "toolkit")
	for _, bad := range []string{".", "..", "   ", "/"} {
		if got := VendorDirFor(home, bad); got != want {
			t.Errorf("VendorDirFor(%q) = %q, want %q", bad, got, want)
		}
	}
}
