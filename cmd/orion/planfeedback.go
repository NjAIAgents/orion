package main

// `orion request-plan-changes <KEY> <what you want changed>` is the operator's
// half of the plan gate: free text in, a revised plan out (OR-280).
//
// WHY A FILE AND NOT A FLAG ON THE RE-RUN. A stage reads files, not
// conversation -- the same rule the whole chain is built on -- and this text
// has to survive the gap between the person typing it and a plan stage that
// may run minutes or days later, possibly from a different terminal or from
// the web UI shelling out to this same command. `orion answer` already
// established the shape: write what the human said into the committed
// artifact the stage reads, and the stage needs to know nothing about who
// asked or how.
//
// WHY IT APPENDS. Round two's feedback does not cancel round one's: a plan
// that was corrected once and regressed is exactly the thing the record is
// for, and a file that only ever holds the latest request cannot show it.
//
// WHY EVERYTHING AFTER THE KEY IS TEXT. Feedback is prose, and prose starts
// with whatever it starts with -- "-1 on the cache", "--force is wrong here",
// "drop the `rm -rf` step". No flag is parsed after the key, so none of that
// can be read as one; and nothing here reaches a shell, so metacharacters are
// characters. A command that quietly ate the first word of the feedback would
// send the planner a subtly different instruction, which is the failure this
// avoids by having no flags at all.
//
// IT DOES NOT RUN THE STAGE. Orion sequences stages (docs/decisions/0001);
// this records the input one of them reads and names the command that runs
// it. That also keeps it identical from a terminal and from the UI, since
// neither gets a chained agent run it did not ask for.

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/orion-sdlc/orion/internal/config"
	"github.com/orion-sdlc/orion/internal/supervisor"
	"github.com/orion-sdlc/orion/internal/ui"
)

const planChangesUsage = `orion request-plan-changes <KEY|id> <what you want changed>
       everything after the key is the feedback, verbatim
       e.g. orion request-plan-changes ORPAY the refund flow needs its own stage`

// parsePlanChangesArgs splits the arguments into the workspace to act on and
// the feedback text.
//
// The first argument is the target and every argument after it is text,
// joined with single spaces so an unquoted sentence typed at a shell reads
// the way it was typed. No flags, deliberately: see the file comment.
func parsePlanChangesArgs(args []string) (target, text string, err error) {
	if len(args) == 0 {
		return "", "", fmt.Errorf("orion request-plan-changes needs a project key or workspace id")
	}
	target = strings.TrimSpace(args[0])
	if target == "" || strings.HasPrefix(target, "-") {
		// A target that looks like a flag is a mistyped command, not a
		// project called "--force". Refusing it here is what lets everything
		// AFTER it be taken literally without ambiguity.
		return "", "", fmt.Errorf("orion request-plan-changes: %q is not a project key or "+
			"workspace id; the key comes first and the feedback after it", args[0])
	}
	text = strings.TrimSpace(strings.Join(args[1:], " "))
	if text == "" {
		return "", "", fmt.Errorf("orion request-plan-changes: no feedback given, so there is " +
			"nothing for the planner to act on")
	}
	return target, text, nil
}

// planFeedbackEntry is one request, as it is written into the file.
//
// Timestamped and separated by a heading so several rounds stay readable and
// the planner can tell which point was made when -- and fenced, so that text
// beginning with a `#` or a `-` stays the operator's words rather than
// becoming this document's own structure.
func planFeedbackEntry(at time.Time, text string) string {
	return fmt.Sprintf("## %s\n\n```\n%s\n```\n", at.UTC().Format(time.RFC3339), sanitiseFence(text))
}

// sanitiseFence keeps the operator's text inside its fence.
//
// A line of three backticks in the feedback would otherwise close the block
// early and the rest would read as instructions to the planning agent rather
// than as a quoted request. Indenting the closing marker is the smallest
// change that keeps every character and cannot end the block.
func sanitiseFence(text string) string {
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	for i, l := range lines {
		if strings.HasPrefix(strings.TrimSpace(l), "```") {
			lines[i] = " " + l
		}
	}
	return strings.Join(lines, "\n")
}

// appendPlanFeedback writes one entry into the feedback file under repoDir,
// creating the file (with its heading) when this is the first request.
// Returns the repo-relative path written.
func appendPlanFeedback(repoDir, rel string, at time.Time, text string) error {
	abs := filepath.Join(repoDir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return err
	}
	body := planFeedbackEntry(at, text)
	if _, err := os.Stat(abs); os.IsNotExist(err) {
		body = "# Plan feedback\n\n" +
			"What an operator asked to be changed about the plan, newest last.\n" +
			"Written by `orion request-plan-changes`; the plan stage reads it on its\n" +
			"next run and revises the plan against it.\n\n" + body
	} else if err != nil {
		return err
	} else {
		body = "\n" + body
	}

	f, err := os.OpenFile(abs, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.WriteString(body)
	return err
}

// commitPlanFeedback commits the file, because the contract the stage relies
// on is the COMMITTED artifact: feedback sitting uncommitted in a worktree
// does not survive the branch or the pull request that carries the work on.
// A commit that cannot be made is reported and not fatal -- the text is
// written either way, and losing it because git was busy would be worse.
func commitPlanFeedback(out io.Writer, repoDir, rel string) {
	if _, err := gitIn(repoDir, "add", "--", rel); err != nil {
		fmt.Fprintf(out, "  %s\n", ui.Dim(out, "feedback written but not committed: "+err.Error()))
		return
	}
	if _, err := gitIn(repoDir, "commit", "-qm", "docs: record requested plan changes"); err != nil {
		fmt.Fprintf(out, "  %s\n", ui.Dim(out, "feedback written but not committed: "+err.Error()))
		return
	}
	ui.Ok(out, "committed", "%s", rel)
}

// runRequestPlanChanges is `orion request-plan-changes`.
func runRequestPlanChanges(args []string) {
	target, text, err := parsePlanChangesArgs(args)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		fmt.Fprintln(os.Stderr, "usage: "+planChangesUsage)
		os.Exit(64)
	}
	ws, _, err := resolveWorkspace(target)
	exitOn(err)

	cfg := config.Load(ws.RepoDir())
	rel := supervisor.PlanFeedbackArtifact(cfg, ws.Task.Slug)
	w := os.Stdout
	if err := appendPlanFeedback(ws.RepoDir(), rel, time.Now(), text); err != nil {
		fmt.Fprintf(os.Stderr, "orion request-plan-changes: %v\n", err)
		os.Exit(1)
	}
	ui.Ok(w, "recorded", "%s", rel)
	commitPlanFeedback(w, ws.RepoDir(), rel)

	// The command that acts on it, named rather than run: this writes the
	// input, the chain runs the stage.
	fmt.Fprintf(w, "\nnext: orion plan %s --from plan\n", planKeyOf(ws))
	fmt.Fprintf(w, "  %s\n", ui.Dim(w, "that re-runs the plan stage, which reads the file above"))
}
