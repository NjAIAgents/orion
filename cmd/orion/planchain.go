package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/orion-sdlc/orion/internal/registry"
	"github.com/orion-sdlc/orion/internal/supervisor"
	"github.com/orion-sdlc/orion/internal/ui"
	"github.com/orion-sdlc/orion/internal/workspace"
)

// Running the planning chain, rather than printing the command for each stage
// in turn.
//
// `orion plan` already holds the whole chain: it declares planStages, prints
// the roster in order, and estimates the cost of all four together. Ending
// with "next: orion run <id> --stage spec" and stopping asked the operator to
// hand-execute a loop Orion was already holding -- four commands typed in the
// right order, from a roster shown once and then scrolled away.
//
// IT PAUSES BETWEEN STAGES. Each stage is a supervised model run costing real
// money and producing an artifact the next stage designs from, so the operator
// gets to read what one stage wrote before paying for the one that consumes
// it. A chain that ran four stages unattended would turn one wrong spec into
// three more stages built on it -- which is exactly what happened on CloudLens
// before the artifact check learned to hear a stage refuse.

// stageRunner runs one stage. Injected so a test drives the chain without
// spawning a model.
type stageRunner func(ws *workspace.Workspace, stage string) (*supervisor.Result, error)

// confirmer asks a yes/no question. Injected for the same reason.
type confirmer func(prompt string) bool

// asker reads one free-text answer, or "" when nobody is there. A frame
// step that can recover from a bad input -- a path already taken -- needs
// to ask for a value, not a yes or no.
type asker func(prompt string) string

// stepIO is what a frame step is given: where to write, and the two ways to
// ask. Both may be nil off a terminal, and a step must behave sensibly then.
type stepIO struct {
	Out     io.Writer
	Confirm confirmer
	Ask     asker
}

// Degraded is a frame step reporting that it did NOT do what it exists to
// do, without claiming the chain must stop.
//
// A step had two outcomes -- nil, meaning done, or an error, meaning stop --
// and a step that half-worked had to pick one. Both were wrong: the remote
// step set no default branch and protected neither branch and printed
// "done"; the clone step created no copy at all and printed "done" (OR-408).
// A reader who is told a step is done and finds it is not stops reading the
// other lines too.
type Degraded struct {
	// What did not happen, in the step's own words.
	Reason string
	// Fix is what the operator can do about it, when there is something.
	Fix string
}

func (d *Degraded) Error() string { return d.Reason }

// degraded is a helper for the common case.
func degraded(reason, fix string) error { return &Degraded{Reason: reason, Fix: fix} }

// askCheckoutPath asks where the operator wants their own clone.
//
// Asked BEFORE the chain rather than after it, while they are already
// answering questions: a prompt at the end arrives when the interesting part
// is over and the terminal has scrolled, and the answer is only recorded here
// anyway -- the clone itself happens once there is something to clone.
//
// Blank is a complete answer. The sandbox is a legitimate place to leave a
// project, and `orion clone` exists for anyone who changes their mind.
func askCheckoutPath(out io.Writer, ask func(string) string) string {
	fmt.Fprintln(out)
	fmt.Fprintln(out, ui.Heading(out, "Your copy"))
	fmt.Fprintln(out, "Orion works in its own sandbox, which keeps a bad run out of your files.")
	fmt.Fprintln(out, "It can also put an ordinary clone wherever you keep your code.")
	fmt.Fprintln(out)
	fmt.Fprintln(out, ui.Dim(out,
		"A folder you already keep code in works: the copy goes inside it, named for the project."))
	fmt.Fprintln(out)
	answer := strings.TrimSpace(ask("Where? (e.g. ~/code -- blank to stay in the sandbox)"))
	if answer == "" {
		return ""
	}
	p, err := expandPath(answer)
	if err != nil {
		ui.Warn(out, "%v -- staying in the sandbox", err)
		return ""
	}
	return p
}

