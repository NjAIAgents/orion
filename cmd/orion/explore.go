package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/orion-sdlc/orion/internal/actors"
	"github.com/orion-sdlc/orion/internal/events"
	"github.com/orion-sdlc/orion/internal/supervisor"
	"github.com/orion-sdlc/orion/internal/workspace"
)

// `orion explore "<question>"` -- one narrow question about a repository,
// answered by a subagent in its own context (OR-183).
//
// The caller here is not Orion. It is the agent Orion is already running: it
// hits a thing it has not read, and instead of grepping until it finds out --
// leaving every file it opened in its context, re-sent on every remaining
// turn -- it asks. The reading happens in a context that is discarded and
// only the answer, plus the paths it came from, crosses back.
//
// Which is why this is a command and not a function. The asking run is a
// separate process mid-flight; a Bash call is the only thing it can reach
// Orion with.

// Bounds for the explore subagent, tighter than any run that changes
// something. This is a locate-and-report, and an explore that is still
// hunting after fifteen turns is no longer the cheap answer it was asked to
// be -- at which point the caller reading for itself is the better deal.
const (
	exploreMaxMinutes = 5
	exploreMaxTurns   = 15
)

// exploreOptions is what the subagent runs with, split from runExplore so the
// actor, model and prompt it is configured with can be asserted without
// spawning a process -- the same reason fixOptions is split from fixRun.
func exploreOptions(key, question string) supervisor.Options {
	return supervisor.Options{
		Stage:      "explore",
		Prompt:     supervisor.ExplorePrompt(question),
		MaxMinutes: exploreMaxMinutes,
		MaxTurns:   exploreMaxTurns,
		// Its own actor on its own pinned cheap model rather than the asking
		// run's: an expensive model doing a cheap read is how this pattern
		// stops being a saving. Attributed to the same ticket so the spend is
		// its own row in that ticket's cost report instead of vanishing --
		// several of these across one run add up, and a cost report that
		// cannot see them cannot say so.
		Actor: events.ActorExplore, Key: key,
		Model:  actors.Model(events.ActorExplore),
		Effort: actors.Effort(events.ActorExplore),
	}
}

// exploreAnswer is what crosses back: the answer, and the files it was read
// out of.
//
// Separated because they are weighed differently. An answer with a path can
// be opened and checked by whoever asked; an answer without one cannot be
// audited at all, and is worth less for exactly that reason.
type exploreAnswer struct {
	Answer string
	Paths  []string
}

// parseExploreAnswer splits the subagent's report into the answer and its
// cited paths.
//
// The PATHS line is taken from the END and only the last one counts: an
// answer that quotes the instruction, or names the marker while explaining
// itself, would otherwise have its own prose read as a citation. Everything
// before it is the answer, verbatim.
//
// A missing PATHS line is not an error. The subagent was told to write one,
// but a caller that got a usable answer and threw it away over a formatting
// miss would have spent the money for nothing; no paths and unparseable
// paths land in the same place, which is "unsourced".
func parseExploreAnswer(final string) exploreAnswer {
	lines := strings.Split(strings.TrimSpace(final), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		rest, ok := strings.CutPrefix(strings.TrimSpace(lines[i]), supervisor.ExplorePathsPrefix)
		if !ok {
			continue
		}
		return exploreAnswer{
			Answer: strings.TrimSpace(strings.Join(lines[:i], "\n")),
			Paths:  splitPaths(rest),
		}
	}
	return exploreAnswer{Answer: strings.TrimSpace(final)}
}

// splitPaths reads the comma-separated tail of a PATHS line. "none" is the
// instructed way to say there are none, so it is dropped rather than
// recorded as a file called none.
func splitPaths(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		p = strings.TrimSpace(p)
		if p == "" || strings.EqualFold(p, "none") {
			continue
		}
		out = append(out, p)
	}
	return out
}

