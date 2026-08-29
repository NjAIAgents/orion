package main

// `orion conflict verify` checks a merge resolution for dropped changes.
//
// Exposed as a command rather than left as an internal call because the thing
// it guards is done by whoever resolves the conflict -- today a person, once
// the dispatch half of OR-186 lands an agent -- and both need the same
// answer. A guardrail only one caller can reach is one the other caller
// silently does without.

import (
	"fmt"
	"os"

	"github.com/orion-sdlc/orion/internal/conflict"
	"github.com/orion-sdlc/orion/internal/ui"
)

const conflictUsage = `orion conflict verify <base> <ours> <theirs> [--dir PATH]

Checks the resolution committed at HEAD against the two sides it came from:
conflict markers, merge-tool litter, and any file BOTH sides changed that
came out byte-identical to one of them -- which means the other side's edit
is not in the result.

Exits non-zero when something needs a person. It never writes or pushes.
`

func runConflict(args []string) {
	if len(args) == 0 || args[0] != "verify" {
		fmt.Fprint(os.Stderr, conflictUsage)
		os.Exit(64)
	}
	rest := args[1:]

	dir := "."
	var refs []string
	for i := 0; i < len(rest); i++ {
		if rest[i] == "--dir" && i+1 < len(rest) {
			i++
			dir = rest[i]
			continue
		}
		refs = append(refs, rest[i])
	}
	if len(refs) != 3 {
		fmt.Fprint(os.Stderr, conflictUsage)
		os.Exit(64)
	}

	r, err := conflict.Verify(dir, refs[0], refs[1], refs[2])
	exitOn(err)

	w := os.Stdout
	if r.Safe() {
		ui.Ok(w, "verified", "no dropped side, no markers, no litter")
		return
	}
	// Reported, never resolved. The two automatic "fixes" available here are
	// to re-take one side or to edit the file, and both are the mistake this
	// exists to catch.
	for _, f := range r.Findings() {
		ui.Warn(w, "%s: %s", f.File, f.Why)
	}
	ui.Warn(w, "%d finding(s); do not push this resolution until each is explained",
		len(r.Findings()))
	os.Exit(1)
}
