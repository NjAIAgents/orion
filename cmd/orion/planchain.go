package main

import (
	"bufio"
	"fmt"
	"io"
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

// runPlanChain runs the planning stages in order, pausing after each.
//
// Returns the number of stages that completed, so the caller can report where
// a stopped chain got to. A stage that fails, blocks, or is declined ends the
// chain: everything after it reads what it wrote.
func runPlanChain(out io.Writer, ws *workspace.Workspace, run stageRunner, ask confirmer) int {
	done := 0
	for i, s := range planStages {
		// The first stage is not asked about. The operator just confirmed the
		// whole chain and its cost to get here; asking again before anything
		// has happened is a prompt with no new information in it.
		if i > 0 {
			fmt.Fprintln(out)
			if !ask(fmt.Sprintf("Continue to %d/%d %s -- %s?",
				i+1, len(planStages), s.Stage, s.What)) {
				fmt.Fprintf(out, "\n  %s\n", ui.Dim(out, fmt.Sprintf(
					"stopped after %d of %d stages, at your request", done, len(planStages))))
				fmt.Fprintf(out, "  resume: orion run %s --stage %s\n", ws.ID, s.Stage)
				return done
			}
		}

		fmt.Fprintf(out, "\n%s\n", ui.Heading(out, fmt.Sprintf(
			"%d/%d  %s", i+1, len(planStages), s.Stage)))

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
