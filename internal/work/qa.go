package work

// The QA stage.
//
//	implementer commits -> QA derives cases, writes tests, runs them
//	                    -> findings? -> implementer fixes -> QA re-verifies
//	                    -> clean, or the round ceiling and a person is told
//
// It runs after the slice is committed and BEFORE the branch is pushed, so
// the tests QA wrote and any fix they forced are part of the same pull
// request as the change they are about. Verifying after the pull request
// opens would put the evidence in a second one, and a reviewer would be
// reading the code without it.
//
// A loop, not a gate. QA reports; Orion routes; the implementer fixes. QA
// never blocks on its own authority, which is deliberate -- a verification
// actor that can stop a run has to be right every time, and it is not. So the
// worst it can do is spend two rounds and tell a person what is still open.
// The ceiling is what keeps that promise honest: without it, "never blocks"
// would be true of the outcome and false of the bill.
//
// Every failure inside here is a warning, never a failed run. The change is
// committed and correct-as-far-as-anyone-knows by this point, and turning "QA
// could not be reached" into a failed ticket would throw away finished work
// over the verification of it.

import (
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	"github.com/orion-sdlc/orion/internal/actors"
	"github.com/orion-sdlc/orion/internal/config"
	"github.com/orion-sdlc/orion/internal/events"
	"github.com/orion-sdlc/orion/internal/njagents"
	"github.com/orion-sdlc/orion/internal/notify"
	"github.com/orion-sdlc/orion/internal/supervisor"
	"github.com/orion-sdlc/orion/internal/ui"
	"github.com/orion-sdlc/orion/internal/workspace"
)

// qaOutcome is what the stage ended with.
type qaOutcome struct {
	Ran      bool
	Clean    bool
	Rounds   int    // fix rounds spent
	Findings string // open findings, when it ended without them being cleared
}

// Verdict is what the stage is worth saying on the boundary out of it: the
// result, not the fact that it ran. "verified clean" and "findings still
// open" are different enough to change what a reader does next, and a
// boundary that said only "qa -> push" would hide which one happened.
func (o qaOutcome) Verdict() string {
	switch {
	case o.Clean && o.Rounds > 0:
		return fmt.Sprintf("verified clean after %d fix round(s)", o.Rounds)
	case o.Clean:
		return "verified clean"
	case o.Findings != "":
		return fmt.Sprintf("findings still open after %d fix round(s)", o.Rounds)
	}
	return "unverified"
}

// qaJob is what one stage run needs. A struct rather than eleven parameters
// because the caller already holds all of it and threading it positionally is
// how the wrong worktree gets passed.
type qaJob struct {
	Key         string
	Summary     string
	Description string
	// ImplSession is the implementer's session, so a finding is a message in
	// the conversation that produced the code rather than a new run that pays
	// for the whole context again. Empty means a fresh run.
	ImplSession string
	// Actor is whichever actor route() picked to work this ticket -- not
	// necessarily the implementer. A fix round must resume THIS actor: a
	// docs ticket whose findings were "fixed" by the backend developer would
	// commit under one actor's session and report under another's, and the
	// run would carry two different authors for one branch (OR-171).
	Actor      string
	WS         *workspace.Workspace // the JOB worktree, not the shared clone
	MaxMinutes int
	MaxTurns   int
	// BaseSHA is the commit this ticket's branch was cut from, before the
	// implementer touched anything. It is what every test QA writes must be
	// demonstrated failing against -- see redgreen.go and OR-156. Empty skips
	// that check rather than failing the stage over it.
	BaseSHA string
}

