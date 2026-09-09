package main

// `orion decompose <KEY> [tasks.md]` creates a tracker tree from a
// /speckit.tasks task list.
//
// The same code `orion plan` runs as its decompose step when the feature
// directory holds a tasks.md (plandecompose.go), here for a person to run
// by hand against any task list. The decompose STAGE prompt still names
// /pm-plan (or whatever the project's toolkit block configures), and the
// chain falls back to it when there is no tasks.md, so a project with no
// spec-kit output decomposes as it always did, on any tracker. The tracker
// seam this would need to reach Linear, Notion and GitHub has not landed
// (OR-303), so the native route is Jira-only and says so.

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/orion-sdlc/orion/internal/config"
	"github.com/orion-sdlc/orion/internal/decompose"
	"github.com/orion-sdlc/orion/internal/tracker"
	"github.com/orion-sdlc/orion/internal/ui"
)

// decomposeBackend opens the tracker the tree is created in. A variable so
// the chain's tests can stand in a fake and create nothing real.
var decomposeBackend = func(root string) (decompose.Backend, error) {
	return backendFor(config.Load(root).Tracker.Provider)
}

// backendFor picks the backend the project's orion.json asks for.
//
// A CONFIGURED PROVIDER WITH NO BACKEND IS REFUSED, not quietly served by
// Jira. tracker.provider has been a settable field since before this route
// existed and nothing read it, so a project that had written "linear" there
// got a Jira tree built against Jira credentials -- either a confusing
// failure or, with both configured, a tree in the wrong tracker. Jira is
// the only backend shipped, and the refusal says so and names the way
// through (OR-303).
func backendFor(provider string) (decompose.Backend, error) {
	switch p := strings.ToLower(strings.TrimSpace(provider)); p {
	case "", "jira":
		jira, err := tracker.NewJiraFromEnv()
		if err != nil {
			return nil, err
		}
		return decompose.NewJiraBackend(jira), nil
	default:
		return nil, fmt.Errorf(
			"tracker.provider is %q, and decompose can only create a tree in jira.\n"+
				"  Decompose through the stage instead, which works on any tracker:\n"+
				"    orion plan <KEY>   (the chain falls back to the stage without a tasks.md)\n"+
				"  Or set tracker.provider to \"jira\" in orion.json to use this route.", p)
	}
}

// errDeclined is the answer "no" to the one confirmation: nothing was
// created, and the caller decides whether that is a stop or a shrug.
var errDeclined = errors.New("not confirmed, so nothing was created")

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

	ask := func(prompt string) bool { return confirmCreate(os.Stdout, prompt) }
	err := decomposeTree(os.Stdout, ".", project, path, ask)
	if errors.Is(err, errDeclined) {
		ui.Warn(os.Stdout, "%v", err)
		return
	}
	if errors.Is(err, errTreeStopped) {
		os.Exit(1)
	}
	exitOn(err)
}

// errTreeStopped is a partial failure already reported in full -- what was
// created, what was not, where it stopped -- so the caller exits without
// repeating it.
var errTreeStopped = errors.New("the tree stopped partway; see above")

// decomposeTree reads one task list and creates its tree in project,
// after one confirmation for the whole of it. root is the repository the
// queue label is configured in.
//
// One body for two callers: `orion decompose` and the chain's decompose
// step. The stop-and-report on a partial failure is the same on both --
// what exists now, what does not, and that a re-run links the first and
// creates only the second -- because the boundary is the property, not the
// command that hit it.
func decomposeTree(out io.Writer, root, project, path string, ask confirmer) error {
	text, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	tree, err := decompose.Parse(string(text), path)
	if err != nil {
		return err
	}
	// The queue label, so the tree is claimable the moment it exists:
	// stories and epic-level tasks, per Tree.Queue's rule. From the
	// project's own config, which is where `orion watch` reads it too.
	queue := strings.TrimSpace(config.Load(root).Tracker.QueueLabel)
	if queue == "" {
		queue = tracker.QueueLabelDefault
	}
	tree.Queue(queue)

	backend, err := decomposeBackend(root)
	if err != nil {
		return err
	}
	plan, err := decompose.Build(tree, backend, project)
	if err != nil {
		return err
	}

	decompose.Preview(out, plan)

	if plan.NewCount() == 0 {
		fmt.Fprintln(out)
		ui.Ok(out, "nothing to do", "every item is already in %s", project)
		return nil
	}

	fmt.Fprintf(out, "\n  %s\n", ui.Dim(out,
		"Issues in a shared tracker are seen by other people and cannot be cleanly\n"+
			"  withdrawn, so this asks once for the whole tree and creates nothing without\n"+
			"  an answer."))

	if !ask(fmt.Sprintf("Create %d items in %s?", plan.NewCount(), project)) {
		return errDeclined
	}

	res, applyErr := decompose.Apply(plan, backend)
	for _, k := range res.Created {
		ui.Ok(out, "created", "%s", k)
	}
	if applyErr != nil {
		// The boundary, stated: what exists now, and what does not. A re-run
		// searches by the identity label, finds exactly the items above, and
		// makes only the rest.
		ui.Fail(out, "%v", applyErr)
		fmt.Fprintf(out, "\n  %d created, %d already there, and the tree stops at %q.\n",
			len(res.Created), len(res.Linked), res.FailedAt)
		fmt.Fprintf(out, "  Re-run the same command once the cause is fixed: it links what is above\n"+
			"  and creates only what is missing.\n")
		return fmt.Errorf("%w: %v", errTreeStopped, applyErr)
	}
	fmt.Fprintf(out, "\n  %d created, %d already there", len(res.Created), len(res.Linked))
	if res.Links > 0 {
		fmt.Fprintf(out, ", %d ordering link(s)", res.Links)
	}
	fmt.Fprintln(out, ".")
	// An ordering statement that could not be made is said plainly: the
	// tree exists either way, but the queue will start work the artifact
	// meant to hold back, and that is the operator's to know.
	for _, f := range res.LinkFailed {
		ui.Warn(out, "%s", f)
	}
	return nil
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
