package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/orion-sdlc/orion/internal/actors"
	"github.com/orion-sdlc/orion/internal/config"
	"github.com/orion-sdlc/orion/internal/events"
	"github.com/orion-sdlc/orion/internal/supervisor"
	"github.com/orion-sdlc/orion/internal/workspace"
)

// `orion explore "<question>" ["<question>" ...]` -- narrow questions about a
// repository, each answered by its own subagent in its own context (OR-183),
// and all of them at once (OR-229).
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
//
// Several questions in one call rather than several calls is the whole of
// OR-229. An implementer starting a ticket does not have one question, it has
// four, and asked one at a time they cost four round trips of the asking run's
// own wall clock while it sits idle waiting -- so in practice it greps
// instead, which is the cost this command exists to remove. Dispatched
// through supervisor.Fan they run concurrently under the project's own
// max_concurrent_children cap, and the asking run pays for one wait.

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
		Stage:  "explore",
		Prompt: supervisor.ExplorePrompt(question),
		// The question itself, so a fan of five explores reads as five
		// different questions rather than five copies of the word "explore".
		About:      question,
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

// exploredQuestion is one question and whatever came back for it: an answer,
// or the reason there is none.
//
// Per question rather than per batch, because one child failing must not cost
// the caller the siblings that succeeded -- that is the failure policy
// supervisor.Fan already implements, and a caller that collapsed N results
// into one error would throw away work already paid for.
type exploredQuestion struct {
	Question string
	Answer   exploreAnswer
	Err      error
}

