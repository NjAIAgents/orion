package main

// `orion release` manages Jira versions, which Orion uses as release
// MILESTONES.
//
// THE NAMING HAZARD, and why the bare verb refuses to act. "Release" already
// means something else in this repository: cutting the binary, which pushes a
// tag, the Homebrew tap and the Scoop bucket. Two meanings now share one
// word, and the failure modes are NOT symmetrical -- creating a Jira version
// by mistake is untidy and reversible, publishing a release by mistake is
// public and is not.
//
// So `release` is a NOUN WITH SUBCOMMANDS and never acts on its own. A bare
// `orion release` prints what it could do and exits non-zero. That is the
// whole guard rail: the dangerous action can only ever be reached by naming
// it, so no half-typed command and no truncated script line arrives there by
// accident. Whatever later wraps scripts/release.sh gets an explicit verb --
// publish, cut, ship -- never the bare noun, and never `create`, which is now
// taken by the harmless one (OR-190).

import (
	"fmt"
	"os"
	"strings"

	"github.com/orion-sdlc/orion/internal/changelog"
	"github.com/orion-sdlc/orion/internal/registry"
	"github.com/orion-sdlc/orion/internal/tracker"
	"github.com/orion-sdlc/orion/internal/ui"
	"github.com/orion-sdlc/orion/internal/workspace"
)

const releaseUsage = `orion release <command>

  create <version> [--project KEY] [--description TEXT]
        Create an unreleased Jira version (a milestone). Safe to re-run:
        a version that already exists is reported, not an error.

  list [--project KEY]
        Every version on the project, and whether it is released.

  status <version> [--project KEY]
        What is in the milestone, what is unfinished, and whether the
        changelog fragments and the version agree in both directions.

This manages MILESTONES in Jira. It does not build, tag or publish anything.
`

// releaseAction names what a set of arguments asks for, without doing it.
//
// Split out from runRelease so the guard rail is a testable decision rather
// than an os.Exit buried in a switch. "" means the arguments do not name an
// action, which is the answer for both a bare `orion release` and an unknown
// subcommand, and the only correct response to it is to print usage and fail.
func releaseAction(args []string) string {
	if len(args) == 0 {
		return ""
	}
	switch args[0] {
	case "create":
		return "create"
	case "list", "ls":
		return "list"
	case "status":
		return "status"
	case "help", "--help", "-h":
		return "help"
	}
	return ""
}

func runRelease(args []string) {
	switch releaseAction(args) {
	case "create":
		runReleaseCreate(args[1:])
	case "list":
		runReleaseList(args[1:])
	case "status":
		runReleaseStatus(args[1:])
	case "help":
		fmt.Print(releaseUsage)
	default:
		// A bare `orion release` does NOT fall through to a default action,
		// because in this repository the obvious default would be cutting a
		// real release. It prints what it could do and fails.
		if len(args) > 0 {
			fmt.Fprintf(os.Stderr, "orion release: unknown command %q\n\n", args[0])
		}
		fmt.Fprint(os.Stderr, releaseUsage)
		os.Exit(64)
	}
}

// projectKeyFor resolves which Jira project to act on: the flag when given,
// otherwise the single registered project.
//
// Guessing when there are several would act on the wrong tracker, so it
// refuses and names the candidates instead.
func projectKeyFor(flag string) string {
	if flag != "" {
		return strings.ToUpper(flag)
	}
	reg, err := registry.Load(workspace.Home())
	exitOn(err)
	keys := reg.Keys()
	switch len(keys) {
	case 0:
		fmt.Fprintln(os.Stderr, "orion release: no project is registered; pass --project KEY")
		os.Exit(64)
	case 1:
		return keys[0]
	}
	fmt.Fprintf(os.Stderr, "orion release: %d projects are registered, so --project is required: %s\n",
		len(keys), strings.Join(keys, ", "))
	os.Exit(64)
	return ""
}