// runQA drives the exchange to clean, to the ceiling, or to nothing at all.
func runQA(job qaJob, cfg config.Config, opts Options, deps Deps,
	log *events.Log, w io.Writer) (out qaOutcome) {

	key := job.Key
	if !cfg.QA.On() {
		ui.Say(w, key, events.ActorOrion, ui.VerbOK, "QA is switched off for this project")
		return qaOutcome{}
	}
	if deps.Supervise == nil {
		return qaOutcome{}
	}

	// Captured before QA's first turn, so the tests it goes on to write can
	// be told apart from the implementer's own -- the diff between this and
	// HEAD once the stage ends is QA's, and only QA's, regardless of how many
	// fix rounds run in between.
	preQA, _ := headSHA(job.WS.RepoDir())
	defer func() {
		// The commit FIRST, then the check that reads commits. Everything
		// downstream of this stage reads history rather than the worktree --
		// red-before-green diffs preQA..HEAD, the push carries commits, the
		// pull request shows commits -- so a test that is still only on disk
		// here is a test none of them can see (OR-234).
		commitQAWork(job, cfg, log, w)
		// Only when QA actually ran: a stage that never started wrote no
		// tests, and there is nothing to prove red about a run that failed
		// before its first turn (qaRan below handles that case).
		if out.Ran {
			reportRedBeforeGreen(job, preQA, log, w)
		}
	}()

	cases := deriveCases(job, deps, log, w)

	// Authoring is fanned across subagents when there is enough to fan
	// (OR-305). It writes files and returns; the QA session below still runs,
	// still reads the diff, and still owns the verdict -- so a fan that does
	// nothing leaves this stage exactly as it was.
	cases = fanAuthoring(job, cfg, cases, deps, log, w)

	// Then the suite, once, as a process Orion owns (OR-306).
	//
	// OUTSIDE the fan on purpose. These are two independent changes, and
	// running the suite only when the fan ran would leave every small ticket
	// -- the common case -- verified the old way, with the agent deciding
	// what to run and whether to run it. The whole point is that the verdict
	// comes from an exit code.
	//
	// It is also the right ORDER: nothing compiles until every author has
	// stopped writing, which is what keeps ADR 0016's "builds are not
	// isolated" hazard out of reach for a fanned stage.
	runAuthoredSuite(job, cfg, log, w)

	tools := qaTools(cfg, opts.Home)
	// Which path it took, said out loud. A stage that silently degraded to
	// half its coverage reads exactly like one that did not, and the
	// difference is the whole reason the fallback is allowed to exist.
	ui.Say(w, key, events.ActorQA, ui.VerbWorking, "verifying with %s", tools.Path())
	log.Emit(events.Event{Kind: events.KindRunStart, Actor: events.ActorQA,
		Model: actors.Model(events.ActorQA),
		Msg:   "deriving test cases from the ticket, using " + tools.Path()})

	res, err := deps.Supervise(job.WS, supervisor.Options{
		Stage:  "qa",
		Prompt: supervisor.QAPrompt(key, job.Summary, job.Description, cases, tools),
		Model:  actors.Model(events.ActorQA),
		Effort: actors.Effort(events.ActorQA),
		// The implementer's allowance, not a smaller one. QA reads the
		// ticket, reads the diff, writes tests and runs a suite; a suite that
		// takes ten minutes to run takes ten minutes to run for either actor.
		MaxMinutes: job.MaxMinutes, MaxTurns: job.MaxTurns,
		OnActivity: ActivityLogger(log, w, key, events.ActorQA),
		Actor:      events.ActorQA, Key: key,
	})
	if !qaRan(res, err, key, log, w) {
		return qaOutcome{}
	}

	out = qaOutcome{Ran: true}
	findings, clean, ok, qaSession := qaReadVerdict(job, cfg.QA, "", res, deps, log, w)
	if !ok {
		qaNoVerdict(job, deps, log, w)
		return out
	}

	for max := cfg.QA.Rounds(); !clean; {
		out.Findings = findings
		// The full text, not firstLine(findings): the event log has no width
		// limit and is what `orion logs` reads, so this is the one durable
		// record of what QA actually found on the common path where the fix
		// lands in round one and QA never escalates (OR-167).
		log.Emit(events.Event{Kind: events.KindQA, Actor: events.ActorQA,
			Model: actors.Model(events.ActorQA), Msg: findings})
		line := firstSubstantiveLine(findings)
		if line == strings.TrimSpace(findings) {
			ui.Say(w, key, events.ActorQA, ui.VerbWarn, "findings: %s", line)
		} else {
			ui.Say(w, key, events.ActorQA, ui.VerbWarn,
				"findings: %s (full text in the event log)", line)
		}

		if out.Rounds >= max {
			// The ceiling. Two agents can disagree about a test for as long
			// as somebody keeps paying them, and the thing that ends it has
			// to be a count rather than either of their judgements.
			qaEscalate(job, out, cfg, deps, log, w)
			return out
		}
		out.Rounds++
		// The round this exchange is about, and what QA found to open it.
		// Both are read again after the loop has moved on to the next
		// round's findings, so they are taken now (OR-200).
		report := qaRoundReport{Key: key, Round: out.Rounds,
			Findings: findings, FixActor: job.Actor}

		// QA found something and hands the run back to whoever wrote the
		// branch. Both sides named: two adjacent lines with different names
		// leave the reader to work out that the developer is now holding it
		// again, which is exactly what was wrong before (OR-189).
		handoff(w, log, deps, opts, ui.Handoff{Key: key, From: "qa",
			To: fmt.Sprintf("fix round %d", out.Rounds),
			By: events.ActorQA, Next: job.Actor,
			Detail: fmt.Sprintf("round %d of %d", out.Rounds, max)})
		fix, fixErr := deps.Supervise(job.WS, supervisor.Options{
			Stage: "ticket", Resume: job.ImplSession,
			Prompt: supervisor.QAFindingsMessage(findings),
			// The actor that opened this branch, on its own model and effort.
			// Fixing what QA found is implementation work, so it must not
			// silently run on whatever the operator's CLI defaults to
			// (OR-133), and it must not silently switch actors either
			// (OR-171): a docs ticket's fixes belong to the docs actor, not
			// the backend developer.
			Model:      actors.Model(job.Actor),
			Effort:     actors.Effort(job.Actor),
			MaxMinutes: job.MaxMinutes, MaxTurns: job.MaxTurns,
			OnActivity: ActivityLogger(log, w, key, job.Actor),
			Actor:      job.Actor, Key: key,
		})
		if fixErr != nil || fix == nil || fix.ExitCode != 0 {
			report.Verdict = "The fix run did not finish, so this was never re-verified."
			qaPostRound(deps, report)
			qaGiveUp(key, "the developer's fix run did not finish", log, w)
			return out
		}
		if fix.SessionID != "" {
			job.ImplSession = fix.SessionID
		}
		// The resolution, in the implementer's own words -- one line of it.
		// The whole exchange is in the run log; what a reviewer wants on the
		// ticket is what changed (OR-200).
		report.Fix = firstSubstantiveLine(tailOf(fix))

		// And back again. The return leg is a boundary too, and it has to be
		// marked or the log shows a run entering a fix round and never
		// leaving it -- which breaks the duration the event log exists to
		// make queryable as surely as an unmarked departure does.
		handoff(w, log, deps, opts, ui.Handoff{Key: key,
			From: fmt.Sprintf("fix round %d", out.Rounds), To: "qa",
			By: job.Actor, Next: events.ActorQA, Detail: "re-verifying"})
		res, err = deps.Supervise(job.WS, supervisor.Options{
			Stage: "qa", Resume: qaSession,
			Prompt:     supervisor.QAReverifyMessage(),
			Model:      actors.Model(events.ActorQA),
			Effort:     actors.Effort(events.ActorQA),
			MaxMinutes: job.MaxMinutes, MaxTurns: job.MaxTurns,
			OnActivity: ActivityLogger(log, w, key, events.ActorQA),
			Actor:      events.ActorQA, Key: key,
		})
		if !qaRan(res, err, key, log, w) {
			report.Verdict = "The re-verification run did not finish, so whether the fix cleared " +
				"this is unknown."
			qaPostRound(deps, report)
			return out
		}
		findings, clean, ok, qaSession = qaReadVerdict(job, cfg.QA, qaSession, res, deps, log, w)
		if !ok {
			report.Verdict = "Re-verification ended without a verdict, so whether the fix " +
				"cleared this is unknown."
			qaPostRound(deps, report)
			qaNoVerdict(job, deps, log, w)
			return out
		}
		report.Verdict = "Re-verified: findings are still open."
		if clean {
			report.Verdict = "Re-verified: every case passes."
		}
		qaPostRound(deps, report)
	}

	if out.Rounds == 0 {
		// QA ran and found nothing. Said out loud on the ticket, because a
		// ticket with no QA comment cannot be told apart from one QA never
		// verified -- and after OR-156 that difference is the whole point.
		qaPostNothingFound(deps, key)
	}
	out.Clean, out.Findings = true, ""
	log.Emit(events.Event{Kind: events.KindQA, Actor: events.ActorQA,
		Model: actors.Model(events.ActorQA),
		Msg:   fmt.Sprintf("verified the change; every case passes (%d fix round(s))", out.Rounds)})
	ui.Say(w, key, events.ActorQA, ui.VerbOK, "every case passes")
	return out
}