// exploreAll asks every question at once and returns the answers in the order
// they were asked, whatever order they came back in.
//
// One Options per question, each its own actor row in the ticket's cost
// report exactly as a single explore already is: fanning out adds
// concurrency, not a second accounting path.
func exploreAll(ws *workspace.Workspace, key string, questions []string) []exploredQuestion {
	jobs := make([]supervisor.Options, len(questions))
	for i, q := range questions {
		jobs[i] = exploreOptions(key, q)
	}
	out := make([]exploredQuestion, len(questions))
	for i, r := range supervisor.Fan(ws, jobs) {
		out[i] = exploredQuestion{Question: questions[i]}
		switch {
		case r.Result == nil:
			out[i].Err = r.Err
			if out[i].Err == nil {
				out[i].Err = fmt.Errorf("the explore run produced no result at all")
			}
		case r.Result.ExitCode != 0:
			out[i].Err = fmt.Errorf("the explore run returned nothing (exit %d): %s",
				r.Result.ExitCode, r.Result.Reason)
		case strings.TrimSpace(r.Result.Final) == "":
			out[i].Err = fmt.Errorf("the explore run finished without writing an answer")
		default:
			out[i].Answer = parseExploreAnswer(r.Result.Final)
		}
	}
	return out
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

// runExplore answers the questions it is given and prints the answers.
//
// Failing EVERY question exits non-zero with the same instruction: read it
// yourself. That is the fallback the whole design rests on -- this can only
// ever save the caller reading, never prevent it -- and an error message that
// left the caller wondering whether to wait would cost more than the subagent
// saves.
func runExplore(args []string) {
	repo := argFlag(args, "--repo", ".")
	questions := positional(args, "--repo", "--key")
	if len(questions) == 0 {
		fmt.Fprintln(os.Stderr,
			`orion: usage: orion explore [--repo DIR] "<question>" ["<question>" ...]`)
		os.Exit(64)
	}

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

	logExploreDispatch(&jobWS, key, questions)
	asked := exploreAll(&jobWS, key, questions)
	// Every question is recorded, answered or not, and BEFORE the give-up
	// path below: a question that failed is a question that was asked and
	// paid for, and one that leaves no event is indistinguishable afterwards
	// from one nobody ever asked. In a batch that is the difference between
	// "the third question came back empty" and "something about this run is
	// missing an answer".
	for _, q := range asked {
		if q.Err != nil {
			logExploreFailure(&jobWS, key, q.Question, q.Err)
			continue
		}
		logExplore(&jobWS, key, q.Question, q.Answer)
	}
	// Every question failed, so there is nothing to print and the caller has
	// to go and read for itself -- the fallback this whole command rests on.
	// One failure among several is NOT this: the answers that did arrive are
	// worth more than the consistency of failing the batch.
	if firstErr, all := exploreAllFailed(asked); all {
		exploreGiveUp(firstErr)
	}
	printExploreAll(os.Stdout, asked)
}

// exploreAllFailed reports whether nothing came back at all, and the first
// reason why.
func exploreAllFailed(asked []exploredQuestion) (error, bool) {
	var first error
	for _, q := range asked {
		if q.Err == nil {
			return nil, false
		}
		if first == nil {
			first = q.Err
		}
	}
	return first, len(asked) > 0
}

// printExploreAll writes every answer back to whoever asked.
//
// A single question prints exactly as it always did -- the caller of one
// explore is reading prose, not a report, and a header above one answer is
// noise. Several are labelled with the question they answer, because answers
// arrive in a batch and an unlabelled one belongs to whichever question the
// reader guesses.
func printExploreAll(w io.Writer, asked []exploredQuestion) {
	if len(asked) == 1 && asked[0].Err == nil {
		printExplore(w, asked[0].Answer)
		return
	}
	for i, q := range asked {
		if i > 0 {
			fmt.Fprintln(w)
		}
		fmt.Fprintf(w, "Q: %s\n", q.Question)
		if q.Err != nil {
			fmt.Fprintf(w, "orion: this one could not be answered: %v\n"+
				"Read it yourself; the other answers here are unaffected.\n", q.Err)
			continue
		}
		printExplore(w, q.Answer)
	}
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

// logExploreDispatch records that a batch of questions went out together.
//
// The per-answer notes below say what each subagent came back with, but
// nothing in them says the three ran AT ONCE rather than one after another,
// and that is the property this exists to make queryable: an answer arriving
// is the same event either way. Written before the fan-out rather than after,
// so a run that dies mid-flight still shows what it had out.
//
// Only for a batch. A single question is the shape this command already had,
// and a "dispatched 1 concurrently" line in the log for every one of them is
// noise that makes the batches harder to find, not easier.
func logExploreDispatch(ws *workspace.Workspace, key string, questions []string) {
	if len(questions) < 2 {
		return
	}
	l, err := events.Open(events.Path(ws.Dir), events.Event{})
	if err != nil {
		return
	}
	defer func() { _ = l.Close() }()

	maxConcurrent := config.Load(ws.RepoDir()).Limits.MaxConcurrentChildren
	l.Emit(events.Event{
		Kind: events.KindNote, Actor: events.ActorExplore, Key: key,
		Model: actors.Model(events.ActorExplore),
		Msg: fmt.Sprintf("dispatched %d explore subagents concurrently (cap %d)",
			len(questions), maxConcurrent),
		Detail: map[string]any{
			"questions":      questions,
			"max_concurrent": maxConcurrent,
		},
	})
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

// logExploreFailure records a question that came back with nothing, and why.
//
// Its own event rather than a line inside the dispatch record, so a reader
// filtering on the question gets the same shape whether it was answered or
// not: which question, and what happened to it. Without one, a batch of four
// leaves three events and nothing saying which question is missing -- and a
// question that failed is one that ran, took a subagent's turn and spent
// money, so it is not the same thing as a question nobody asked.
func logExploreFailure(ws *workspace.Workspace, key, question string, cause error) {
	l, err := events.Open(events.Path(ws.Dir), events.Event{})
	if err != nil {
		return
	}
	defer func() { _ = l.Close() }()

	l.Emit(events.Event{
		Kind: events.KindNote, Actor: events.ActorExplore, Key: key,
		Model: actors.Model(events.ActorExplore),
		Msg:   "could not explore: " + oneLine(question) + " -- " + oneLine(cause.Error()),
		Detail: map[string]any{
			"question": question,
			"error":    cause.Error(),
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