// cloneStep makes the copy the operator asked for, as the last step of the
// chain, once the stages have committed something worth copying.
//
// Best effort: the planning work is done and committed by now, so a failed
// clone costs a convenience rather than the run. It says what went wrong and
// names the command to retry with, and returns nil so the chain still ends.
// A step of the chain rather than a call after it, so that a resume can see
// it: a re-run after a failed clone retries the clone.
func cloneStep(sio *stepIO, ws *workspace.Workspace) error {
	dest := strings.TrimSpace(ws.Task.CheckoutPath)
	if dest == "" {
		return nil
	}
	// PUSH BEFORE CLONING. The remote step pushes main and develop and then
	// returns; every stage after it -- spec, plan, tasks, scaffold --
	// commits to the sandbox and nothing sends those anywhere. Cloning
	// from the remote at that point hands over a repository with none of
	// the work in it, so the branch the chain has been committing to goes
	// up first (OR-418).
	pushPlanBranch(sio.Out, ws)

	// A path already taken is the ordinary failure here, and the operator
	// is standing right there: ask for another rather than warning about
	// it and calling the step done (OR-408). Up to three tries, because a
	// prompt that will not take no for an answer is its own problem.
	for try := 0; try < 3; try++ {
		err := cloneWorkspace(sio.Out, ws, dest)
		if err == nil {
			ws.Task.CheckoutPath = dest
			_ = ws.SaveTask()
			return nil
		}
		ui.Warn(sio.Out, "%v", err)
		if sio.Ask == nil {
			break
		}
		next := strings.TrimSpace(sio.Ask("  Another path for your copy, or press enter to skip:"))
		if next == "" {
			break
		}
		dest = next
	}
	return degraded(
		fmt.Sprintf("no copy was made at %s", dest),
		fmt.Sprintf("orion clone %s <path>", ws.ID))
}

// cloneDone: nothing to do when no copy was asked for, and done when the
// copy is already a repository -- cloneWorkspace refuses an existing
// directory, so a resume that ran it again would only report that.
func cloneDone(ws *workspace.Workspace) bool {
	dest := strings.TrimSpace(ws.Task.CheckoutPath)
	if dest == "" {
		return true
	}
	if p, err := expandPath(dest); err == nil {
		dest = p
	}
	// EITHER SHAPE, because the answer to "where do you want your copy" is
	// usually the folder someone keeps code in, and the copy then lands at
	// <folder>/<id> rather than at the path itself (OR-418). Checking only
	// the recorded path would report the step unfinished forever and clone
	// again on every resume.
	for _, p := range []string{dest, filepath.Join(dest, ws.ID)} {
		if _, err := os.Stat(filepath.Join(p, ".git")); err == nil {
			return true
		}
	}
	return false
}

// runPlanChain runs the planning stages in order, pausing after each.
//
// Returns the number of stages that completed, so the caller can report where
// a stopped chain got to. A stage that fails, blocks, or is declined ends the
// chain: everything after it reads what it wrote.
func runPlanChain(out io.Writer, ws *workspace.Workspace, run stageRunner, ask confirmer) int {
	return runPlanChainFrom(out, ws, run, ask, "")
}

// runPlanChainFrom is runPlanChain with a step to re-run from: that step and
// every one after it run whether or not they are done. Steps before it keep
// the normal rule. from is a step name already validated by planFromIndex;
// an unknown one here is treated as no --from, never as "from the start".
func runPlanChainFrom(out io.Writer, ws *workspace.Workspace, run stageRunner, ask confirmer, from string) int {
	return runPlanChainWith(out, ws, run, ask, nil, from)
}