// Bounds for the case-derive subagent, deliberately far tighter than the QA
// run's own: the criteria and the diff are already in its prompt, so this is
// a read-and-list with nothing to search for, and a run that gets stuck
// hunting has stopped being cheap. Same reasoning as the log-triage bounds in
// cmd/orion/fix.go.
const (
	caseDeriveMaxMinutes = 5
	caseDeriveMaxTurns   = 10
)

// caseDeriveOptions is what the case-derive subagent runs with, separated
// from deriveCases so the actor, model and prompt it is configured with can
// be asserted without spawning anything -- the same reason triageOptions is
// split from triageLog.
func caseDeriveOptions(job qaJob, diff string) supervisor.Options {
	return supervisor.Options{
		Stage:      "qa-cases",
		Prompt:     supervisor.QACasesPrompt(job.Key, job.Summary, job.Description, diff),
		MaxMinutes: caseDeriveMaxMinutes,
		MaxTurns:   caseDeriveMaxTurns,
		// Its own actor on its own pinned model, not QA's: this is reading a
		// specification and listing what follows from it, not the judgement of
		// writing the tests, and pinning it is what makes the split a saving
		// rather than a second run at QA's price. Attributed to the same ticket
		// so its spend is its own row in that ticket's cost report instead of
		// hiding inside QA's total (OR-182, following OR-143).
		Actor: events.ActorCaseDerive, Key: job.Key,
		Model:  actors.Model(events.ActorCaseDerive),
		Effort: actors.Effort(events.ActorCaseDerive),
	}
}

