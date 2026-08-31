package collect

import (
	"fmt"
	"io"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/orion-sdlc/orion/internal/config"
	"github.com/orion-sdlc/orion/internal/events"
	"github.com/orion-sdlc/orion/internal/lessons"
	"github.com/orion-sdlc/orion/internal/notify"
	"github.com/orion-sdlc/orion/internal/ui"
	"github.com/orion-sdlc/orion/internal/workspace"
)

// The CI fix loop.
//
//	CI fails -> fetch the failing job's log -> agent fixes on the SAME branch
//	         -> push -> CI runs again -> repeat, bounded
//
// The agent that wrote the branch is the right one to fix it: the failure is
// specific, the code is its own, and the alternative is a person copying a CI
// log into a chat window by hand -- which is the manual loop this whole
// project exists to remove.
//
// Everything below is about knowing when to STOP. An unbounded version is a
// machine that spends money all night oscillating between two broken states,
// and it does not crash, so nobody finds out until the bill.
func tryFix(res Result, key string, pr PR, cfg config.Config, branch string,
	opts Options, deps Deps, ws *workspace.Workspace, log *events.Log, w io.Writer) (bool, Result) {

	// A person working this branch by hand always wins, and is checked
	// before anything else -- including before an attempt is recorded, so
	// a held ticket never spends part of its fix-attempt budget while
	// waiting for a human to finish (OR-130).
	if dir := worktreeOrRepo(ws, branch); manuallyLocked(dir) {
		ui.Say(w, key, events.ActorOrion, ui.VerbWaiting,
			"%s is locked for manual work (%s); the fix loop is leaving it alone", branch, manualLockName)
		log.Emit(events.Event{Kind: events.KindNote, Actor: events.ActorOrion,
			Msg: "skipped fix attempt: " + manualLockName + " present"})
		return false, res
	}

	fp := Fingerprint(pr.Detail)
	state := loadFixes(ws.Dir).States[key]
	// The ceiling comes from config with zero meaning the shipped default,
	// the same shape QA's own ceiling reads by (config.QA.Rounds). The
	// fallback used to be spelled out here, which made the two numbers look
	// unrelated and left this one unsettable.
	max := cfg.CI.Attempts()

	// Brake one, and it is checked BEFORE the ceiling on purpose: the same
	// failure twice means the last attempt achieved nothing. Spending the
	// remaining attempts would prove only that it can fail identically three
	// times. Raising the ceiling does not reach past this -- an attempt beyond
	// the second is only ever reached by a run presenting a DIFFERENT failure
	// each round, which is the only kind of run a further attempt can help.
	if state.Repeating(fp) {
		giveUp(key, ws, log, w, fmt.Sprintf(
			"the last fix produced an identical failure, so it is not making progress (attempt %d)",
			state.Count()))
		return false, res
	}

	// Brake two: the ceiling, for a loop that changes something every round
	// and still never converges.
	if state.Count() >= max {
		giveUp(key, ws, log, w, fmt.Sprintf(
			"%d fix attempts were spent without a green build", state.Count()))
		return false, res
	}

	if opts.DryRun {
		ui.Ok(w, "would", "%s: attempt %d of %d to fix CI", key, state.Count()+1, max)
		return true, res
	}

	// Counted BEFORE the run. A crash mid-fix must not refund the attempt --
	// a ceiling that resets whenever the process dies is no ceiling at all,
	// and process death is exactly what a runaway loop tends to produce.
	state, err := recordAttempt(ws.Dir, key, branch, fp, pr.Detail, pr.Head)
	if err != nil {
		ui.Warn(w, "could not record the fix attempt: %v", err)
		return false, res
	}

	attempt := state.Count()

	// The build went red, so the next party IS an agent and money starts
	// being spent -- the one boundary after a pull request opens where that
	// is true, and until now the only one of the three that was completely
	// unmarked (OR-189). Through the same ui.Stage internal/work uses: a
	// second hand-rolled copy in this package is exactly the drift OR-176
	// produced, and boundary lines that differ between the two packages are
	// the same failure in a new place.
	ui.Stage(w, log, ui.Handoff{Key: key, From: "ci", To: "fix",
		By: events.ActorCI, Next: events.ActorDevOps,
		Detail: fmt.Sprintf("CI failed; attempt %d of %d", attempt, max)})
	log.Emit(events.Event{Kind: events.KindCI, Actor: events.ActorOrion,
		Msg: fmt.Sprintf("fix attempt %d of %d: %s", attempt, max, firstLine(pr.Detail))})

	tell(w, log, notify.Event{
		Key: key, Channel: channelOf(ws), Level: notify.Warning, Workspace: ws.ID,
		Actor: events.ActorDevOps,
		Title: fmt.Sprintf("%s: fixing a CI failure (attempt %d of %d)", key, attempt, max),
		Body: fmt.Sprintf("*The build went red and Orion is trying to fix it.*\n\n"+
			"*What failed*\n%s\n\n• pull request  %s\n\n"+
			"_Nothing is required of you. If it cannot fix it in %d attempts, "+
			"it stops and says so._", quote(pr.Detail), link(pr.URL, "open it"), max),
	})

	pushed, summary, denied, err := deps.Fix(ws, key, branch, pr.Detail, log)
	if err != nil {
		giveUp(key, ws, log, w, "the fix run failed: "+err.Error())
		res.Err = err
		return false, res
	}
	if !pushed {
		// A policy denial is not the agent failing to see the fix -- it saw
		// it and was not permitted to apply it. Reporting it the same way as
		// "does not know how to fix this" contradicts itself in one sentence
		// (OR-174): a root cause named exactly right is not evidence of not
		// knowing. It also does not spend an attempt: no attempt was ever
		// possible, so the count just recorded is retracted below.
		if denied != nil {
			blockedByPolicy(key, ws, log, w, *denied)
			return false, res
		}
		// Exit 0 with nothing pushed means the agent could not see what to
		// change. Another identical attempt would produce the same nothing.
		// The agent's own closing message says why, when it said anything --
		// carried into the give-up reason instead of being dropped, so a
		// person reading the log learns what the agent actually saw rather
		// than only that it gave up (OR-129).
		reason := "the agent produced no change, so it does not know how to fix this"
		if summary != "" {
			reason += ": " + summary
		}
		giveUp(key, ws, log, w, reason)
		return false, res
	}

	// The same summary the run just stated its root cause in (OR-157) is
	// what proposeLesson reads back at merge time -- attached to the attempt
	// now because this is the only place it is ever known. Best-effort: a
	// lesson field failing to write must never turn a pushed fix into a
	// reported failure.
	if err := recordRootCause(ws.Dir, key, summary); err != nil {
		ui.Warn(w, "could not record the fix's root cause: %v", err)
	}

	// The one-line summary is the point of OR-129: without it this event and
	// the "run complete" line above it are both pure bookkeeping -- turns,
	// tokens, cost -- and say nothing about what was actually wrong or what
	// changed to fix it.
	msg := fmt.Sprintf("pushed a fix for CI (attempt %d)", attempt)
	if summary != "" {
		msg += ": " + summary
	}
	log.Emit(events.Event{Kind: events.KindPush, Actor: events.ActorImplementer, Msg: msg})
	if summary != "" {
		ui.Ok(w, "ok", "%s: pushed a fix; CI will run again -- %s", key, summary)
	} else {
		ui.Ok(w, "ok", "%s: pushed a fix; CI will run again", key)
	}
	res.Changed = true
	res.Verdict = VerdictPending // it is building again, not failing
	return true, res
}

