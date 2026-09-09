package provision

// Installing spec-kit into the workspace repository (docs/decisions/0022).
//
// spec-kit is not a skills repository to clone: `specify init` writes its
// commands into the project as .claude/skills/speckit-*/SKILL.md, from
// templates bundled inside the CLI. So it is installed per project, by the
// chain, before the first stage that would read it -- a frame step in
// Orion's own process, the same class of provisioning as creating the
// remote. Once is enough: a resumed chain finds .specify/ and moves on.

import (
	"embed"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// orionPreset is the preset Orion installs into every spec-kit project
// (docs/decisions/0021): a wrap of /speckit-specify that removes its
// best-guess rule, its marker cap and its interactive clarification loop,
// and a spec template with the Open questions section the discovery gate
// reads. Embedded so a workspace never needs the network or a clone to get
// it; materialised to a temporary directory for `specify preset add --dev`,
// which copies it into the project's own .specify/presets/orion/.
//
//go:embed presets/orion
var orionPreset embed.FS

// PresetID is the preset's id, and the directory spec-kit records it under.
const PresetID = "orion"

// SpecKitInstall is how the `specify` CLI is installed on a machine that
// lacks it. Named in every message that finds it missing, so the fix is on
// screen rather than in a manual.
const SpecKitInstall = "uv tool install specify-cli --from git+https://github.com/github/spec-kit.git@" + SpecKitTag

// SpecKitTag is the spec-kit release Orion's gates were written against:
// the skill names, the [NEEDS CLARIFICATION] marker, the Critical Issues
// Count line and the constitution template's slots are all read from what
// this release installs (speckit_contract_test.go pins them). A machine
// provisioned later gets the same release, not whatever main holds that
// day; moving the pin is a change here, checked by that test.
const SpecKitTag = "v1.0.4"

// SpecKitReinstall brings an installed CLI to the pinned release. `uv tool
// upgrade` re-resolves the same tag and does nothing, so a reinstall is the
// verb.
const SpecKitReinstall = "uv tool install --reinstall specify-cli --from git+https://github.com/github/spec-kit.git@" + SpecKitTag

// wrapMarker is the first heading of every wrap the orion preset carries,
// and the one thing that proves an installed skill was composed with it.
// The preset's registration under .specify/presets/ is not that proof:
// spec-kit recomposes skills on its own paths, and the registration
// outlives the composition.
const wrapMarker = "## Orion runs this headless"

// wrappedSkills are the skills the preset wraps. EVERY ONE of them has to
// carry the marker, not just the first: the preset gained its tasks wrap
// after some projects were already provisioned, and a check that looked at
// speckit-specify alone reported those projects up to date while the
// command whose output Orion parses was still spec-kit's own (OR-411).
var wrappedSkills = []string{"speckit-specify", "speckit-tasks"}

// PresetApplied reports whether every skill the preset wraps carries the
// wrap -- the chain's toolkit step is not done until they all do.
func PresetApplied(dir string) bool {
	for _, name := range wrappedSkills {
		b, err := os.ReadFile(filepath.Join(dir, ".claude", "skills", name, "SKILL.md"))
		if err != nil || !strings.Contains(string(b), wrapMarker) {
			return false
		}
	}
	return true
}

// SpecKitDir is the directory spec-kit keeps its own state in, and the
// signal that a project has been initialised.
const SpecKitDir = ".specify"

// InitSpecKit initialises spec-kit in dir and commits what it wrote.
// Returns whether it did anything: false when the project is already
// initialised, which is the resumed-chain case and not an error.
//
// Non-interactive on purpose. `specify init` chooses an integration by
// prompt when it can, and a chain has nobody at the prompt; --non-interactive
// makes it use the named integration and fail rather than hang when a choice
// has no default. --force is required to initialise a directory that is not
// empty, which a provisioned workspace never is.
func InitSpecKit(dir string) (bool, error) {
	needInit := !isDir(filepath.Join(dir, SpecKitDir))
	// Judged by the composed skill, not by the registration: a skill that
	// lost the wrap runs spec-kit's best-guess rule, which is exactly what
	// the preset exists to remove, and the registration would still say
	// "installed".
	needPreset := !PresetApplied(dir)
	if !needInit && !needPreset {
		return false, nil
	}
	bin, err := exec.LookPath("specify")
	if err != nil {
		return false, fmt.Errorf("the specify CLI is not on PATH, and this project's stages delegate to spec-kit.\n"+
			"  Install it:  %s\n"+
			"  Then re-run: orion plan", SpecKitInstall)
	}
	if needInit {
		if out, err := specify(bin, dir, "init", "--here", "--force", "--non-interactive", "--integration", "claude"); err != nil {
			return false, fmt.Errorf("specify init failed in %s: %v\n%s", dir, err, out)
		}
		if !isDir(filepath.Join(dir, SpecKitDir)) {
			return false, fmt.Errorf("specify init exited 0 but left no %s/ in %s", SpecKitDir, dir)
		}
	}
	// The preset, after init and whether or not init just ran: an existing
	// project initialised by hand gets it too. From a temporary copy of the
	// embedded files, because `preset add --dev` copies from the path it is
	// given into the project, and copying the project's own directory onto
	// itself is not a thing to ask an installer to do.
	if needPreset {
		tmp, err := os.MkdirTemp("", "orion-preset-")
		if err != nil {
			return needInit, err
		}
		defer os.RemoveAll(tmp)
		if err := materialise(orionPreset, "presets/"+PresetID, tmp); err != nil {
			return needInit, fmt.Errorf("writing the %s preset: %w", PresetID, err)
		}
		// A registration without the composition -- the skill was
		// reinstalled around it -- has to be removed first: `preset add`
		// refuses an id it already knows, and nothing recomposes on its own.
		if exists(filepath.Join(dir, SpecKitDir, "presets", PresetID, "preset.yml")) {
			if out, err := specify(bin, dir, "preset", "remove", PresetID); err != nil {
				return needInit, fmt.Errorf("specify preset remove failed in %s: %v\n%s", dir, err, out)
			}
		}
		if out, err := specify(bin, dir, "preset", "add", "--dev", tmp); err != nil {
			return needInit, fmt.Errorf("specify preset add failed in %s: %v\n%s", dir, err, out)
		}
		if !PresetApplied(dir) {
			return needInit, fmt.Errorf("specify preset add exited 0 but .claude/skills/speckit-specify/SKILL.md does not carry the %s wrap", PresetID)
		}
	}

	// Committed, because the handoff between stages is tracked files and
	// the next stage's agent runs against the repository, not this
	// worktree. Nothing to commit is not an error: an installer that wrote
	// only ignored files has still installed.
	if out, err := git(dir, "add", "-A"); err != nil {
		return true, fmt.Errorf("staging what specify init wrote: %s", out)
	}
	if _, err := git(dir, "diff", "--cached", "--quiet"); err == nil {
		return true, nil
	}
	if out, err := git(dir, "commit", "-q", "-m",
		"chore: install spec-kit into the workspace\n\n"+
			"Written by `specify init --here --integration claude`, run by Orion before\n"+
			"the first stage that reads it (docs/decisions/0022)."); err != nil {
		return true, fmt.Errorf("committing what specify init wrote: %s", out)
	}
	return true, nil
}

// specify runs one specify command in dir and returns its combined output.
func specify(bin, dir string, args ...string) (string, error) {
	cmd := exec.Command(bin, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// materialise copies an embedded directory tree to dst.
func materialise(src fs.FS, root, dst string) error {
	return fs.WalkDir(src, root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, filepath.FromSlash(p))
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		b, err := fs.ReadFile(src, p)
		if err != nil {
			return err
		}
		return os.WriteFile(target, b, 0o644)
	})
}

func isDir(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}
