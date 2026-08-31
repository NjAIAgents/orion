package main

// `orion release verify` runs the five promotion checks (OR-188).
//
// This is the verification half of the ticket. It does NOT open the promotion
// pull request, ask in Slack, merge, tag or publish. Those are deliberately
// not wired yet: building the gate and then using it to cut the very next
// release would exercise its checks for the first time on the release they
// exist to protect, which is the one run where being wrong is expensive.

import (
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"

	"github.com/orion-sdlc/orion/internal/changelog"
	"github.com/orion-sdlc/orion/internal/promote"
	"github.com/orion-sdlc/orion/internal/tracker"
	"github.com/orion-sdlc/orion/internal/ui"
)

// exemptSubjects are commit subjects that legitimately carry no ticket.
// Kept narrow: changelog assembly and the release commit itself are produced
// by the release process, so requiring a ticket for them would report a
// finding on every release and teach the reader to skip check five.
var exemptSubjects = regexp.MustCompile(`(?i)^(docs: assemble|chore\(release\)|merge (pull request|branch|remote))`)

var ticketKey = regexp.MustCompile(`\b([A-Z][A-Z0-9]+-\d+)\b`)

// promotionTarget is the branch a promotion lands on, and so the far end of
// the range being promoted. It matches what the command prints ("promoting X
// onto main").
const promotionTarget = "main"

// unattributedCommits lists commits this promotion would put on `onto` that
// name no ticket at all.
//
// Both halves of that sentence were wrong before OR-238, and the two faults
// hid each other:
//
// The RANGE was <last tag>..develop, which walks commits already on main and
// therefore not part of this promotion at all. The tell was a count larger
// than the promotion itself -- 188 findings on a 30-commit range.
//
// The CRITERION was "carries a key belonging to THIS version", so a commit
// correctly keyed to OR-190 and shipped in an earlier release was reported as
// carrying no ticket key. A commit belonging to an earlier milestone is not a
// defect, and scoping the range alone would not have fixed that.
//
// This matters more than the noise it made: a gate that always fires a large
// warning teaches the operator to skim past the channel where a real finding
// would eventually appear.
func unattributedCommits(root, onto, base string) []string {
	var out []string
	rng := "origin/" + onto + "..origin/" + base
	for _, line := range strings.Split(gitOut(root, "log", "--format=%h %s", rng), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		subject := line
		if i := strings.Index(line, " "); i > 0 {
			subject = line[i+1:]
		}
		if exemptSubjects.MatchString(subject) || ticketKey.MatchString(subject) {
			continue
		}
		out = append(out, line)
	}
	return out
}

// gitOut lives in main.go and is reused rather than redeclared: two helpers
// that shell out to git with different error handling is how two callers get
// different answers to the same question.

func ghOut(dir string, args ...string) string {
	cmd := exec.Command("gh", args...)
	cmd.Dir = dir
	out, _ := cmd.CombinedOutput()
	return strings.TrimSpace(string(out))
}

func runReleaseVerify(args []string) {
	var project, base string
	rest := []string{}
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--project":
			i++
			if i < len(args) {
				project = args[i]
			}
		case "--base":
			i++
			if i < len(args) {
				base = args[i]
			}
		default:
			rest = append(rest, args[i])
		}
	}
	if len(rest) == 0 {
		fmt.Fprintln(os.Stderr, "orion release verify: which version?")
		os.Exit(64)
	}
	version := rest[0]
	key := projectKeyFor(project)
	if base == "" {
		base = "develop"
	}

	root, err := os.Getwd()
	exitOn(err)

	j, err := tracker.NewJiraFromEnv()
	exitOn(err)
	issues, err := j.IssuesInVersion(key, version)
	exitOn(err)

	fragments, err := changelog.Load(root)
	exitOn(err)
	collated, err := changelog.LoadCollated(root, version)
	exitOn(err)

	tickets := make([]changelog.Ticket, 0, len(issues))
	for _, is := range issues {
		tickets = append(tickets, changelog.Ticket{Key: is.Key, Done: is.Resolved()})
	}
	rec := changelog.Reconcile(version, fragments, tickets, collated)

	in := promote.Inputs{
		Version:                    version,
		NotDone:                    rec.NotDone,
		TicketsWithoutFragment:     rec.TicketsWithoutFragment,
		TicketsNotNamedInChangelog: rec.TicketsNotNamedInChangelog,
		FragmentsWithoutTicket:     rec.FragmentsWithoutTicket,
	}

	_ = gitOut(root, "fetch", "--quiet", "origin")
	in.HeadSHA = gitOut(root, "rev-parse", "origin/"+base)

	// The build for the branch's own PUSH, not for a pull request: the push
	// build is the one that tested the tree that actually resulted.
	if line := ghOut(root, "run", "list", "--branch", base, "--event", "push",
		"--limit", "1", "--json", "headSha,conclusion",
		"--jq", `.[0] | .headSha + " " + (.conclusion // "")`); line != "" {
		parts := strings.Fields(line)
		if len(parts) > 0 {
			in.BuildSHA = parts[0]
		}
		if len(parts) > 1 {
			in.BuildState = parts[1]
		}
	}

	if out := ghOut(root, "pr", "list", "--base", base, "--state", "open",
		"--json", "number", "--jq", `.[] | "#" + (.number|tostring)`); out != "" {
		in.OpenPullRequests = strings.Fields(out)
	}

	in.UnattributedCommits = unattributedCommits(root, promotionTarget, base)

	v := promote.Verify(in)
	w := os.Stdout

	ui.Ok(w, version, "promoting %s onto main", in.HeadSHA[:min(8, len(in.HeadSHA))])
	for _, c := range v.Warnings() {
		ui.Warn(w, "%s: %s", c.Name, c.Detail)
	}
	for _, c := range v.Blockers() {
		ui.Fail(w, "%s: %s", c.Name, c.Detail)
	}

	if v.Blocking() {
		ui.Fail(w, "%d blocking check(s); not safe to promote", len(v.Blockers()))
		os.Exit(1)
	}
	ui.Ok(w, "verified", "safe to promote (%d warning(s) to read first)", len(v.Warnings()))
}