// runPlanChainWith is runPlanChainFrom with the free-text asker a frame step
// may need to recover from a bad input -- a clone path already taken.
func runPlanChainWith(out io.Writer, ws *workspace.Workspace, run stageRunner, ask confirmer, askText asker, from string) int {
	fromIdx, err := planFromIndex(from)
	if err != nil {
		fromIdx = -1
	}
	done := 0
	// Whether anything has run yet. The first step that actually runs is
	// not asked about -- the operator confirmed the chain immediately
	// before -- and that is decided by what ran, not by position, because
	// a resumed chain skips any number of done steps first.
	started := false
	for i, s := range planStages {
		forced := fromIdx >= 0 && i >= fromIdx
		// A step whose work is already there is reported and skipped, not
		// asked about: the question "continue to spec?" has no answer when
		// the spec is committed and passes its gate. This is what makes a
		// re-run of `orion plan` a resume rather than a repeat -- and it
		// counts as done, so a chain that skips everything still ends.
		// Unless --from reaches it: then the operator has said to redo it.
		if !forced && s.Done != nil && s.Done(ws) {
			fmt.Fprintf(out, "\n  %s%s\n", ui.Icon(out, ui.VerbOK), ui.Dim(out, fmt.Sprintf("= done  %d/%d  %s", i+1, len(planStages), s.Stage)))
			done++
			continue
		}

		// The first stage is not asked about. The operator just confirmed the
		// whole chain and its cost to get here; asking again before anything
		// has happened is a prompt with no new information in it.
		if started {
			fmt.Fprintln(out)
			if !ask(fmt.Sprintf("Continue to %d/%d %s -- %s?",
				i+1, len(planStages), s.Stage, s.What)) {
				fmt.Fprintf(out, "\n  %s%s\n", ui.Icon(out, "pending"), ui.Dim(out, fmt.Sprintf(
					"stopped after %d of %d stages, at your request", done, len(planStages))))
				// A frame step cannot be run by `orion run`; the chain runs
				// it, so the resume is `orion plan`, which skips what is
				// done and picks up here.
				if s.Frame != nil {
					fmt.Fprintf(out, "  resume: orion plan %s\n", planKeyOf(ws))
				} else {
					fmt.Fprintf(out, "  resume: orion run %s --stage %s\n", ws.ID, s.Stage)
				}
				return done
			}
		}

		fmt.Fprintf(out, "\n%s\n", ui.Heading(out, fmt.Sprintf(
			"%d/%d  %s", i+1, len(planStages), s.Stage)))

		// A frame step runs here, in this process: no model, no log path,
		// no budget checkpoint. It stops the chain the way a failed stage
		// does, naming itself, because everything after it needs what it
		// makes -- a tracker tree needs a remote to point at.
		started = true
		if s.Frame != nil {
			err := s.Frame(&stepIO{Out: out, Confirm: ask, Ask: askText}, ws)
			// A fallback step that found nothing to work from hands the
			// stage to the supervised runner below, which is what it would
			// have been before the native route existed.
			if errors.Is(err, errNotApplicable) && s.Fallback {
				err = nil
				s.Frame = nil
			}
			var deg *Degraded
			if s.Frame == nil {
				// fall through to the stage runner
			} else if errors.As(err, &deg) {
				// DID NOT DO WHAT IT EXISTS TO DO, and the chain can still
				// go on. Said as a warning rather than a tick, and the
				// operator decides whether to continue -- a step reported
				// done that was not is a line nobody trusts afterwards.
				done++
				fmt.Fprintln(out, ui.Icon(out, ui.VerbWarn)+ui.Label(out, "warning", deg.Reason))
				if deg.Fix != "" {
					fmt.Fprintf(out, "  %s\n", ui.Dim(out, "when you want it: "+deg.Fix))
				}
				if ask != nil && !ask(fmt.Sprintf("%s did not finish. Continue anyway?", s.Stage)) {
					fmt.Fprintf(out, "\n  %s\n", ui.Dim(out, fmt.Sprintf(
						"stopped after %d of %d steps, at your request", done, len(planStages))))
					fmt.Fprintf(out, "  resume: orion plan %s\n", planKeyOf(ws))
					return done
				}
				continue
			} else if err != nil {
				fmt.Fprintln(out)
				fmt.Fprintln(out, ui.Icon(out, ui.VerbFail)+ui.Label(out, "failed", err.Error()))
				fmt.Fprintf(out, "\n  %s\n", ui.Dim(out, fmt.Sprintf(
					"stopped at %d of %d steps, at %s", i+1, len(planStages), s.Stage)))
				fmt.Fprintf(out, "  resume: orion plan %s\n", planKeyOf(ws))
				return done
			} else {
				done++
				fmt.Fprintln(out, ui.Icon(out, ui.VerbOK)+ui.Label(out, "done", s.Stage))
				continue
			}
		}

		before := headBranch(ws.RepoDir())
		res, err := run(ws, s.Stage)
		noteBranchChange(out, ws, s.Stage, before)
		if res != nil {
			fmt.Fprintf(out, "  exit %d  %s  %s\n",
				res.ExitCode, res.Duration.Round(time.Second), res.LogPath)
		}
		if err != nil {
			// The error already says what went wrong -- a failed run, or an
			// artifact the stage owed and did not leave, including one that
			// declares itself BLOCKED. Repeating it as "stage failed" would
			// bury the part that names the fix.
			fmt.Fprintln(out)
			fmt.Fprintln(out, ui.Icon(out, ui.VerbFail)+ui.Label(out, "failed", err.Error()))
			fmt.Fprintf(out, "\n  %s\n", ui.Dim(out, fmt.Sprintf(
				"stopped at %d of %d stages", i+1, len(planStages))))
			fmt.Fprintf(out, "  re-run this stage: orion run %s --stage %s\n", ws.ID, s.Stage)
			return done
		}
		done++
		fmt.Fprintln(out, ui.Icon(out, ui.VerbOK)+ui.Label(out, "done", s.Stage))
	}
	return done
}

// planKeyOf is the tracker key the workspace is bound to, for a resume line
// that names `orion plan KEY` -- which resumes -- rather than `orion run`,
// which cannot run a frame step. Falls back to the workspace id when the
// binding is absent, so the line is never blank.
func planKeyOf(ws *workspace.Workspace) string {
	if k := ws.Task.TrackerKey(); k != "" {
		return k
	}
	return ws.ID
}

// askYesNo reads a y/N answer.
//
// Defaults to NO, and treats a closed stdin as no: a chain that carried on
// because nobody was there to say otherwise is the failure this pause exists
// to prevent.
func askYesNo(r *bufio.Reader, out io.Writer, prompt string) bool {
	fmt.Fprintf(out, "%s [y/N] ", prompt)
	line, err := r.ReadString('\n')
	if err != nil && strings.TrimSpace(line) == "" {
		fmt.Fprintln(out)
		return false
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true
	}
	return false
}