// lessonNotice is what recordLesson learned, held until the merge it came
// from has been fully reported. Printing it eagerly is the OR-178 bug: a
// retrospective bookkeeping request rendered inside the merge report reads as
// a live problem WITH that merge.
type lessonNotice struct {
	key         string
	recordErr   error // set means: announce a "could not record" warning, nothing else
	text        string
	signature   string
	strikes     int
	evidence    []string
	anchor      string // e.g. "3f38370, 2026-08-29 06:13" -- names the run this is ABOUT
	channel     string
	workspaceID string
}

// recordLesson files a lesson candidate when a branch that went red was
// fixed and then merged, and returns what should be announced once the merge
// report has finished -- nil when there is nothing to say yet.
//
// This is the shape a lesson wants: a mistake with a correction attached, both
// observed by the system rather than inferred. CI said what broke, an agent
// changed something, and the merge proves the change was right. Nothing here
// decides the lesson is TRUE -- it only says this happened, here, on this date,
// and files it for a human to accept or throw away.
//
// It observes on every such merge; the two-strike rule inside the store is
// what decides when a person is actually asked. A build that goes red once is
// a bad afternoon. The same build going red the same way a second time is a
// rule nobody has written down yet.
//
// Called BEFORE the fix history is cleared -- this is the only moment both
// halves (what broke, and that it was fixed) exist together -- so the read
// cannot be deferred to announcement time the way the printing can.
//
// Best-effort throughout. The merge has already happened, and a memory-keeping
// failure must never turn a successful merge into a reported failure.
func recordLesson(key string, pr PR, state FixState, source string,
	ws *workspace.Workspace, log *events.Log) *lessonNotice {

	if len(state.Attempts) == 0 {
		return nil // it merged without ever going red; there is no mistake to learn from
	}

	first := state.Attempts[0]

	// The root cause comes from the LAST attempt: the one whose fix actually
	// stuck, since the merge proves it. Keying on it -- not the CI check name
	// -- is the point of OR-177: a repo with one job matrix produces the same
	// check name for every failure it will ever have, so that fallback
	// collapses a gofmt violation, a broken build and a failing test into one
	// lesson that only ever says "CI sometimes fails." An attempt with no
	// stated root cause -- an older run, or a fix loop that gave up before
	// ever stating one -- has nothing transferable to propose, so it
	// proposes NOTHING rather than falling back to the check name. A store of
	// vacuous lessons is worse than an empty one: a reviewer stops reading.
	rootCause := normalizeRootCause(state.Attempts[len(state.Attempts)-1].RootCause)
	if rootCause == "" {
		return nil
	}
	// The same identifier the session hook scopes lessons by, so a lesson
	// recorded here is one the next session in that repo actually reads.
	project := filepath.Base(source)
	if source == "" {
		project = ws.ID
	}
	last := state.Attempts[len(state.Attempts)-1].At

	// The text carries only the normalized root cause -- the mechanism, not
	// the specifics of this one sighting. The CI check name, ticket key,
	// attempt count and PR url differ between two occurrences of the same
	// mistake, so they go in the evidence instead, which is per-sighting and
	// is what a reviewer reads to judge the proposal.
	text := rootCause
	evidence := fmt.Sprintf("%s in %s on %s: CI failed with %q, %d fix attempt(s), merged %s",
		key, project, last.Format("2006-01-02"), state.Attempts[0].Detail, state.Count(), pr.URL)

	store := lessons.New(workspace.Home())
	c, err := store.Observe(lessons.Proposal{
		Text:     text,
		Kind:     lessons.KindReview,
		Project:  project,
		Evidence: evidence,
		At:       last,
		Stack:    lessons.DetectStack(source),
	})
	if err != nil {
		return &lessonNotice{key: key, recordErr: err}
	}
	if log != nil {
		log.Emitf(events.KindNote, events.ActorOrion,
			"lesson observed (%d of %d strikes): %s", c.Strikes, lessons.Strikes, text)
	}
	if !c.Ready() {
		// Seen once. Saying nothing is deliberate: asking a person about
		// every one-off is how an approval prompt becomes noise they dismiss
		// without reading, which costs more than the lesson is worth.
		return nil
	}
	return &lessonNotice{
		key: key, text: text, signature: c.Signature, strikes: c.Strikes,
		evidence: c.Evidence, anchor: anchorFor(first),
		channel: channelOf(ws), workspaceID: ws.ID,
	}
}