// deriveCases reduces the ticket's acceptance criteria and the branch's diff
// to the list of cases QA has to cover, through a subagent that reads both in
// its own context and returns only the list.
//
// Returns "" on any failure, which is today's behaviour: QA derives the cases
// inside its own run from the description it is given. QA must still run --
// a triage step that silently produced nothing must never be the reason a
// ticket has no tests.
//
// The list is written to the event log for the reason OR-129 made the fix
// loop record its closing summary: what a subagent returns is all the parent
// ever sees of it, so an answer that is not written down is gone the moment
// this function returns.
func deriveCases(job qaJob, deps Deps, log *events.Log, w io.Writer) string {
	if strings.TrimSpace(job.Description) == "" || job.BaseSHA == "" {
		return ""
	}
	// The diff of the whole slice, base to HEAD. A failure here is not worth
	// a warning: it means the subagent has nothing to read the change out of,
	// so the stage takes the path it took before this step existed.
	diff, err := runGit(job.WS.RepoDir(), "diff", job.BaseSHA, "HEAD")
	if err != nil || strings.TrimSpace(diff) == "" {
		return ""
	}

	res, sErr := deps.Supervise(job.WS, caseDeriveOptions(job, diff))
	if sErr != nil || res == nil || res.ExitCode != 0 || strings.TrimSpace(res.Final) == "" {
		ui.Say(w, job.Key, events.ActorCaseDerive, ui.VerbWarn,
			"could not derive the cases, so QA reads the ticket itself")
		log.Emitf(events.KindNote, events.ActorCaseDerive,
			"case derivation produced nothing; QA derives its own cases from the ticket")
		return ""
	}

	cases := strings.TrimSpace(res.Final)
	log.Emit(events.Event{Kind: events.KindQA, Actor: events.ActorCaseDerive,
		Model: actors.Model(events.ActorCaseDerive),
		Msg:   "derived the cases to cover:\n" + cases})
	ui.Say(w, job.Key, events.ActorCaseDerive, ui.VerbOK,
		"derived %d case(s) from the acceptance criteria and the diff", countCases(cases))
	return cases
}

// countCases is how many lines of the list actually name a case, for the one
// line the console gets. A blank line is not a case, and neither is a header
// the agent wrote above its list despite being asked for none.
func countCases(cases string) int {
	n := 0
	for _, line := range strings.Split(cases, "\n") {
		t := strings.TrimSpace(line)
		if t != "" && !strings.HasSuffix(t, ":") {
			n++
		}
	}
	return n
}

// qaRan reports whether a QA run produced a usable result, warning when it
// did not. A QA run that failed is not a failed ticket: see the file comment.
func qaRan(res *supervisor.Result, err error, key string, log *events.Log, w io.Writer) bool {
	if err == nil && res != nil && res.ExitCode == 0 {
		return true
	}
	why := "the QA run did not finish"
	if err != nil {
		why += ": " + err.Error()
	} else if res != nil {
		why += fmt.Sprintf(": exit %d, %s", res.ExitCode, res.Reason)
	}
	qaGiveUp(key, why, log, w)
	return false
}

func qaGiveUp(key, why string, log *events.Log, w io.Writer) {
	log.Emitf(events.KindQA, events.ActorOrion, "QA stopped: %s", why)
	ui.Say(w, key, events.ActorOrion, ui.VerbWarn,
		"%s, so this change goes to review unverified by QA", why)
}

// commitQAWork commits whatever the QA stage left in the worktree (OR-234).
//
// QAPrompt already asks for this commit, and QA does not reliably make it: on
// OR-217 it left 163 lines across two test files STAGED, on OR-213 110 lines
// across three UNSTAGED, fifteen minutes apart. Asking again is not a fix --
// Orion owns the artifact, so Orion makes the commit.
//
// All three consequences of not doing it are silent. The red-before-green
// check reads commits, so an uncommitted test reports "QA did not add or
// change a test file" -- a false negative on precisely the run where a test
// WAS written. collect's rebaseOnto refuses a worktree holding uncommitted
// tracked changes, so QA's own output disables the automation meant to keep
// the branch current. And the branch is pushed from what is committed: on
// OR-217 CI went green on a pull request that did not contain the failing test
// sitting on disk beside it, which is the worst outcome available.
//
// Unconditional, on every exit from the stage. A run that ended on the round
// ceiling, on a failing test, or with no verdict at all still leaves its tests
// on the branch: a red pull request is the correct outcome for a change QA
// found a defect in, and it must not be avoided by leaving the evidence
// behind.
//
// Everything the worktree holds, not only the test files, for the reason
// settleTripResidue commits everything: the tree has to end CLEAN or the next
// rebase still refuses, and OR-233 is meant to be a backstop rather than the
// thing keeping the branch rebasable. The same two exclusions, so the
// breaker's stop-note and the hooks' state stay out of the branch's history.
//
// A failure here is a warning, never a failed run -- see the file comment.
func commitQAWork(job qaJob, cfg config.Config, log *events.Log, w io.Writer) {
	n, err := workspace.CommitAll(job.WS.RepoDir(), msgQATests(job.Key),
		filepath.Join(cfg.Paths.Plans, "BLOCKED.md"), cfg.Paths.State)
	switch {
	case err != nil:
		log.Emitf(events.KindNote, events.ActorOrion,
			"could not commit what the QA stage left uncommitted: %v", err)
		ui.Say(w, job.Key, events.ActorOrion, ui.VerbWarn,
			"could not commit what QA left in the worktree, so it does not reach "+
				"the pull request: %v", err)
	case n > 0:
		// Said out loud, because the whole failure this replaces was invisible.
		log.Emitf(events.KindQA, events.ActorOrion,
			"committed %d file(s) the QA stage left uncommitted, so they reach the pull request", n)
		ui.Say(w, job.Key, events.ActorOrion, ui.VerbOK,
			"committed %d file(s) QA left uncommitted", n)
	}
}

