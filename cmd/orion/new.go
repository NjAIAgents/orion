package main

// `orion new "<idea>"` is the INTERACTIVE front half of the two sequential
// phases docs/decisions/0006 describes.
//
// Input is flat text from a human. Output is a named tracker project carrying
// a real description, which is the handoff artifact `orion plan` consumes --
// it already reads that description as the idea it designs from.
//
// IT PROVISIONS NO WORKSPACE. That is the question OR-148 left open and
// docs/decisions/0013 settles: `orion plan` provisions the workspace as its
// first action, keyed on the canonical slug, and a tracker project gets
// exactly one workspace (docs/decisions/0012). A `new` that also made one
// would leave two workspaces per project, which is the state 0012 exists to
// refuse. So `new` leaves nothing on disk and the elaborated idea lives in the
// project description until `plan` runs.
//
// THE INTERROGATION IS SYNCHRONOUS, and this is the one place in the system
// where that is right, because a human is present by definition.
// internal/discovery exists because the intent stage runs through `claude -p`
// and there was nobody to ask; that constraint does not reach here. So the
// questions are asked and answered now, in the terminal, rather than written
// into a file for a later stage to be blocked by.
//
// CREATION GOES THROUGH adopt.RemotePlan's describe-then-confirm gate, the
// same one `orion init` uses. A Jira project cannot be deleted without admin
// rights, and that gate is the only place that sentence is said before
// somebody agrees. A second confirmation pattern beside it would be one more
// prompt to train people to skim.

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/orion-sdlc/orion/internal/adopt"
	"github.com/orion-sdlc/orion/internal/tracker"
	"github.com/orion-sdlc/orion/internal/ui"
	"github.com/orion-sdlc/orion/internal/workspace"
)

// newQuestion is one thing a flat-text idea does not say.
//
// The set is taken from what internal/discovery already judges a thin idea to
// be missing -- "who it is for, what is out of scope and what success means
// are all unstated" -- so the conversation here answers the same questions the
// async gate would otherwise have to block on later.
type newQuestion struct {
	Heading string // how the answer is labelled in the description
	Ask     string // what the human is asked
}

var newQuestions = []newQuestion{
	{"For", "Who is this for? The people whose problem it is."},
	{"Problem", "What is wrong for them today?"},
	{"Success", "How will you know it worked?"},
	{"Out of scope", "What is explicitly NOT part of this?"},
	{"Constraints", "Anything it must or must not do -- systems, deadlines, technologies?"},
}

type newOptions struct {
	Idea string
	Site string // the tracker's base URL, for the plan and the browse link
	In   io.Reader
	Out  io.Writer
	// Confirm is the describe-then-confirm gate. Injected so a test can drive
	// both answers, and so the terminal check stays in the wiring rather than
	// in the logic.
	Confirm func(prompt string) bool
	// Ideas reads an idea already written down in the tracker, when Idea is a
	// key rather than prose (OR-349). Nil means the key path is unavailable
	// and the command interviews as it always has.
	Ideas ideaReader
}

func runNew(idea string, rest []string) {
	// These shaped a WORKSPACE, and this command no longer makes one. Ignoring
	// them silently would create a project and quietly drop the repo the user
	// asked to start from, which reads as success.
	for _, f := range []string{"--from", "--template", "--container", "--skip-discovery"} {
		if hasFlag(rest, f) || argFlag(rest, f, "") != "" {
			exitOn(fmt.Errorf("%s provisioned a workspace, and `orion new` no longer does (docs/decisions/0013).\n"+
				"  It creates the tracker project; `orion plan <KEY>` creates the workspace.", f))
		}
	}

	// The terminal is required for the INTERVIEW, not for the command. An
	// idea given by key is already written down, so there is nothing to ask
	// and nothing to type -- which is what makes this path usable from a
	// script (OR-349).
	if !isTerminal(os.Stdin) && !looksLikeIdeaKey(idea) {
		exitOn(fmt.Errorf("orion new needs a terminal: it interviews you about the idea.\n" +
			"  For an idea already written down in the tracker, pass its key:\n" +
			"    orion new PRIOR-3"))
	}

	j, err := tracker.NewJiraFromEnv()
	exitOn(err)

	exitOn(newRun(j, newOptions{
		Idea:    idea,
		Site:    j.BaseURL,
		In:      os.Stdin,
		Out:     os.Stdout,
		Confirm: confirm,
		Ideas:   j,
	}))
}

