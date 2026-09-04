package supervisor

// The artifact gate: a stage that exits 0 without leaving its artifact has
// not finished.
//
// stagePrompt's contract is that the artifact is the HANDOFF -- the next
// stage reads files, not conversation -- so an unwritten artifact breaks the
// chain silently. Silently is the whole problem: a toolkit block naming a
// skill that does not exist produces an agent that runs, finds no such skill,
// says so in prose and exits 0. Nothing downstream looks at prose, so the
// first visible symptom is a later stage reading a file that was never
// written, several stages away from the orion.json line that caused it.
//
// WHICH artifact a stage owes is decided here, in Go, and cannot be changed
// from orion.json. Orion owns artifact paths (docs/decisions/0001); a toolkit
// declares only the COMMAND a stage runs. The configured directories in
// cfg.Paths still apply -- they are where a project keeps its chain, which is
// Orion's own setting, not a toolkit's.

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/orion-sdlc/orion/internal/config"
)

// stageArtifact returns the repo-relative file a stage owes, or "" when it
// owes none.
//
// The three stages that owe one are the three whose prompts end "write X and
// commit it": intent, spec and plan. Everything else is skipped because it
// has no single committed file to point at -- verify, review and pr produce
// reports, decompose produces tracker items, and build and scaffold produce a
// diff over files nobody can name in advance. Demanding a file from those
// would be a false BLOCK on every run, which restores the silence this gate
// exists to remove and adds a way to stop correct work.
//
// Both spellings of the two aliased stages are accepted, for the same reason
// config.Toolkit.Stage accepts them: the caller passes supervisor's stage
// vocabulary and that vocabulary has two words for some stages.
func stageArtifact(cfg config.Config, stage, slug string) string {
	switch strings.ToLower(strings.TrimSpace(stage)) {
	case "intent":
		return filepath.Join(cfg.Paths.Intent, slug+".md")
	case "spec", "design":
		return filepath.Join(cfg.Paths.Specs, slug+".spec.md")
	case "plan":
		return filepath.Join(cfg.Paths.Plans, slug+".plan.md")
	}
	return ""
}

// checkStageArtifact reports whether a finished stage left the artifact it
// owes: present, non-empty, and tracked by git.
//
// EMPTY FAILS. A created-but-unwritten file is the exact shape of a command
// that half-ran, and it is worse than no file at all: the next stage finds
// the path it was told to read and designs from nothing.
//
// UNTRACKED FAILS, because the contract is the COMMITTED handoff. A file
// sitting in a worktree that git has never heard of does not survive the
// branch, the worktree, or the pull request that carries the work onward.
//
// A file deleted from the working tree needs no separate check: whether the
// deletion was staged or not, nothing is at the path and the absent case
// already reports it.
func checkStageArtifact(repoDir string, cfg config.Config, stage, slug string) error {
	rel := stageArtifact(cfg, stage, slug)
	if rel == "" {
		return nil
	}

	info, err := os.Stat(filepath.Join(repoDir, rel))
	switch {
	case err != nil:
		return artifactError(cfg, stage, rel,
			"nothing is at that path: the command wrote no file.")
	case info.IsDir():
		return artifactError(cfg, stage, rel,
			"that path is a directory, not the file the stage owes.")
	}

	body, err := os.ReadFile(filepath.Join(repoDir, rel))
	if err != nil {
		return artifactError(cfg, stage, rel,
			fmt.Sprintf("the file is there but cannot be read: %v.", err))
	}
	if strings.TrimSpace(string(body)) == "" {
		return artifactError(cfg, stage, rel,
			"the file is there and empty: the command created it and never wrote it.")
	}

	if out, err := exec.Command("git", "-C", repoDir,
		"ls-files", "--error-unmatch", "--", rel).CombinedOutput(); err != nil {
		return artifactError(cfg, stage, rel, fmt.Sprintf(
			"the file is there but git does not track it, so it was never committed (%s).",
			strings.TrimSpace(firstLine(string(out)))))
	}
	return nil
}

// artifactError words the failure so the orion.json line that caused it is
// findable from the message alone.
//
// All three of artifact, stage and command are named because the failure this
// catches is a wrong skill name in a config file: the path says what is
// missing, the stage says which block to look in, and the command is the line
// to correct. Any one of them missing turns the message into a search.
func artifactError(cfg config.Config, stage, rel, why string) error {
	return fmt.Errorf(
		"the %s stage exited 0 without leaving its artifact: %s\n"+
			"  Ran: %s\n"+
			"  %s\n"+
			"  That artifact is the handoff -- the next stage reads files, not "+
			"conversation -- so Orion stops here rather than several stages later.\n"+
			"  Check that the command names a skill that exists, and that it "+
			"writes and commits exactly that path.",
		stage, rel, stageCommand(cfg, stage), why)
}

// stageCommand names what the stage was configured to run, or says plainly
// that nothing was and the built-in prompt ran instead.
//
// A project with no toolkit block is the common case and its failure is a
// real one -- the agent was asked for the artifact and did not produce it --
// so it gets a sentence rather than an empty quoted string.
func stageCommand(cfg config.Config, stage string) string {
	if cmd := cfg.Toolkit.Stage(stage); cmd != "" {
		return fmt.Sprintf("%q, from toolkit.stages.%s in orion.json", cmd, canonicalStage(stage))
	}
	return fmt.Sprintf("Orion's built-in %s prompt (orion.json declares no toolkit.stages.%s)",
		canonicalStage(stage), canonicalStage(stage))
}

// canonicalStage is the spelling toolkit.stages holds a command under, so the
// message points at the key a reader will actually find in their file.
func canonicalStage(stage string) string {
	switch strings.ToLower(strings.TrimSpace(stage)) {
	case "design":
		return "spec"
	}
	return strings.ToLower(strings.TrimSpace(stage))
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