// msgQATests is the commit message for QA's own work. It says why the commit
// exists rather than what is in it, because `git log` on a branch with a
// verification commit on it raises exactly one question: who wrote this, and
// was it reviewed like the rest.
func msgQATests(key string) string {
	return fmt.Sprintf("test(%s): the tests QA wrote for this change\n\n"+
		"QA writes its tests after the implementer has already committed, and this\n"+
		"is what it left in the worktree when its stage ended. Committed by Orion\n"+
		"so the evidence travels with the change it is about: everything after the\n"+
		"QA stage reads commits, so a test left on disk is one the red-before-green\n"+
		"check cannot see, the pull request does not carry, and CI can go green\n"+
		"without.\n\n"+
		"These tests may be FAILING. That is the correct state for a change QA\n"+
		"found a defect in -- read the findings on the ticket rather than assuming\n"+
		"a red branch is a broken one.\n", key)
}

// reportRedBeforeGreen says which of QA's own tests were proven to fail
// against the pre-change commit and which were not (OR-156) -- on the
// console and in the event log, in the style of OR-129: what actually
// happened, not that a check merely ran.
//
// Reporting only. A test that could not be shown red is not dropped and does
// not stop the branch going to review either: QA does not block on its own
// authority (see the file comment above), and this check runs with the same
// authority QA does.
func reportRedBeforeGreen(job qaJob, preQA string, log *events.Log, w io.Writer) {
	key := job.Key
	res := checkRedBeforeGreen(job.WS.RepoDir(), job.BaseSHA, preQA)

	if res.Skipped != "" {
		log.Emitf(events.KindNote, events.ActorQA, "red-before-green not checked: %s", res.Skipped)
		return
	}
	if len(res.Proven) > 0 {
		msg := fmt.Sprintf("proved red before green on %d test file(s): %s",
			len(res.Proven), strings.Join(res.Proven, ", "))
		log.Emit(events.Event{Kind: events.KindQA, Actor: events.ActorQA, Msg: msg})
		ui.Say(w, key, events.ActorQA, ui.VerbOK, "%s", msg)
	}
	if len(res.Unproven) > 0 {
		msg := fmt.Sprintf("%d test file(s) already passed against the code before this change, "+
			"so they prove nothing about it: %s", len(res.Unproven), strings.Join(res.Unproven, ", "))
		log.Emit(events.Event{Kind: events.KindQA, Actor: events.ActorQA, Msg: msg})
		ui.Say(w, key, events.ActorQA, ui.VerbWarn, "%s", msg)
	}
	for _, u := range res.Unclear {
		msg := fmt.Sprintf("could not tell whether %s was red before this change: %s", u.File, u.Reason)
		log.Emit(events.Event{Kind: events.KindNote, Actor: events.ActorQA, Msg: msg})
		ui.Say(w, key, events.ActorQA, ui.VerbWarn, "%s", msg)
	}
}

// qaVerdictKind is what a QA run's closing message actually said.
//
// Three states, not two. The sentinel means clean; a message that names a
// failure is findings; a message that does neither is not a verdict at all,
// and that is a different thing from having found defects.
//
// Collapsing the third into the second is what OR-204 was: a run that
// verified everything and wrote "All pass" as prose was routed into an opus
// fix round, and the message it carried told the implementer to fix a defect
// nobody had described. The cheapest way to satisfy that instruction is to
// relax whatever assertion looks closest, and a weakened assertion makes CI
// GREENER rather than redder -- so nothing downstream catches it, and it has
// to not happen here.
type qaVerdictKind int

const (
	// qaVerdictNone: neither the sentinel nor a named failure. Unknown, not
	// failing. Never dispatched as findings; re-asked once, then a person.
	qaVerdictNone qaVerdictKind = iota
	qaVerdictClean
	qaVerdictFindings
)

// qaVerdict reads the QA agent's closing message.
//
// Clean only on the sentinel, and only when it starts a line: an agent that
// quotes its own instructions back ("write QA CLEAN when everything passes")
// would otherwise declare a clean branch by describing one.
func qaVerdict(final string) (said string, kind qaVerdictKind) {
	if hasSentinel(final, supervisor.QAClean) {
		return "", qaVerdictClean
	}
	said = strings.TrimSpace(final)
	if said == "" {
		// A run that said nothing has not said everything passes -- and has
		// not described a defect either, so it is not something to hand a
		// developer to fix.
		return "QA finished without reporting anything, so nothing was verified.", qaVerdictNone
	}
	if !namesAFailure(said) {
		return said, qaVerdictNone
	}
	return said, qaVerdictFindings
}

