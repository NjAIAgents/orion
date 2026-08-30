package main

// `orion release close` marks a Jira version released -- the last step of a
// release, and until now the only one that had to be done outside Orion
// (OR-209).
//
// WHY `close` AND NOT `publish`. `publish`, `cut` and `ship` are reserved for
// whatever eventually wraps scripts/release.sh, and release.go's guard rail
// asserts that none of them resolves to an action today. Spending one of them
// on the harmless Jira-side verb would put the reserved word one typo away
// from the irreversible meaning, which is the exact hazard that made `release`
// a noun with subcommands. `close` sits next to `create` and says what it does
// to a milestone.
//
// WHY IT IS NOT CALLED FROM scripts/release.sh. Wiring it there would close
// the tag, the artifacts and the milestone in one motion and the tracker could
// never drift -- but release.sh deliberately needs only an authenticated gh,
// and putting a Jira credential into it makes cutting a binary depend on the
// tracker being reachable. Orion owns the sequencing across steps
// (docs/decisions/0001), so the operator runs this as the next command; the
// date derivation below is what keeps that honest when they run it a day late.

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/orion-sdlc/orion/internal/tracker"
	"github.com/orion-sdlc/orion/internal/ui"
)

// closeVerdict is what closing a milestone should do, decided without doing it.
type closeVerdict struct {
	// Action is one of "close", "already" or "refuse".
	Action  string
	NotDone []string
}

// decideClose answers whether this version may be closed.
//
// An already-released version is reported, not an error, and is checked FIRST:
// a re-run must be a no-op the way `release create` is, and refusing a closed
// milestone for unfinished tickets would report a problem the command could
// not have caused and cannot fix.
//
// Unfinished tickets otherwise REFUSE. `release verify` treats them as a
// warning because shipping what is done and rolling the rest forward is
// normal -- but that roll-forward moves them to the next version, and closing
// a milestone that still holds them records work as shipped that was not.
// --force exists because the operator may know better; the default must not.
func decideClose(v tracker.Version, notDone []string, force bool) closeVerdict {
	switch {
	case v.Released:
		return closeVerdict{Action: "already"}
	case len(notDone) > 0 && !force:
		return closeVerdict{Action: "refuse", NotDone: notDone}
	}
	return closeVerdict{Action: "close", NotDone: notDone}
}

const dateLayout = "2006-01-02"

// releaseDate picks the day the milestone is dated: the flag when given,
// otherwise the tag's commit date, otherwise today (an empty string, which
// MarkReleased reads as now).
//
// The tag's date is the default because the common case is closing a
// milestone after the release: v0.8.0 shipped on the 29th and was closed on
// the 30th, and a milestone dated the 30th says a release happened on a day
// none did. tagDate is passed in rather than read here so the precedence is
// testable without a repository.
func releaseDate(flag, tagDate string) (string, error) {
	if flag != "" {
		if _, err := time.Parse(dateLayout, flag); err != nil {
			return "", fmt.Errorf("--date %q is not a YYYY-MM-DD date", flag)
		}
		return flag, nil
	}
	// A missing tag, or a git that answered with something else, falls through
	// to today rather than failing: the date is worth getting right, not worth
	// blocking the close over.
	if _, err := time.Parse(dateLayout, tagDate); err == nil {
		return tagDate, nil
	}
	return "", nil
}

func runReleaseClose(args []string) {
	var project, date string
	force := false
	rest := []string{}
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--project":
			i++
			if i < len(args) {
				project = args[i]
			}
		case "--date":
			i++
			if i < len(args) {
				date = args[i]
			}
		case "--force":
			force = true
		default:
			rest = append(rest, args[i])
		}
	}
	if len(rest) == 0 {
		fmt.Fprintln(os.Stderr, "orion release close: which version? e.g. orion release close v0.8.1")
		os.Exit(64)
	}
	name := rest[0]
	key := projectKeyFor(project)

	j, err := tracker.NewJiraFromEnv()
	exitOn(err)

	// Exact name, via the same lookup `release create` uses: v0.8 and v0.8.1
	// are different milestones and closing the wrong one is not undone by
	// re-running.
	v, found, err := j.FindVersion(key, name)
	exitOn(err)
	w := os.Stdout
	if !found {
		ui.Fail(w, "%s has no version named %s; `orion release list --project %s` shows the names",
			key, name, key)
		os.Exit(1)
	}

	issues, err := j.IssuesInVersion(key, name)
	exitOn(err)
	var notDone []string
	for _, is := range issues {
		if !is.Resolved() {
			notDone = append(notDone, is.Key)
		}
	}

	verdict := decideClose(v, notDone, force)
	switch verdict.Action {
	case "already":
		when := v.ReleaseDate
		if when == "" {
			when = "an unrecorded date"
		}
		ui.Ok(w, "released", "version %s on %s was already released (%s); nothing to do",
			v.Name, key, when)
		return
	case "refuse":
		ui.Fail(w, "%s has %d unfinished ticket(s), so closing it would record them as shipped: %s",
			v.Name, len(verdict.NotDone), strings.Join(verdict.NotDone, ", "))
		ui.Warn(w, "roll them onto the next milestone, or pass --force if they really did ship")
		os.Exit(1)
	}

	// The version name is the tag name in this repository (v0.8.1), so the
	// tag's own commit date is the day the release happened.
	root, err := os.Getwd()
	exitOn(err)
	when, err := releaseDate(date, gitOut(root, "log", "-1", "--format=%cs", name))
	exitOn(err)
	if when == "" {
		when = time.Now().Format(dateLayout)
	}

	exitOn(j.MarkReleased(v.ID, when))
	ui.Ok(w, "closed", "version %s on %s released %s (%d ticket(s), id %s)",
		v.Name, key, when, len(issues), v.ID)
	if len(verdict.NotDone) > 0 {
		ui.Warn(w, "forced past %d unfinished ticket(s): %s",
			len(verdict.NotDone), strings.Join(verdict.NotDone, ", "))
	}
}