// newRun is the whole command with the tracker and the terminal injected.
func newRun(t tracker.Tracker, opts newOptions) error {
	out := opts.Out
	idea := strings.TrimSpace(opts.Idea)
	// An absent idea is NOT an error. This command interviews, so a missing
	// idea is simply its first question -- asked below, after the permission
	// probe, so that a run doomed by credentials fails before anything is
	// typed rather than after.

	// The permission is checked BEFORE the interview, not after it. Finding out
	// that the account cannot create a project is cheap; finding out after five
	// questions have been answered wastes the one resource this command spends,
	// which is the human's attention.
	cap, err := t.Probe()
	if err != nil {
		return err
	}
	if !cap.Authenticated {
		return fmt.Errorf("not authenticated to the tracker: %s\n  Fix it with: orion config", cap.Detail)
	}
	if cap.AccountID == "" {
		return fmt.Errorf("could not resolve the authenticated account, and a project cannot be created without a lead.\n" +
			"  Check the credentials with: orion doctor")
	}
	if !cap.CanCreateProject && !cap.Undetermined {
		return fmt.Errorf("this account cannot create a tracker project: %s\n"+
			"  Create one by hand and design from it instead: orion plan <KEY>", cap.Detail)
	}

	r := bufio.NewReader(opts.In)

	// An idea already written down answers the interview's questions before
	// it is asked (OR-349). Only when it really is written down: every idea
	// in a fresh discovery project carries the unfilled template, and
	// planning from boilerplate is the failure the interview prevents.
	var (
		description string
		name        string
		source      tracker.Issue
	)
	if looksLikeIdeaKey(idea) && opts.Ideas != nil {
		found, err := fetchIdea(opts.Ideas, idea)
		if err != nil {
			return err
		}
		ok, why := writtenDown(found)
		if !ok {
			// Not an error. The key is valid and the idea exists; it just has
			// nothing in it yet, and the interview is the right answer.
			ui.Warn(out, "%s is not written up yet: %s.", found.Key, why)
			fmt.Fprintf(out, "  Interviewing instead. Fill the idea in and re-run to skip this.\n\n")
			if !isTerminal(os.Stdin) {
				return fmt.Errorf("%s has nothing to design from, and there is no terminal to interview in.\n"+
					"  Write the idea up in the tracker, then re-run: orion new %s", found.Key, found.Key)
			}
		} else {
			source = found
			description = ideaDescription(found, opts.Site)
			name = strings.TrimSpace(found.Summary)
			fmt.Fprintln(out, ui.Heading(out, "The idea"))
			fmt.Fprintf(out, "  %s  %s\n", found.Key, name)
			fmt.Fprintf(out, "  %s\n\n", ui.Dim(out, "read from the tracker; no interview needed"))
		}
	}

	if description == "" {
		fmt.Fprintln(out, ui.Heading(out, "The idea"))
		// Given on the command line it is echoed; absent, it becomes the
		// interview's first question rather than a usage error, since asking
		// is what this command does.
		if idea == "" {
			fmt.Fprintln(out, "One or two sentences is plenty -- the questions after")
			fmt.Fprintln(out, "this one go into the detail.")
			fmt.Fprintln(out)
			answer, ok := ask(r, out, "What do you want built?")
			if !ok || strings.TrimSpace(answer) == "" {
				return fmt.Errorf("no idea given, so there is nothing to plan.\n" +
					"  Run it again with one: orion new \"what you want built\"")
			}
			idea = strings.TrimSpace(answer)
		} else {
			fmt.Fprintf(out, "  %s\n\n", idea)
		}
		fmt.Fprintln(out, "Five questions. Every later stage runs non-interactively, so this is the")
		fmt.Fprintln(out, "last point at which anything can be asked. Enter leaves one unanswered.")
		fmt.Fprintln(out)

		description = elaborate(r, out, idea)
	}

	if name == "" {
		var err error
		name, err = askName(r, out)
		if err != nil {
			return err
		}
	}
	slug := workspace.Slugify(name)
	key, err := tracker.ResolveKey(t, tracker.DeriveKey(slug))
	if err != nil {
		return err
	}

	fmt.Fprintln(out, ui.Heading(out, "Project description"))
	for _, line := range strings.Split(description, "\n") {
		fmt.Fprintf(out, "  %s\n", line)
	}
	fmt.Fprintln(out)
	fmt.Fprintf(out, "  slug %s  %s\n", slug,
		ui.Dim(out, "(will name the workspace and the git repo)"))
	fmt.Fprintln(out)

	// The gate. One prompt, and it is adopt's, because that is where the
	// sentence about deletion lives.
	plan := adopt.RemotePlan{ProjectName: name, JiraKey: key, JiraSite: opts.Site}
	fmt.Fprint(out, plan.Describe())
	if opts.Confirm == nil || !opts.Confirm("Proceed?") {
		fmt.Fprintln(out, "cancelled: nothing was created")
		return nil
	}

	b, err := t.CreateProject(key, name, description, cap.AccountID)
	if err != nil {
		return err
	}

	fmt.Fprintln(out)
	ui.Ok(out, "created", "%s project %s  %s/browse/%s", t.Name(), b.Key, opts.Site, b.Key)

	// The back-link, so a reader of the idea can find the work (OR-349).
	// BEST EFFORT: the project exists by now, and losing the link is untidy
	// where losing the run would not be. The idea's own status is left alone
	// deliberately -- that board's workflow belongs to whoever runs it.
	if source.Key != "" && opts.Ideas != nil {
		if err := opts.Ideas.Comment(source.Key, ideaBackLink(b.Key, opts.Site)); err != nil {
			ui.Warn(out, "could not comment the new key onto %s: %v", source.Key, err)
			fmt.Fprintf(out, "  The project was created; only the link back is missing.\n")
		} else {
			ui.Ok(out, "linked", "%s now names %s", source.Key, b.Key)
		}
	}

	fmt.Fprintf(out, "\nnext: orion plan %s\n", b.Key)
	return nil
}