// anchorFor names the run a lesson is ABOUT: the commit that failed, and
// when. Without this, "CI failed ... and the branch needed a fix" is a
// past-tense clause with nothing marking it retrospective or saying which
// run it describes -- a reader watching a live merge cannot tell it is not
// about the merge in front of them (OR-178).
func anchorFor(a Attempt) string {
	when := a.At.Local().Format("2006-01-02 15:04")
	if a.Head == "" {
		return when
	}
	sha := a.Head
	if len(sha) > 9 {
		sha = sha[:9]
	}
	return fmt.Sprintf("%s, %s", sha, when)
}

// announceLesson prints and notifies about a lesson recorded earlier by
// recordLesson. Called strictly AFTER the merge has finished being reported,
// never between its lines: a request to review a past mistake is not a fact
// about the merge just announced, and printing it there is what turned a
// clean merge into something that read as a supervisor shipping over a red
// build (OR-178).
//
// Given its own level -- reused from the informational one already used for
// the matching Slack message -- rather than WARNING. Nothing is wrong and
// nothing is at risk; a bookkeeping entry waiting for review is not a live
// problem, and WARNING is the word this renderer uses for those.
func announceLesson(n *lessonNotice, w io.Writer, log *events.Log) {
	if n == nil {
		return
	}
	if n.recordErr != nil {
		ui.Warn(w, "could not record a lesson proposal: %v", n.recordErr)
		return
	}
	ui.Ok(w, "note", "%s: from an earlier failure on this ticket (%s): %s\n"+
		"          a lesson is waiting for your decision -- approve it with: orion lessons approve %s",
		n.key, n.anchor, n.text, n.signature)
	tell(w, log, notify.Event{
		Key: n.key, Channel: n.channel, Level: notify.Info, Workspace: n.workspaceID,
		Title: "A lesson is waiting for your decision",
		Body: fmt.Sprintf("*This has now happened %d times, so it may be a pattern worth remembering.*\n\n"+
			"From an earlier failure on this ticket (%s):\n%s\n\n*Where it was seen*\n%s\n\n"+
			"Nothing has been recorded yet. Approve it and it is injected into every future session's "+
			"CLAUDE.md; reject it and it is never asked about again.\n\n"+
			"```\norion lessons approve %s\norion lessons reject %s\n```",
			n.strikes, n.anchor, quote(n.text), quote(strings.Join(n.evidence, "\n")), n.signature, n.signature),
	})
}

