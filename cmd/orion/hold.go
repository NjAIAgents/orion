package main

// Binding the environmental hold to the real machine.
//
// internal/work owns WHAT a hold means; everything here is the wiring it
// refuses to do itself, because a package that shells out to `claude` and
// `gh` in its own constructor cannot be tested without them.

import (
	"fmt"
	"os"
	"strings"

	"github.com/orion-sdlc/orion/internal/doctor"
	"github.com/orion-sdlc/orion/internal/slack"
	"github.com/orion-sdlc/orion/internal/work"
	"github.com/orion-sdlc/orion/internal/workspace"
)

// slackForHold is the confirmation path, detected and never required.
func slackForHold() work.SlackAPI {
	c, err := slack.FromEnv()
	if err != nil {
		return nil
	}
	return c
}

// recheckEnv re-runs the one doctor check that speaks to a fault.
func recheckEnv(kind work.FaultKind) (string, string) {
	return doctor.Recheck(string(kind), ".")
}

// preflightEnv is the free, local half of `orion doctor`, asked before a
// ticket is claimed.
//
// Two checks, and both are chosen for costing nothing: `claude auth status
// --json` spends no tokens, and finding nj-agents is a filesystem lookup.
// The network-bound checks are deliberately absent -- gh and Jira announce
// themselves through the calls Orion already makes to them, and making every
// claim wait on two round trips to prove what the next line proves anyway is
// a tax on the healthy case.
func preflightEnv() (work.Fault, bool) {
	for _, kind := range []work.FaultKind{work.FaultClaudeAuth, work.FaultNJAgents} {
		label, detail := doctor.Recheck(string(kind), ".")
		// Only FAIL. A WARN is a degraded capability, and holding the whole
		// queue for one would stop work that would otherwise have succeeded.
		if label == "FAIL" {
			return work.NewFault(kind, detail), true
		}
	}
	return work.Fault{}, false
}

// runResetHeld is the manual path: an operator who has just fixed something
// and does not want to wait for the next tick.
//
// It re-checks anyway. "I fixed it" typed at a terminal is the same claim as a
// reaction in Slack, and the whole point of the re-check is that a claim is not
// a fact. What being here in person DOES buy is the fault nothing can check --
// a quota reset has no free probe, and a person who has just looked at the
// provider's dashboard is better evidence than Orion can otherwise get.
func runResetHeld(args []string) {
	home := workspace.Home()
	want := strings.ToLower(strings.TrimSpace(argFlag(args, "--held", "")))

	standing := work.Holds(home)
	if len(standing) == 0 {
		fmt.Println("orion: nothing is held.")
		return
	}
	var only work.FaultKind
	if want != "" {
		for _, h := range standing {
			if strings.EqualFold(string(h.Kind), want) {
				only = h.Kind
			}
		}
		if only == "" {
			fmt.Fprintf(os.Stderr, "orion: nothing is held for %q. Held: %s\n",
				want, strings.Join(kindsOf(standing), ", "))
			os.Exit(64)
		}
	}

	left := work.Release(home, work.ReleaseDeps{
		Slack: slackForHold(), Recheck: recheckEnv, Manual: true, Only: only,
	}, os.Stdout)
	if len(left) == 0 {
		fmt.Println("orion: every hold is cleared; the next tick starts work again.")
		return
	}
	fmt.Fprintf(os.Stderr, "orion: still held: %s\n", strings.Join(kindsOf(left), ", "))
	os.Exit(1)
}

func kindsOf(hs []work.Hold) []string {
	out := make([]string, 0, len(hs))
	for _, h := range hs {
		out = append(out, string(h.Kind))
	}
	return out
}