// elaborate turns the flat text plus the answers into the description the
// project will carry.
//
// An unanswered question is NAMED rather than dropped. The description is the
// only artifact this command produces, so a later stage designing from it
// should be able to tell "nobody decided this" from "this does not apply" --
// silence collapses the two, and the stage invents the answer.
func elaborate(r *bufio.Reader, out io.Writer, idea string) string {
	var b strings.Builder
	b.WriteString(idea)

	// Input ending part-way through is not special-cased: ask answers blank
	// from then on, so the rest of the questions land in `unstated` and the
	// description still says which ones nobody answered.
	var unstated []string
	for _, q := range newQuestions {
		a, _ := ask(r, out, q.Ask)
		if a == "" {
			unstated = append(unstated, strings.ToLower(q.Heading))
			continue
		}
		fmt.Fprintf(&b, "\n\n%s: %s", q.Heading, a)
	}
	if len(unstated) > 0 {
		fmt.Fprintf(&b, "\n\nNot stated when this project was created: %s.", strings.Join(unstated, ", "))
	}
	return b.String()
}

// askName loops until there is a name, because there is nothing sensible to
// default to.
//
// Deliberately NOT derived from the idea. A slug of "customers should see
// claim status in the portal" makes a project called "Customers Should See
// Claim", which somebody accepts by pressing Enter and then lives with in
// three places (docs/decisions/0009). Asking costs one line of typing.
func askName(r *bufio.Reader, out io.Writer) (string, error) {
	fmt.Fprintln(out, ui.Heading(out, "The name"))
	fmt.Fprintln(out, "  One name will label the tracker project, the workspace and the git repo.")
	for {
		n, ok := ask(r, out, "Project name?")
		if n != "" {
			return n, nil
		}
		// A required prompt has to give up when there is nothing left to read,
		// or a closed stdin spins here forever printing the same line.
		if !ok {
			return "", fmt.Errorf("no project name given, and there is no input left to ask for one")
		}
		fmt.Fprintln(out, "  A name is required.")
	}
}

// ask puts one question and reads one line. The second return is false once
// the input is exhausted, which is what stops a required prompt looping.
func ask(r *bufio.Reader, out io.Writer, question string) (string, bool) {
	fmt.Fprintf(out, "%s\n  > ", question)
	line, err := r.ReadString('\n')
	fmt.Fprintln(out)
	return strings.TrimSpace(line), err == nil
}

// isTerminal reports whether f is a real terminal, so the refusal happens
// before the interview rather than at the first prompt nobody can answer.
//
// Deliberately NOT a char-device check. os.ModeCharDevice is set for
// /dev/null as well as for a tty, and /dev/null is precisely what a command
// with no stdin gets: cron redirects it there, CI does, and Go connects a
// child's nil Stdin to it. Every one of those would have been classified
// interactive and fallen through to five questions nobody is present to
// answer, which is the opposite of what the check is for.
//
// stty rather than golang.org/x/term: go.mod has no dependencies and
// internal/creds already makes this exact trade for echo suppression, calling
// a module "a poor trade for one syscall". One exec, on one interactive
// command, is cheaper than the first entry in go.sum.
//
// stty's own output goes nowhere -- an exec.Cmd's unset Stdout and Stderr are
// already /dev/null -- so the probe stays silent either way. A machine with no
// stty answers false and gets the refusal, which is the safe direction: the
// message names `orion plan`, while a wrong "yes" blocks forever on a read.
func isTerminal(f *os.File) bool {
	cmd := exec.Command("stty")
	cmd.Stdin = f
	return cmd.Run() == nil
}