// runExplore answers one question about a repository and prints the answer.
//
// Every failure exits non-zero with the same instruction: read it yourself.
// That is the fallback the whole design rests on -- this can only ever save
// the caller reading, never prevent it -- and an error message that left the
// caller wondering whether to wait would cost more than the subagent saves.
func runExplore(args []string) {
	repo := argFlag(args, "--repo", ".")
	rest := positional(args, "--repo", "--key")
	if len(rest) == 0 {
		fmt.Fprintln(os.Stderr, `orion: usage: orion explore [--repo DIR] "<question>"`)
		os.Exit(64)
	}
	question := rest[0]

	ws, err := exploreWorkspace()
	if err != nil {
		exploreGiveUp(err)
	}
	// The worktree the asking run is working in, not the sandbox clone: the
	// question is about the code as it stands right now, mid-change, and the
	// clone does not have that.
	top, err := topLevel(repo)
	if err != nil {
		exploreGiveUp(err)
	}
	jobWS := *ws
	jobWS.RepoPath = top

	key := argFlag(args, "--key", "")
	if key == "" {
		key = exploreKey(ws, top)
	}
	if key == "" {
		// Not fatal. An unattributed answer is still an answer; an answer
		// withheld over bookkeeping is not.
		fmt.Fprintln(os.Stderr, "orion: no ticket recorded for this branch; "+
			"this explore will not appear in any ticket's cost report")
	}

	res, err := supervisor.Run(&jobWS, exploreOptions(key, question))
	if err != nil {
		exploreGiveUp(err)
	}
	if res.ExitCode != 0 || strings.TrimSpace(res.Final) == "" {
		exploreGiveUp(fmt.Errorf("the explore run returned nothing (exit %d): %s",
			res.ExitCode, res.Reason))
	}

	ans := parseExploreAnswer(res.Final)
	logExplore(&jobWS, key, question, ans)
	printExplore(os.Stdout, ans)
}

// printExplore writes the answer back to whoever asked, and says plainly when
// it is unsourced.
//
// The warning is not decoration. A subagent that under-reports loses
// information silently, and the caller cannot otherwise tell "the repository
// does not contain this" from "I did not find it" -- the first is a fact to
// design around, the second is a gap, and treating the second as the first is
// how this pattern produces a wrong architectural decision rather than merely
// a wasted call.
func printExplore(w io.Writer, ans exploreAnswer) {
	fmt.Fprintln(w, ans.Answer)
	if len(ans.Paths) == 0 {
		fmt.Fprintln(w, "\norion: this answer cites no file, so nothing here can be checked. "+
			"Treat it as unproven and confirm it yourself before building on it.")
		return
	}
	fmt.Fprintf(w, "\n%s %s\n", supervisor.ExplorePathsPrefix, strings.Join(ans.Paths, ", "))
}

// logExplore writes the answer and its citations into the event log.
//
// What a subagent returns is all anyone ever sees of it: the reading is in a
// context that no longer exists. So an answer that is not written down here
// is gone the moment this process exits, and with it any way to ask later
// what the run was told and whether it was true. The paths go in Detail
// rather than only inside the prose because they are what makes the answer
// checkable, and a citation nobody can query is not much of one.
func logExplore(ws *workspace.Workspace, key, question string, ans exploreAnswer) {
	l, err := events.Open(events.Path(ws.Dir), events.Event{})
	if err != nil {
		return
	}
	defer func() { _ = l.Close() }()

	sourced := "unsourced"
	if len(ans.Paths) > 0 {
		sourced = strings.Join(ans.Paths, ", ")
	}
	l.Emit(events.Event{
		Kind: events.KindNote, Actor: events.ActorExplore, Key: key,
		Msg: "explored: " + oneLine(question) + " -- " + oneLine(ans.Answer) + " [" + sourced + "]",
		Detail: map[string]any{
			"question": question,
			"answer":   ans.Answer,
			"paths":    ans.Paths,
		},
	})
}

// exploreWorkspace opens the workspace the asking run belongs to.
//
// From ORION_WORKSPACE, which the supervisor already exports into every run
// it starts, so the asking agent does not have to be told an id it has no
// reason to know. Opened rather than reconstructed: the workspace carries the
// task record the supervisor writes back to, and a hand-built one would
// overwrite it with blanks.
func exploreWorkspace() (*workspace.Workspace, error) {
	id := strings.TrimSpace(os.Getenv("ORION_WORKSPACE"))
	if id == "" {
		return nil, fmt.Errorf("ORION_WORKSPACE is not set, so there is no workspace to run in.\n" +
			"  explore is for an agent inside a run Orion started")
	}
	return workspace.Open(id)
}

// exploreKey resolves which ticket this worktree belongs to, from the branch
// it is standing on. Empty when nothing recorded one.
func exploreKey(ws *workspace.Workspace, dir string) string {
	out, err := exec.Command("git", "-C", dir, "rev-parse", "--abbrev-ref", "HEAD").Output()
	if err != nil {
		return ""
	}
	key, _ := workspace.KeyOfBranch(ws, strings.TrimSpace(string(out)))
	return key
}

func topLevel(dir string) (string, error) {
	out, err := exec.Command("git", "-C", dir, "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return "", fmt.Errorf("%s is not inside a git repository", dir)
	}
	return strings.TrimSpace(string(out)), nil
}

// exploreGiveUp is the one exit for every failure: say what broke, then send
// the caller back to reading the repository itself.
func exploreGiveUp(err error) {
	fmt.Fprintf(os.Stderr, "orion: explore could not answer: %v\n"+
		"  Read the repository yourself instead; nothing is waiting on this.\n", err)
	os.Exit(1)
}