// headBranch is the branch the sandbox is standing on, or "" when the
// question has no answer -- no repository yet, or a detached HEAD.
func headBranch(dir string) string {
	out, err := exec.Command("git", "-C", dir, "rev-parse", "--abbrev-ref", "HEAD").Output()
	if err != nil {
		return ""
	}
	if b := strings.TrimSpace(string(out)); b != "HEAD" {
		return b
	}
	return ""
}

// noteBranchChange says when a stage moved the sandbox onto another branch,
// and records where it left it.
//
// Nothing stops a stage's model from checking out a branch of its own, and
// until this existed nothing noticed either: every stage after it committed
// somewhere the chain never chose, and the first sign was a pull request
// from a branch nobody recognised (OR-405).
func noteBranchChange(out io.Writer, ws *workspace.Workspace, stage, before string) {
	after := headBranch(ws.RepoDir())
	if after == "" {
		return
	}
	if before != "" && before != after {
		ui.Warn(out, "%s left the sandbox on %s, not %s", stage, after, before)
		fmt.Fprintf(out, "  %s\n", ui.Dim(out, fmt.Sprintf(
			"every stage after this one commits there: git -C %s checkout %s to put it back",
			ws.RepoDir(), before)))
	}
	if ws.Task.PlanBranch != after {
		ws.Task.PlanBranch = after
		_ = ws.SaveTask()
	}
}

// pushPlanBranch sends the branch the chain committed to up to the remote.
//
// Best effort and reported, never fatal: the commits are safe in the sandbox
// either way, and a workspace with no remote has nowhere to send them. What
// it prevents is the clone that follows handing over a repository missing
// every artifact the chain just produced (OR-418).
func pushPlanBranch(out io.Writer, ws *workspace.Workspace) {
	if strings.TrimSpace(ws.Task.Remote) == "" {
		return
	}
	branch := headBranch(ws.RepoDir())
	if branch == "" {
		return
	}
	cmd := exec.Command("git", "-C", ws.RepoDir(), "push", "-u", "origin", branch)
	if b, err := cmd.CombinedOutput(); err != nil {
		ui.Warn(out, "could not send %s to the remote: %s", branch, strings.TrimSpace(string(b)))
		fmt.Fprintf(out, "  %s\n", ui.Dim(out,
			"your copy will not have what the stages committed until it gets there"))
		return
	}
	ui.Ok(out, "pushed", "%s -> origin", branch)
	// So the copy made next opens on it. Recorded here rather than left to
	// noteBranchChange, which only runs after a SUPERVISED stage: a chain
	// whose branch was set at provisioning and never changed has no record
	// at all, and the copy would open on the repository default.
	if ws.Task.PlanBranch != branch {
		ws.Task.PlanBranch = branch
		_ = ws.SaveTask()
	}
}

// registerPlanProject records the project-key to repository mapping, so the
// `orion watch KEY` the chain ends by printing actually runs.
//
// It was nobody's job. `orion init` does it, but only for a repository it
// recognises as its own source -- and a workspace the chain provisioned has
// the sandbox as its source, so running init in the copy tries to create a
// SECOND sandbox for a project that already has one. The chain knows the
// key, the workspace and the remote at this point; the registry is the one
// place that does not (FOUND ON A REAL PROJECT: the chain finished and its
// own next command refused with "not a registered project").
//
// Reported and never fatal: the tree is created and the copy is made either
// way, and a mapping is one command to add.
func registerPlanProject(out io.Writer, ws *workspace.Workspace) {
	key := strings.TrimSpace(ws.Task.TrackerKey())
	if key == "" {
		return
	}
	// The source is the operator's own copy when there is one, since that is
	// the checkout a person works in; the sandbox otherwise.
	source := strings.TrimSpace(ws.Task.CheckoutPath)
	if p, err := expandPath(source); err == nil && source != "" {
		if _, statErr := os.Stat(filepath.Join(p, ws.ID, ".git")); statErr == nil {
			source = filepath.Join(p, ws.ID)
		} else {
			source = p
		}
	}
	if source == "" {
		source = ws.RepoDir()
	}
	var channel string
	if ws.Task.Slack != nil {
		channel = ws.Task.Slack.ID
	}
	err := registry.Bind(workspace.Home(), registry.Entry{
		Key: key, Source: source, Workspace: ws.ID,
		Channel: channel, Remote: ws.Task.Remote,
	})
	if err != nil {
		ui.Warn(out, "%v", err)
		return
	}
	ui.Ok(out, "registered", "%s -> %s", key, source)
}
