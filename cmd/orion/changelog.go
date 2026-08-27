package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/orion-sdlc/orion/internal/changelog"
	"github.com/orion-sdlc/orion/internal/config"
	"github.com/orion-sdlc/orion/internal/ui"
)

// orion changelog -- generate CHANGELOG.md with nj-agents' changelog skill.
//
// Orion orchestrates; nj-agents provides the capability. A changelog is a
// reusable SDLC activity -- useful in a repository that never adopted Orion --
// so it belongs there and is called from here, exactly as pr-describe is.
//
// This exists because I wrote a CHANGELOG by hand while that skill already
// existed, one week after wiring pr-describe for precisely the same reason.
// The rule that would have caught it: if a capability would be useful in a
// repository that never adopted Orion, it is not Orion's to implement.
//
// The skill is AUTHORING-class: it writes the file and proposes the commit,
// never committing, pushing or tagging. Orion keeps that contract -- it runs
// the skill and stops. Committing the result is a person's decision, and a
// changelog is the one artifact whose whole purpose is to be read before it
// is believed.
func runChangelog(args []string) {
	w := os.Stdout

	dir := argFlag(args, "--path", ".")
	root, err := config.FindRoot(dir)
	if err != nil {
		// Not fatal: the skill works in any git repository, and Orion's own
		// config is irrelevant to it. Fall back to where we were asked to run.
		root = dir
	}
	version := argFlag(args, "--version", "")

	// Fragments first. When tickets wrote `.changelog.d/<KEY>.md`, collation is
	// a deterministic merge -- group by section, emit in keepachangelog order --
	// and a deterministic merge does not need an agent to perform it, or to be
	// trusted afterwards. The skill path below is for a repository that has no
	// fragments, where the commits are the only signal there is.
	frags, err := changelog.Load(root)
	if err != nil {
		ui.Fail(w, "%v", err)
		os.Exit(1)
	}
	if len(frags) > 0 {
		collateFragments(w, root, version)
		return
	}

	if _, err := exec.LookPath("claude"); err != nil {
		ui.Fail(w, "claude CLI not found on PATH; the changelog skill runs inside it")
		os.Exit(1)
	}

	ui.Ok(w, "working", "generating the changelog with nj-agents changelog")
	out, err := changelogRunner(root, version)
	if err != nil {
		ui.Fail(w, "%v", err)
		os.Exit(1)
	}

	// The skill reports what it did. Print its closing message rather than
	// inventing a summary, because it is the only thing that knows whether
	// it wrote a new section, updated one, or found nothing to record.
	if s := strings.TrimSpace(out); s != "" {
		fmt.Fprintf(w, "\n%s\n", s)
	}
	fmt.Fprintf(w, "\n%s\n", ui.Dim(w,
		"Nothing was committed. Read it, then commit it yourself -- a changelog\n"+
			"  is the one artifact whose purpose is to be read before it is believed."))
}

// collateFragments merges `.changelog.d/*.md` into CHANGELOG.md and deletes
// them.
//
// The tickets that shipped without a fragment are reported afterwards, because
// the failure this whole mechanism can still have is quiet: a ticket writes no
// fragment, the release simply does not mention its change, and nothing says
// an entry was expected. A key listed here is a prompt to look, not an
// accusation -- a commit that merely mentions another ticket reads the same as
// one that implements it.
func collateFragments(w *os.File, root, version string) {
	seen := changelog.TicketKeys(root)

	res, err := changelog.Collate(root, version)
	if err != nil {
		ui.Fail(w, "%v", err)
		os.Exit(1)
	}

	ui.Ok(w, "collated", "%d fragment(s) into CHANGELOG.md under %s: %s",
		len(res.Keys), res.Version, strings.Join(res.Keys, ", "))
	ui.Ok(w, "removed", "%s/ is empty again", changelog.Dir)

	if missing := changelog.Unrecorded(seen, res.Keys); len(missing) > 0 {
		ui.Warn(w, "no changelog fragment for %s.\n"+
			"         Either the change is invisible to a reader of the changelog, or\n"+
			"         its entry is missing from this release. Check before you tag.",
			strings.Join(missing, ", "))
	}

	fmt.Fprintf(w, "\n%s\n", ui.Dim(w,
		"Nothing was committed. Commit the CHANGELOG.md edit and the fragment\n"+
			"  deletions together -- a fragment that survives collation ships twice."))
}

// changelogRunner runs the skill with write access to ONE file.
//
// `Write(CHANGELOG.md)` and `Edit(CHANGELOG.md)` rather than bare Write.
// The skill's own contract says it touches only the changelog, and a
// contract is a promise rather than a constraint -- this makes it the
// second. A generator with unrestricted write access to a repository is a
// generator that can change the code it is describing.
//
// git is read-only: log, tag, diff, show, status. The skill reads
// Conventional Commits as its input signal, and nothing more than reading
// them is required to write a changelog.
func changelogRunner(dir, version string) (string, error) {
	bin, err := exec.LookPath("claude")
	if err != nil {
		return "", fmt.Errorf("claude CLI not found on PATH")
	}
	args := []string{
		"-p", changelogPrompt(version),
		"--output-format", "json",
		"--model", "sonnet",
		"--max-turns", "30",
		"--allowedTools", strings.Join([]string{
			"Skill",
			"Read", "Glob", "Grep",
			"Write(CHANGELOG.md)", "Edit(CHANGELOG.md)",
			"Bash(git log:*)", "Bash(git tag:*)", "Bash(git diff:*)",
			"Bash(git show:*)", "Bash(git status:*)",
		}, ","),
	}
	cmd := exec.Command(bin, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "ORION_ROLE=changelog")

	raw, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("the changelog run failed: %w", err)
	}
	var res struct {
		Result  string `json:"result"`
		IsError bool   `json:"is_error"`
	}
	if json.Unmarshal(raw, &res) != nil {
		return strings.TrimSpace(string(raw)), nil
	}
	if res.IsError {
		return "", fmt.Errorf("the changelog skill reported an error: %s",
			truncateStr(res.Result, 300))
	}
	return res.Result, nil
}

func changelogPrompt(version string) string {
	lines := []string{
		"Use the changelog skill to generate or update CHANGELOG.md for this",
		"repository.",
		"",
		"Read the commits since the last tag as the input signal.",
	}
	if version != "" {
		lines = append(lines, "",
			"Write the section for version "+version+".")
	} else {
		lines = append(lines, "",
			"Choose the version yourself from the change set, following semver,",
			"and say which you chose and why.")
	}
	lines = append(lines,
		"",
		"Write ONLY CHANGELOG.md. Do not touch any other file, do not commit,",
		"do not push, and do not tag -- Orion and the person running it handle",
		"those.",
		"",
		"Write for someone deciding whether to upgrade: what changed, and what",
		"it now refuses to do. A list of commit subjects is not a changelog;",
		"the reader has git for that.")
	return strings.Join(lines, "\n")
}
