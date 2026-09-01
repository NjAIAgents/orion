package main

import (
	"strings"
	"testing"
)

// The guard rail, and the reason `release` is a noun with subcommands.
//
// "Release" means two things here: a Jira milestone, and cutting the binary
// (a tag plus the Homebrew tap and the Scoop bucket). The failure modes are
// not symmetrical -- creating a version by mistake is reversible, publishing
// one is public and is not -- so the bare verb must never resolve to an
// action. Anything that does not NAME what it wants gets usage and a
// non-zero exit (OR-190).
func TestBareReleaseNamesNoAction(t *testing.T) {
	for _, args := range [][]string{
		nil,
		{},
		{"publish"}, // still unwired, and staying that way
		{"cut"},
		{"--yes"},
		// OR-116 spent `ship` on the publishing command, so it is no longer
		// on this list -- see TestOnlyShipIsWiredOfTheReservedPublishingVerbs
		// in releaseship_test.go, which holds the other two to the rule.
	} {
		if got := releaseAction(args); got != "" {
			t.Errorf("release %v resolved to %q; a command that does not name "+
				"its action must not reach one", args, got)
		}
	}
}

func TestReleaseSubcommandsResolve(t *testing.T) {
	cases := map[string]string{
		"create": "create",
		"add":    "add",
		"close":  "close",
		"list":   "list",
		"ls":     "list",
		"help":   "help",
		"--help": "help",
		"-h":     "help",
	}
	for arg, want := range cases {
		if got := releaseAction([]string{arg}); got != want {
			t.Errorf("release %q resolved to %q, want %q", arg, got, want)
		}
	}
}

// The usage text has to be clear about which "release" this is, because the
// whole hazard is that a reader assumes the other one.
//
// Since OR-116 both meanings live on this list, which makes the sentence
// separating them load-bearing rather than decorative: it must name
// milestones, name ship as the exception, and say ship is irreversible. A
// reader who skims the subcommand list and stops must still not be able to
// mistake `ship` for a tracker operation.
func TestReleaseUsageSeparatesTheMilestoneVerbsFromTheOneThatPublishes(t *testing.T) {
	low := strings.ToLower(releaseUsage)
	for _, want := range []string{
		"milestone",
		"except ship",
		"does not build,\ntag or publish anything",
		"irreversible",
	} {
		if !strings.Contains(low, want) {
			t.Errorf("usage never says %q, so a reader cannot tell the milestone "+
				"verbs from the one that cuts a real release:\n%s", want, releaseUsage)
		}
	}
	// And ship's own entry has to say what it touches, since that is the
	// difference a reader is deciding on.
	for _, want := range []string{"homebrew tap", "scoop bucket", "--beta", "--dry-run"} {
		if !strings.Contains(low, want) {
			t.Errorf("ship's usage entry never mentions %q:\n%s", want, releaseUsage)
		}
	}
}