// hasSentinel reports whether a closing message contains a sentinel at the
// start of a line, ignoring the decoration a model puts in front of one.
//
// AT THE START OF A LINE is what makes a sentinel a sentinel: an agent that
// quotes its own instructions back ("write QA CLEAN when everything passes")
// would otherwise declare a clean branch by describing one. Shared with the
// database stage (OR-135), which reports through sentinels of its own -- the
// rule is the same rule, and a second copy of it would be a second place for
// the quoting hazard to be forgotten.
func hasSentinel(final, sentinel string) bool {
	for _, line := range strings.Split(final, "\n") {
		line = strings.TrimLeft(strings.TrimSpace(line), "*#->_ ")
		if len(line) < len(sentinel) {
			continue
		}
		if strings.EqualFold(line[:len(sentinel)], sentinel) {
			return true
		}
	}
	return false
}

// failureStems are what a findings report says and a passing one does not,
// as word prefixes: "fail" covers fails, failed, failing and failure.
//
// This is prose, and reading prose is exactly what the sentinel exists to
// avoid -- so note what this list is allowed to decide. It cannot make a
// branch clean: only the sentinel does that, unchanged. It cannot turn a
// report it does not recognise into a pass either -- that becomes NO verdict,
// which costs one cheap re-ask and damages nothing. The only thing it decides
// is whether Orion may skip asking, and it skips only when QA has named a
// failure in so many words. Every way it can be wrong lands on the re-ask,
// which is the safe side.
var failureStems = []string{
	"fail", "defect", "broke", "regress", "incorrect", "wrong", "missing",
	"mismatch", "crash", "panic", "unable", "problem", "unexpected",
}

// negators are the words that deny the one after them. "No failures" and "all
// cases pass" are the same claim, and reading the first as findings is
// exactly the misread this file is about -- a QA run that reports its pass in
// the negative must not be routed into a fix round for saying "failure".
var negators = map[string]bool{
	"no": true, "not": true, "never": true, "nothing": true, "none": true,
	"neither": true, "without": true, "zero": true, "any": true,
}

// namesAFailure reports whether the message says something failed.
//
// Word by word rather than by substring, so "debug" is not a bug and
// "passfail" is neither -- and so the word before a match can be read, which
// is what makes the negation check possible at all.
func namesAFailure(s string) bool {
	prev := ""
	for _, word := range strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsNumber(r)
	}) {
		if !negators[prev] {
			for _, stem := range failureStems {
				if strings.HasPrefix(word, stem) {
					return true
				}
			}
		}
		prev = word
	}
	return false
}

// Bounds for the verdict re-ask. One line is being asked for, from a session
// that has already done the work, so a run that spends more than this has
// stopped answering the question. The short cap is also what makes the claim
// true that this is cheaper than the fix round it replaces.
const (
	qaVerdictMaxMinutes = 5
	qaVerdictMaxTurns   = 5
)

// qaReadVerdict reads a QA run's closing message and, when that message gave
// no verdict at all, asks once more for one.
//
// One re-ask, never a fix round: see qaVerdictKind. It runs on QA's own model
// against QA's own session, so it costs one short turn rather than an
// implementer round, and it cannot damage a clean branch -- the worst it can
// do is ask a question.
//
// ok is false when QA still did not answer, or when the re-ask itself did not
// finish. The caller escalates to a person; it must not guess. next is QA's
// session to carry forward, which the re-ask may replace.
func qaReadVerdict(job qaJob, qa config.QA, session string, res *supervisor.Result,
	deps Deps, log *events.Log, w io.Writer) (findings string, clean, ok bool, next string) {

	next = session
	if res.SessionID != "" {
		next = res.SessionID
	}
	said, kind := qaVerdict(tailOf(res))
	if kind != qaVerdictNone {
		return said, kind == qaVerdictClean, true, next
	}

	// What it said instead, written down before it is asked again: the reply
	// to the re-ask replaces it in memory, and this is the only record of the
	// message that was ambiguous in the first place.
	log.Emit(events.Event{Kind: events.KindQA, Actor: events.ActorQA,
		Model: actors.Model(events.ActorQA),
		Msg: "QA ended without " + supervisor.QAClean + " and without findings, so there is " +
			"no verdict to act on. Asking once for one. What it said instead:\n" + said})
	ui.Say(w, job.Key, events.ActorQA, ui.VerbWarn,
		"ended without a verdict; asking once for one rather than dispatching a fix round")

	// Scaled to the run being resumed (OR-252). A re-ask against a session
	// that worked for thirty minutes is not the one-line job the flat cap
	// assumed, and on OR-248 the flat cap killed it: the change reached a
	// pull request with no QA opinion at all.
	budget := qa.VerdictBudget(parentMinutes(res))

	again, err := deps.Supervise(job.WS, supervisor.Options{
		Stage: "qa", Resume: next,
		Prompt:     supervisor.QAVerdictMessage(),
		Model:      actors.Model(events.ActorQA),
		Effort:     actors.Effort(events.ActorQA),
		MaxMinutes: budget, MaxTurns: qaVerdictMaxTurns,
		OnActivity: ActivityLogger(log, w, job.Key, events.ActorQA),
		Actor:      events.ActorQA, Key: job.Key,
	})
	if !qaRan(again, err, job.Key, log, w) {
		// A KILLED RE-ASK IS NOT QA DECLINING TO ANSWER, and the two need
		// different words because they need different fixes. "Gave no
		// verdict, even when asked for one" reads as QA refusing; the truth
		// here is that it never got the chance, and the lever is the budget.
		if killedByClock(again, budget) {
			ui.Say(w, job.Key, events.ActorQA, ui.VerbWarn,
				"the re-ask ran out of time (%d minute budget for a %d minute run); "+
					"raise it with `orion config limits qa.verdict_minutes N`",
				budget, parentMinutes(res))
			log.Emitf(events.KindQA, events.ActorQA,
				"the verdict re-ask was killed at its %d minute budget; "+
					"QA did not decline to answer, it was not given time to", budget)
		}
		return "", false, false, next
	}
	if again.SessionID != "" {
		next = again.SessionID
	}
	if said, kind = qaVerdict(tailOf(again)); kind == qaVerdictNone {
		return "", false, false, next
	}
	return said, kind == qaVerdictClean, true, next
}

