package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// describeRunner runs pr-describe against a finished branch.
//
// Read-only, but not by the same means as adviseRunner. An advisor reads
// committed markdown, so Read, Glob and Grep are enough. pr-describe reads a
// git DELTA -- the branch against its base, plus the commit messages -- which
// needs git, and it is a skill, which needs the Skill tool.
//
// So the allowlist is wider, and every widening is named rather than waved
// through with Bash:
//
//	Skill                 to invoke pr-describe at all
//	Bash(git log:*)       the commit messages
//	Bash(git diff:*)      the delta
//	Bash(git show:*)      a single commit in full
//	Bash(git status:*)    which files moved
//	Read, Glob, Grep      the files the diff points at
//
// Bare `Bash` is deliberately absent. Allowing it would let a description
// step write to the branch it is describing -- and a commit made after the
// pull request was declared finished is one nobody planned to review.
func describeRunner(dir, model, effort, prompt string) (string, error) {
	bin, err := exec.LookPath("claude")
	if err != nil {
		return "", fmt.Errorf("claude CLI not found on PATH")
	}
	args := []string{
		"-p", prompt,
		"--output-format", "json",
		// Bounded tighter than an implementer. Describing a finished branch
		// is a reading task with a known end; a run that has taken thirty
		// turns is not being thorough, it is lost.
		"--max-turns", "20",
		"--allowedTools", strings.Join([]string{
			"Skill",
			"Read", "Glob", "Grep",
			"Bash(git log:*)", "Bash(git diff:*)",
			"Bash(git show:*)", "Bash(git status:*)",
		}, ","),
	}
	// Empty means "whatever the CLI is set to", so an unset field passes no
	// flag rather than an empty one -- same convention as the supervisor.
	if model != "" {
		args = append(args, "--model", model)
	}
	if effort != "" {
		args = append(args, "--effort", effort)
	}
	cmd := exec.Command(bin, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "ORION_ROLE=describer")

	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("the describe run failed: %w", err)
	}
	var res struct {
		Result  string `json:"result"`
		IsError bool   `json:"is_error"`
	}
	if json.Unmarshal(out, &res) != nil {
		return strings.TrimSpace(string(out)), nil
	}
	if res.IsError {
		return "", fmt.Errorf("the describer reported an error: %s", truncateStr(res.Result, 200))
	}
	return res.Result, nil
}
