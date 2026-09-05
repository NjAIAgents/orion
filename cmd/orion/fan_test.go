package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/orion-sdlc/orion/internal/events"
	"github.com/orion-sdlc/orion/internal/fanout"
	"github.com/orion-sdlc/orion/internal/supervisor"
)

// The acceptance criterion, as a fact about the run rather than a sentence in
// a prompt: a subagent that writes cannot run the test suite, because it has
// no tool that can run anything.
func TestAFanChildCannotRunAnything(t *testing.T) {
	opts := fanChildOptions("OR-230", events.ActorImplementer,
		fanout.Assignment{Package: "./internal/a", Task: "change it"})

	if len(opts.AllowedTools) == 0 {
		t.Fatal("no allowlist, so the child has every tool the CLI offers")
	}
	for _, tool := range opts.AllowedTools {
		switch tool {
		case "Bash", "BashOutput", "KillShell", "Task":
			t.Errorf("a writing subagent was allowed %s; it can run the suite", tool)
		}
	}
	// It must still be able to do the job it was given.
	var canWrite bool
	for _, tool := range opts.AllowedTools {
		if tool == "Edit" || tool == "Write" {
			canWrite = true
		}
	}
	if !canWrite {
		t.Error("the child cannot edit anything, so it cannot do the work either")
	}

	denied := strings.Join(opts.DeniedTools, ",")
	for _, tool := range []string{"Bash", "Task"} {
		if !strings.Contains(denied, tool) {
			t.Errorf("%s is not on the denylist; the allowlist alone depends on the "+
				"permission mode in force", tool)
		}
	}
}

// A fan of several packages dispatches children that all run as the same
// actor and stage; the package is the only thing that tells one child's
// roster line from another's (OR-335), so fanChildOptions must carry it as
// About rather than only in the prompt.
func TestFanChildOptionsCarriesThePackageAsAbout(t *testing.T) {
	opts := fanChildOptions("OR-230", events.ActorImplementer,
		fanout.Assignment{Package: "./internal/a", Task: "change it"})

	if opts.About != "./internal/a" {
		t.Errorf("About = %q, want the package so the roster line names which package "+
			"this child was given", opts.About)
	}
}

// The other half of this -- that the bound survives the trip into argv -- is
// TestAToolBoundedRunSaysSoOnItsCommandLine in internal/supervisor, next to
// the builder it asserts against.

// A failed child is reported against its own package and makes the whole fan
// non-zero. A parent that read a partial fan as a complete one would go on to
// build a tree with a package nobody wrote.
func TestAFailedChildIsNamedAndFailsTheFan(t *testing.T) {
	var out bytes.Buffer
	code := printFanResults(&out, []string{"m/a", "m/b"}, []supervisor.FanResult{
		{Result: &supervisor.Result{Final: "added the validator"}},
		{Result: &supervisor.Result{ExitCode: 1, Reason: "wall clock"}},
	})
	if code == 0 {
		t.Error("a fan with a failed child exited 0")
	}
	body := out.String()
	for _, want := range []string{"m/a", "added the validator", "m/b", "FAILED", "1 of 2"} {
		if !strings.Contains(body, want) {
			t.Errorf("report does not mention %q:\n%s", want, body)
		}
	}
	if !strings.Contains(body, "Do that work yourself") {
		t.Error("the report does not say who picks up the package nobody wrote")
	}
}

// Every fan, including a clean one, ends by telling the parent that nothing
// has been verified. That instruction is the other half of "only the parent
// verifies": children that cannot test plus a parent that does not is a
// change nobody checked.
func TestACleanFanStillTellsTheParentToRunTheSuite(t *testing.T) {
	var out bytes.Buffer
	code := printFanResults(&out, []string{"m/a"}, []supervisor.FanResult{
		{Result: &supervisor.Result{Final: "done"}},
	})
	if code != 0 {
		t.Errorf("exit = %d, want 0", code)
	}
	if !strings.Contains(out.String(), "Run the suite ONCE") {
		t.Errorf("report does not tell the parent to verify:\n%s", out.String())
	}
}