func runReleaseCreate(args []string) {
	var project, description string
	rest := []string{}
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--project":
			i++
			if i < len(args) {
				project = args[i]
			}
		case "--description":
			i++
			if i < len(args) {
				description = args[i]
			}
		default:
			rest = append(rest, args[i])
		}
	}
	if len(rest) == 0 {
		fmt.Fprintln(os.Stderr, "orion release create: which version? e.g. orion release create v0.9.0")
		os.Exit(64)
	}
	name := rest[0]
	key := projectKeyFor(project)

	j, err := tracker.NewJiraFromEnv()
	exitOn(err)

	v, created, err := j.CreateVersion(key, name, description)
	exitOn(err)

	if created {
		ui.Ok(os.Stdout, "created", "version %s on %s (id %s)", v.Name, key, v.ID)
		return
	}
	// Not an error. The caller asked for the version to exist and it does,
	// which is the property that makes this callable from a retry.
	ui.Ok(os.Stdout, "exists", "version %s already exists on %s (id %s)", v.Name, key, v.ID)
}

func runReleaseList(args []string) {
	var project string
	for i := 0; i < len(args); i++ {
		if args[i] == "--project" && i+1 < len(args) {
			i++
			project = args[i]
		}
	}
	key := projectKeyFor(project)

	j, err := tracker.NewJiraFromEnv()
	exitOn(err)

	vs, err := j.ListVersions(key)
	exitOn(err)

	if len(vs) == 0 {
		ui.Ok(os.Stdout, "none", "%s has no versions yet", key)
		return
	}
	for _, v := range vs {
		state := "open"
		switch {
		case v.Archived:
			state = "archived"
		case v.Released:
			state = "released"
		}
		fmt.Printf("  %-14s %-9s %s\n", v.Name, state, v.ID)
	}
}

// runReleaseStatus answers "what is in this release, and does the changelog
// agree" -- before the tag exists, rather than after (OR-187).
//
// It REPORTS and does not fix. A mismatch has two possible resolutions,
// inventing a release note or deleting one, and both are worse than telling
// a person what does not line up.
//
// Exit code is 0 for a clean milestone and 1 for one with mismatches, so this
// is usable as a gate. An INCOMPLETE milestone is not a failure: the correct
// behaviour is to ship what is done and roll the rest forward, and one stuck
// ticket must never hold a tag hostage.
func runReleaseStatus(args []string) {
	var project string
	rest := []string{}
	for i := 0; i < len(args); i++ {
		if args[i] == "--project" && i+1 < len(args) {
			i++
			project = args[i]
			continue
		}
		rest = append(rest, args[i])
	}
	if len(rest) == 0 {
		fmt.Fprintln(os.Stderr, "orion release status: which version?")
		os.Exit(64)
	}
	version := rest[0]
	key := projectKeyFor(project)

	j, err := tracker.NewJiraFromEnv()
	exitOn(err)

	issues, err := j.IssuesInVersion(key, version)
	exitOn(err)

	root, err := os.Getwd()
	exitOn(err)
	fragments, err := changelog.Load(root)
	exitOn(err)

	tickets := make([]changelog.Ticket, 0, len(issues))
	for _, is := range issues {
		tickets = append(tickets, changelog.Ticket{Key: is.Key, Done: is.Resolved()})
	}
	r := changelog.Reconcile(version, fragments, tickets)

	w := os.Stdout
	ui.Ok(w, version, "%d ticket(s): %d done, %d not done",
		len(tickets), len(r.Done), len(r.NotDone))

	for _, k := range r.NotDone {
		fmt.Fprintf(w, "          %s\n", ui.Dim(w, "not done   "+k))
	}
	for _, k := range r.TicketsWithoutFragment {
		ui.Warn(w, "%s is done but has no changelog fragment; it would ship unmentioned", k)
	}
	for _, k := range r.FragmentsWithoutTicket {
		ui.Warn(w, "fragment %s is not in %s; either it ships elsewhere or the ticket "+
			"is missing its fixVersion", k, version)
	}

	// Tickets that belong to no milestone at all. Not part of this version's
	// reconciliation, but the gap this whole convention exists to close, and
	// the moment before a release is when it is worth seeing.
	if orphans, err := j.IssuesWithoutVersion(key); err == nil && len(orphans) > 0 {
		ui.Warn(w, "%d open ticket(s) carry no fixVersion at all", len(orphans))
		for _, is := range orphans {
			fmt.Fprintf(w, "          %s\n", ui.Dim(w, is.Key+"  "+is.Summary))
		}
	}

	if !r.Clean() {
		os.Exit(1)
	}
	ui.Ok(w, "reconciled", "every done ticket has a fragment and every fragment has a ticket")
}
