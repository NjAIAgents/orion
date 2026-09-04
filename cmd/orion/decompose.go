package main

// `orion decompose <KEY> [tasks.md]` creates a tracker tree from a
// /speckit.tasks task list.
//
// OPT-IN, and it stays opt-in. The decompose stage prompt still names
// /pm-plan (or whatever the project's toolkit block configures), so a
// project with no spec-kit output decomposes exactly as it did before, on
// any tracker. This command is the native route for the projects that DO
// have a tasks.md, and the tracker seam it would need to reach Linear,
// Notion and GitHub has not landed yet -- so it is Jira-only, says so, and
// is invoked by a person rather than by a stage.

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/orion-sdlc/orion/internal/decompose"
	"github.com/orion-sdlc/orion/internal/tracker"
	"github.com/orion-sdlc/orion/internal/ui"
)

func runDecompose(args []string) {
	pos := positional(args)
	if len(pos) == 0 {
		fmt.Fprintln(os.Stderr, "orion decompose <KEY> [path/to/tasks.md]")
		os.Exit(64)
	}
	project := strings.ToUpper(strings.TrimSpace(pos[0]))

	path := ""
	if len(pos) > 1 {
		path = pos[1]
	} else {
		found, err := findTasks(".")
		exitOn(err)
		path = found
	}

	text, err := os.ReadFile(path)
	exitOn(err)

	tree, err := decompose.Parse(string(text), path)
	exitOn(err)

	jira, err := tracker.NewJiraFromEnv()
	exitOn(err)

	backend := decompose.NewJiraBackend(jira)
	plan, err := decompose.Build(tree, backend, project)
	exitOn(err)

	decompose.Preview(os.Stdout, plan)

	if plan.NewCount() == 0 {
		fmt.Println()
		ui.Ok(os.Stdout, "nothing to do", "every item is already in %s", project)
		return
	}

	fmt.Printf("\n  %s\n", ui.Dim(os.Stdout,
		"Issues in a shared tracker are seen by other people and cannot be cleanly\n"+
			"  withdrawn, so this asks once for the whole tree and creates nothing without\n"+
			"  an answer."))

	if !confirmCreate(os.Stdout, fmt.Sprintf("Create %d items in %s?", plan.NewCount(), project)) {
		ui.Warn(os.Stdout, "not confirmed, so nothing was created")
		return
	}

	res, applyErr := decompose.Apply(plan, backend)
	for _, k := range res.Created {
		ui.Ok(os.Stdout, "created", "%s", k)
	}
	if applyErr != nil {
		// The boundary, stated: what exists now, and what does not. A re-run
		// searches by the identity label, finds exactly the items above, and
		// makes only the rest.
		ui.Fail(os.Stdout, "%v", applyErr)
		fmt.Printf("\n  %d created, %d already there, and the tree stops at %q.\n",
			len(res.Created), len(res.Linked), res.FailedAt)
		fmt.Printf("  Re-run the same command once the cause is fixed: it links what is above\n" +
			"  and creates only what is missing.\n")
		os.Exit(1)
	}
	fmt.Printf("\n  %d created, %d already there.\n", len(res.Created), len(res.Linked))
}

// stdinIsTTY answers "is anybody there".
//
// A variable so a test can assert BOTH answers. Reading os.Stdin directly
// meant the unattended case could only be tested when the test runner
// happened to redirect stdin, and a test that skips itself is not evidence
// that an unattended run creates nothing.
var stdinIsTTY = func() bool {
	fi, err := os.Stdin.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

// findTasks locates the task list when none was named.
//
// /speckit.tasks writes specs/<nnn-slug>/tasks.md, one per feature, so more
// than one is the normal state of a repository -- and picking the newest, or
// the first, would decompose a feature the operator did not ask for. Ambiguity
// is reported rather than resolved.
func findTasks(root string) (string, error) {
	matches, err := filepath.Glob(filepath.Join(root, "specs", "*", "tasks.md"))
	if err != nil {
		return "", err
	}
	switch len(matches) {
	case 0:
		return "", fmt.Errorf("no specs/*/tasks.md here. Name the file:\n" +
			"  orion decompose <KEY> path/to/tasks.md")
	case 1:
		return matches[0], nil
	default:
		return "", fmt.Errorf("several task lists here; name the one to decompose:\n  %s",
			strings.Join(matches, "\n  "))
	}
}

// confirmCreate asks once, for the whole tree.
//
// A non-interactive run answers NO and says so. It is deliberately not
// overridable by a flag: the preview exists so a person sees the tree before
// it is real, and a switch that skipped the person would remove the only
// guarantee this command makes.
func confirmCreate(out io.Writer, prompt string) bool {
	if !stdinIsTTY() {
		fmt.Fprintln(out, "\n  Not a terminal, so nothing was created. The tree above is what a run\n"+
			"  with someone present to confirm it would create.")
		return false
	}
	fmt.Fprintf(out, "\n%s [y/N] ", prompt)
	var ans string
	_, _ = fmt.Fscanln(confirmIn, &ans)
	ans = strings.ToLower(strings.TrimSpace(ans))
	return ans == "y" || ans == "yes"
}
