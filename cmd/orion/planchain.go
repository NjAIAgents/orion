package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/orion-sdlc/orion/internal/supervisor"
	"github.com/orion-sdlc/orion/internal/tracker"
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

// cloneAfterChain makes the copy the operator asked for, once the stages have
// committed something worth copying.
//
// Best effort and last: the planning work is done and committed by now, so a
// failed clone costs a convenience rather than the run. It says what went
// wrong and names the command to retry with.
func cloneAfterChain(out io.Writer, ws *workspace.Workspace) {
	dest := strings.TrimSpace(ws.Task.CheckoutPath)
	if dest == "" {
		return
	}
	if err := cloneWorkspace(os.Stdout, ws, dest); err != nil {
		ui.Warn(out, "%v", err)
		fmt.Fprintf(out, "  Retry when you like: orion clone %s %s\n", ws.ID, dest)
	}
}

// runPlanChain runs the planning stages in order, pausing after each.
//
// Returns the number of stages that completed, so the caller can report where
// a stopped chain got to. A stage that fails, blocks, or is declined ends the
// chain: everything after it reads what it wrote.
func runPlanChain(out io.Writer, ws *workspace.Workspace, run stageRunner, ask confirmer) int {
	done := 0
	for i, s := range planStages {
		// A step whose work is already there is reported and skipped, not
		// asked about: the question "continue to spec?" has no answer when
		// the spec is committed and passes its gate. This is what makes a
		// re-run of `orion plan` a resume rather than a repeat -- and it
		// counts as done, so a chain that skips everything still ends.
		if s.Done != nil && s.Done(ws) {
			fmt.Fprintf(out, "\n  %s\n", ui.Dim(out, fmt.Sprintf("= done  %d/%d  %s", i+1, len(planStages), s.Stage)))
			done++
			continue
		}

		// The first stage is not asked about. The operator just confirmed the
		// whole chain and its cost to get here; asking again before anything
		// has happened is a prompt with no new information in it.
		if i > 0 {
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
		if s.Frame != nil {
			if err := s.Frame(out, ws, ask); err != nil {
				fmt.Fprintln(out)
				ui.Fail(out, "%v", err)
				fmt.Fprintf(out, "\n  %s\n", ui.Dim(out, fmt.Sprintf(
					"stopped at %d of %d steps, at %s", i+1, len(planStages), s.Stage)))
				fmt.Fprintf(out, "  resume: orion plan %s\n", planKeyOf(ws))
				return done
			}
			done++
			ui.Ok(out, "done", "%s", s.Stage)
			continue
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
	var b tracker.Binding
	if len(ws.Task.Tracker) > 0 && json.Unmarshal(ws.Task.Tracker, &b) == nil && b.Key != "" {
		return b.Key
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
