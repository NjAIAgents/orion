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
		{"publish"}, // the dangerous verb, deliberately not wired
		{"cut"},
		{"ship"},
		{"--yes"},
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
func TestReleaseUsageSaysItDoesNotPublish(t *testing.T) {
	low := strings.ToLower(releaseUsage)
	for _, want := range []string{"milestone", "does not build, tag or publish"} {
		if !strings.Contains(low, want) {
			t.Errorf("usage never says %q, so a reader cannot tell this from cutting "+
				"a real release:\n%s", want, releaseUsage)
		}
	}
}