var (
	// A branch name of the form "fix/OR-114-gofmt", matched before the file
	// path pattern below since it would otherwise be swallowed as one.
	rootCauseBranchRe = regexp.MustCompile(`\b[\w][\w-]*/[A-Z][A-Z0-9]{1,9}-\d+[\w-]*\b`)
	// A path or filename with an extension, e.g. "internal/work/work_test.go"
	// or "scripts/test.sh". Requiring the extension keeps ordinary prose like
	// "before build/test" intact -- it has no dot, so it is not a path.
	rootCauseFileRe = regexp.MustCompile(`\b[\w][\w./-]*\.[a-zA-Z]{1,6}\b`)
	rootCauseKeyRe  = regexp.MustCompile(`\b[A-Z][A-Z0-9]{1,9}-\d+\b`)
)

// normalizeRootCause strips what is unique to ONE sighting -- a file path, a
// branch name, a ticket key -- so two occurrences of the same underlying
// mistake collapse to the same lesson even when they hit different files on
// different tickets. What survives is the mechanism: why the code produced
// the failure, which is the part a future session can actually act on.
func normalizeRootCause(s string) string {
	s = rootCauseBranchRe.ReplaceAllString(s, "a branch")
	s = rootCauseFileRe.ReplaceAllString(s, "a file")
	s = rootCauseKeyRe.ReplaceAllString(s, "")
	s = strings.Join(strings.Fields(s), " ")
	return strings.Trim(s, " ,-")
}

// giveUp records why the loop stopped, without marking the ticket.
//
// Deliberately does not relabel: the caller falls through to the normal
// failing path, which owns the tracker changes. Two places writing the same
// labels is how a ticket ends up in two states at once.
//
// And deliberately does NOT clear the attempt history. Clearing here looks
// like tidying up after a finished episode, but `orion collect` is a command
// people re-run -- and on a timer, it re-runs itself. A history wiped at the
// moment of giving up means the very next pass starts from zero attempts and
// tries again, so the ceiling bounds one invocation rather than the problem.
// That is an unbounded spend wearing a bound's clothing.
//
// The history is cleared in exactly one place: a successful merge.
func giveUp(key string, ws *workspace.Workspace, log *events.Log, w io.Writer, why string) {
	ui.Warn(w, "%s: giving up on fixing CI -- %s", key, why)
	log.Emit(events.Event{Kind: events.KindFailed, Actor: events.ActorOrion,
		Msg: "stopped fixing: " + why})
}

// blockedByPolicy reports a fix run refused by the sandbox, and hands the
// agent's own diagnosis to a human rather than discarding it with the run.
//
// Deliberately NOT giveUp: "giving up" says the agent tried and failed, and
// this is the opposite -- it was not permitted to try. Retrying changes
// nothing here, which is also why the loop still stops (tryFix returns
// false, same as giveUp), but unlike giveUp this attempt did not prove
// anything about the agent, so it is retracted rather than counted.
func blockedByPolicy(key string, ws *workspace.Workspace, log *events.Log, w io.Writer, denied PolicyDenial) {
	reason := fmt.Sprintf("blocked by policy: %s(%s)", denied.Tool, denied.Path)
	if denied.Rule != "" {
		reason += fmt.Sprintf(" matches the protected rule %q", denied.Rule)
	}
	ui.Warn(w, "%s: %s -- no further attempt can fix this; a human must apply the change", key, reason)
	log.Emit(events.Event{Kind: events.KindBlocked, Actor: events.ActorOrion,
		Msg: "stopped fixing: " + reason})

	if denied.HandOff != "" {
		ui.Warn(w, "%s: hand-off for a human -- the agent's own diagnosis:\n%s", key, denied.HandOff)
		log.Emit(events.Event{Kind: events.KindNote, Actor: events.ActorImplementer,
			Msg: "hand-off for a human: " + denied.HandOff})
	}

	// This attempt proved nothing about the agent; it never had the chance
	// to try. Retracting keeps the ceiling measuring what it is meant to
	// measure -- attempts the agent actually got to make.
	if err := retractAttempt(ws.Dir, key); err != nil {
		ui.Warn(w, "%s: could not retract the denied attempt: %v", key, err)
	}
}