// firstSubstantiveLine is what the console shows: the first line that
// actually says something, not just the first line. QA's closing message
// often opens with a header ("Verification done. Summary:") before the
// content -- returning the literal first line would spend the one line the
// console has on a sentence that carries no information (OR-167).
func firstSubstantiveLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		t := strings.TrimSpace(line)
		if t == "" || strings.HasSuffix(t, ":") {
			continue
		}
		return t
	}
	return strings.TrimSpace(s)
}

// qaRoundReport is one fix round as the ticket records it: what QA found,
// what the implementer changed in response, and what re-verification made of
// it. Assembled across the round rather than at one point, because the three
// facts become known at three different moments.
type qaRoundReport struct {
	Key      string
	Round    int
	Findings string
	// FixActor is whoever route() picked for this ticket, named in the body:
	// the comment is attributed to QA, so without this a reader cannot tell
	// who made the change QA is re-verifying (OR-171).
	FixActor string
	// Fix is one line of what changed. One line on purpose -- a reviewer
	// wants the resolution, not the exchange that produced it. Empty when
	// the fix run never finished.
	Fix     string
	Verdict string
}

// qaPostRound puts one round on the ticket. Every round, not only the one
// that hits the ceiling -- by the time someone reads the ticket weeks later,
// the in-memory findings and the raw stage log are both gone, and the ticket
// is the only place left to look (OR-167).
//
// Text only, and deliberately: the run log this is drawn from is hundreds of
// kilobytes, cannot be grepped once uploaded, and contains everything the
// agent read and wrote -- including, on OR-105, a live-format access key that
// push protection caught and an attachment would have published past it
// (OR-200). Comments cost kilobytes; attachments cost the quota and the
// secret both.
func qaPostRound(deps Deps, r qaRoundReport) {
	if deps.Jira == nil {
		return
	}
	fix := strings.TrimSpace(r.Fix)
	if fix == "" {
		fix = "nothing was recorded."
	}
	body := fmt.Sprintf("QA round %d found:\n\n%s\n\n%s changed: %s\n\n%s",
		r.Round, strings.TrimSpace(r.Findings), actors.Attribution(r.FixActor), fix, r.Verdict)
	_ = deps.Jira.Comment(r.Key, actors.Comment(events.ActorQA, body))
}

// qaPostNothingFound is the one line a clean run leaves. Without it, silence
// on the ticket means either "QA verified this and found nothing" or "QA
// never ran", and those are not the same claim about a change (OR-156).
func qaPostNothingFound(deps Deps, key string) {
	if deps.Jira == nil {
		return
	}
	_ = deps.Jira.Comment(key, actors.Comment(events.ActorQA,
		"verified this change independently and found nothing: every case passes, "+
			"and no fix rounds were needed."))
}

// qaNoVerdict ends the stage when QA never gave one -- not the sentinel, not
// findings, and not on the second ask either.
//
// A person, never a fix round. This is the whole point of OR-204: an unknown
// verdict dispatched as findings tells the implementer to fix a defect that
// was never described, and the cheapest way to satisfy that instruction is to
// relax whatever assertion looks closest. That makes CI greener rather than
// redder, so the damage ships green and no later gate sees it. What the run
// says instead is the truth -- no verdict was obtained -- which is a claim a
// person can act on and "fixing what QA found" was not.
//
// The run CONTINUES, for the reason qaEscalate's does: QA does not block on
// its own authority, and "QA could not be understood" is weaker ground to
// strand a committed, CI-gated change on than findings would have been.
func qaNoVerdict(job qaJob, deps Deps, log *events.Log, w io.Writer) {
	key := job.Key
	const why = "QA never reported a verdict: its closing message named neither " +
		supervisor.QAClean + " nor any finding, and it did not write one when asked again"

	log.Emit(events.Event{Kind: events.KindEscalate, Actor: events.ActorQA,
		Model: actors.Model(events.ActorQA),
		Msg:   why + "; no fix round was dispatched, because nothing was described to fix"})
	ui.Say(w, key, events.ActorQA, ui.VerbFail,
		"gave no verdict, even when asked for one. This change is unverified and a "+
			"person needs to look.")

	if deps.Jira != nil {
		_ = deps.Jira.Comment(key, actors.Comment(events.ActorQA, why+
			".\n\nSo this change is UNVERIFIED, which is not the same as failing -- and is why "+
			"no fix round ran: there was nothing described to fix. The branch is going to "+
			"review anyway -- QA reports, it does not block -- so read it as a change QA did "+
			"not get through, not as one it passed."))
	}

	tell(w, log, job.WS, notify.Event{
		Key: key, Level: notify.Blocked, Workspace: job.WS.ID, Actor: events.ActorQA,
		Title: key + ": QA gave no verdict",
		Body: strings.Join([]string{
			"*" + job.Summary + "*",
			"",
			"QA ended without " + supervisor.QAClean + " and without findings, and did not",
			"write a verdict when it was asked for one. Nothing was verified, and no fix",
			"round ran -- there was no defect described to fix.",
			"",
			"_The pull request still opens: QA reports findings, it does not block a change._",
			"_Read this as unverified, not as passed._",
		}, "\n"),
	})
}

