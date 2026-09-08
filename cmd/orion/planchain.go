package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

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
	answer := strings.TrimSpace(ask("Where? (e.g. ~/code/thing -- blank to stay in the sandbox)"))
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
func cloneStep(out io.Writer, ws *workspace.Workspace, _ confirmer) error {
	dest := strings.TrimSpace(ws.Task.CheckoutPath)
	if err := cloneWorkspace(out, ws, dest); err != nil {
		ui.Warn(out, "%v", err)
		fmt.Fprintf(out, "  Retry when you like: orion clone %s %s\n", ws.ID, dest)
	}
	return nil
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
	_, err := os.Stat(filepath.Join(dest, ".git"))
	return err == nil
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
			fmt.Fprintf(out, "\n  %s\n", ui.Dim(out, fmt.Sprintf("= done  %d/%d  %s", i+1, len(planStages), s.Stage)))
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
				fmt.Fprintf(out, "\n  %s\n", ui.Dim(out, fmt.Sprintf(
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
			err := s.Frame(out, ws, ask)
			// A fallback step that found nothing to work from hands the
			// stage to the supervised runner below, which is what it would
			// have been before the native route existed.
			if errors.Is(err, errNotApplicable) && s.Fallback {
				err = nil
				s.Frame = nil
			}
			if s.Frame == nil {
				// fall through to the stage runner
			} else if err != nil {
				fmt.Fprintln(out)
				ui.Fail(out, "%v", err)
				fmt.Fprintf(out, "\n  %s\n", ui.Dim(out, fmt.Sprintf(
					"stopped at %d of %d steps, at %s", i+1, len(planStages), s.Stage)))
				fmt.Fprintf(out, "  resume: orion plan %s\n", planKeyOf(ws))
				return done
			} else {
				done++
				ui.Ok(out, "done", "%s", s.Stage)
				continue
			}
		}

		res, err := run(ws, s.Stage)
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
			ui.Fail(out, "%v", err)
			fmt.Fprintf(out, "\n  %s\n", ui.Dim(out, fmt.Sprintf(
				"stopped at %d of %d stages", i+1, len(planStages))))
			fmt.Fprintf(out, "  re-run this stage: orion run %s --stage %s\n", ws.ID, s.Stage)
			return done
		}
		done++
		ui.Ok(out, "done", "%s", s.Stage)
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
