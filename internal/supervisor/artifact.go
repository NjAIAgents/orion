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
// The result is a REPO-relative path in slash form on every platform: it is
// read by people, git and the tracker more than by the filesystem, and the
// one filesystem caller joins it under the repo dir with filepath.Join,
// which absorbs the slashes on Windows (OR-342).
func stageArtifact(cfg config.Config, stage, slug string) string {
	switch strings.ToLower(strings.TrimSpace(stage)) {
	case "intent":
		return filepath.ToSlash(filepath.Join(cfg.Paths.Intent, slug+".md"))
	case "spec", "design":
		return filepath.ToSlash(filepath.Join(cfg.Paths.Specs, slug+".spec.md"))
	case "plan":
		return filepath.ToSlash(filepath.Join(cfg.Paths.Plans, slug+".plan.md"))
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

	// The stage may have refused, and said so in the only place it can.
	//
	// Present, non-empty and tracked are all true of a document whose subject
	// is why it could not be written -- so all three pass and the run reports
	// success. FOUND ON A REAL PROJECT: the spec stage could not find the
	// intent it was to design from, correctly declined to invent one, and
	// committed 26KB beginning "Status: BLOCKED -- not an approved design."
	// Orion printed "exit 0 / reason completed", and the next stage would
	// have planned from a document that says it is not a design.
	//
	// An agent that refuses well is doing the right thing. Failing to HEAR it
	// is the defect.
	if why := declaredBlocked(string(body)); why != "" {
		return artifactError(cfg, stage, rel, why)
	}
	return nil
}

// blockedHeadLines is how far into a document a self-declared block is
// believed.
//
// A refusal is stated at the top -- in a status line, in front matter, or in
// a verdict section -- because it is the document's whole point. Deeper down,
// the same word is ordinary prose: a risks section naming what would block a
// later stage is a FINISHED spec, and failing it would punish thoroughness.
const blockedHeadLines = 40

// statusIsBlocked reports whether a "Status: ..." line says the status is
// blocked, rather than merely mentioning the word.
//
// Only the first word of the value, because that is the status: everything
// after it is elaboration. A spec that wrote "Status: DRAFT -- unapproved,
// and blocked" was failed by the earlier substring test, discarding a
// complete document that had self-reviewed and corrected three of its own
// errors -- because it described its approval state accurately.
func statusIsBlocked(bare string) bool {
	value := strings.TrimSpace(strings.TrimPrefix(bare, "status:"))
	fields := strings.FieldsFunc(value, func(r rune) bool {
		return r == ' ' || r == '-' || r == '\u2014' || r == ':' || r == ','
	})
	if len(fields) == 0 {
		return false
	}
	return fields[0] == "blocked"
}

// declaredBlocked reports why an artifact says it is not a real deliverable,
// or "" when it does not say so.
//
// Matched on a marker plus position rather than on the bare word, for the
// reason above. Deliberately narrow: a missed refusal costs one stage that
// should not have run, while a false positive fails work that is finished,
// and the second is the one that gets a check deleted.
func declaredBlocked(body string) string {
	lines := strings.Split(body, "\n")
	if len(lines) > blockedHeadLines {
		lines = lines[:blockedHeadLines]
	}
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		// Markdown emphasis and list markers surround the real text.
		bare := strings.ToLower(strings.Trim(trimmed, "*_#>-` \t"))
		switch {
		// The status IS blocked, not merely a status line that mentions
		// blocking. "Status: BLOCKED -- not an approved design" is a stage
		// refusing to work; "Status: DRAFT -- unapproved, and blocked" is a
		// finished document describing its own approval state, and failing
		// it discards a complete artifact. The difference is the first word
		// after the colon.
		case strings.HasPrefix(bare, "status:") && statusIsBlocked(bare),
			strings.HasPrefix(bare, "blocked:"),
			strings.HasPrefix(bare, "blocked ") && strings.Contains(bare, "--"):
			return "the stage declared itself BLOCKED in its own artifact: " +
				strings.TrimSpace(trimmed) + "\n" +
				"  It ran, and it refused -- which is the right answer to an input it\n" +
				"  could not work from. Read the artifact for what it needs, supply that,\n" +
				"  and run the stage again. Nothing after this stage should run yet."
		}
	}
	return ""
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