// qaEscalate hands the open findings to a person.
//
// The run CONTINUES afterwards -- the branch is pushed and the pull request
// opens with the findings on the ticket. That follows from QA not blocking on
// its own authority: stopping here would strand a committed, CI-gated change
// on an unreviewed branch on the say-so of the actor that was told it does
// not get that call. What a person gets instead is the findings, before the
// review, with the pull request in front of them.
func qaEscalate(job qaJob, out qaOutcome, cfg config.Config, deps Deps,
	log *events.Log, w io.Writer) {

	key := job.Key
	log.Emit(events.Event{Kind: events.KindEscalate, Actor: events.ActorQA,
		Model: actors.Model(events.ActorQA),
		Msg: fmt.Sprintf("%d fix round(s) did not clear these findings; escalating to a person",
			out.Rounds)})
	ui.Say(w, key, events.ActorQA, ui.VerbFail,
		"%d fix round(s) did not clear these findings. A person needs to look.", out.Rounds)

	body := fmt.Sprintf("verified this change independently and these findings are still open "+
		"after %d fix round(s):\n\n%s\n\nThe branch is going to review anyway -- QA reports, it "+
		"does not block -- so these are for whoever reviews it. If a finding is wrong, the test "+
		"that produced it is on the branch and can be read.", out.Rounds, out.Findings)
	if deps.Jira != nil {
		_ = deps.Jira.Comment(key, actors.Comment(events.ActorQA, body))
	}

	title := fmt.Sprintf("%s: QA findings are still open", key)
	msg := strings.Join([]string{
		"*" + job.Summary + "*",
		"",
		"QA wrote tests from the ticket's acceptance criteria and these are still failing",
		fmt.Sprintf("after %d fix round(s):", out.Rounds),
		"",
		quote(out.Findings),
		"",
		"_The pull request still opens: QA reports findings, it does not block a change._",
		"_Read these before you approve it._",
	}, "\n")
	tell(w, log, job.WS, notify.Event{
		Key: key, Level: notify.Blocked, Workspace: job.WS.ID, Actor: events.ActorQA,
		Title: title, Body: msg,
	})
}

// qaTools discovers what this machine can actually offer, at claim time.
//
// Detected, never required. nj-agents' testing class makes the stage better
// and its absence makes it degrade, so a check that refused to run without it
// would trade a partial verification for none at all.
func qaTools(cfg config.Config, home string) supervisor.QATools {
	t := supervisor.QATools{E2EBaseURL: strings.TrimSpace(cfg.QA.E2EBaseURL)}
	if !cfg.Delegation.Enabled {
		return t
	}
	inst := njagents.Discover(cfg.Delegation.NJAgentsDir, home)
	t.Skills = true
	for _, s := range njagents.TestingSkills {
		if !njagents.HasSkill(inst, s) {
			t.Skills = false
			break
		}
	}
	return t
}

// parentMinutes is how long the run being resumed actually took, rounded up.
//
// ROUNDED UP, and never zero for a run that did anything: a re-ask against a
// run of forty seconds should not compute a budget of zero minutes and then
// fall back to the floor for the wrong reason. The floor is a decision, not
// an accident of integer division.
func parentMinutes(res *supervisor.Result) int {
	if res == nil || res.Duration <= 0 {
		return 0
	}
	m := int(res.Duration.Round(time.Minute) / time.Minute)
	if m < 1 {
		return 1
	}
	return m
}

// killedByClock reports whether a run ended because it ran out of wall clock,
// rather than for any other reason.
//
// The distinction the operator needs: a re-ask that was KILLED tells them to
// raise a budget, and one that finished without answering tells them QA has
// nothing to say. Reporting the first as the second sends them to read a
// diff when the fix was a number.
func killedByClock(res *supervisor.Result, budget int) bool {
	if res == nil || !res.Killed {
		return false
	}
	return strings.Contains(res.Reason, "wall clock") ||
		strings.Contains(res.Reason, fmt.Sprintf("%d minute", budget))
}
