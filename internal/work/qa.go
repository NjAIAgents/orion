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
	"strings"

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
		// Only when QA actually ran: a stage that never started wrote no
		// tests, and there is nothing to prove red about a run that failed
		// before its first turn (qaRan below handles that case).
		if out.Ran {
			reportRedBeforeGreen(job, preQA, log, w)
		}
	}()

	cases := deriveCases(job, deps, log, w)

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
	findings, clean := qaVerdict(tailOf(res))
	qaSession := res.SessionID

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
		ui.Stage(w, log, ui.Handoff{Key: key, From: "qa",
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
		ui.Stage(w, log, ui.Handoff{Key: key,
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
		if res.SessionID != "" {
			qaSession = res.SessionID
		}
		findings, clean = qaVerdict(tailOf(res))
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

// qaVerdict reads the QA agent's closing message.
//
// Clean only on the sentinel, and only when it starts a line: an agent that
// quotes its own instructions back ("write QA CLEAN when everything passes")
// would otherwise declare a clean branch by describing one. Anything else is
// findings -- including an empty message, because a QA run that says nothing
// has not said everything passes.
func qaVerdict(final string) (findings string, clean bool) {
	for _, line := range strings.Split(final, "\n") {
		line = strings.TrimLeft(strings.TrimSpace(line), "*#->_ ")
		if len(line) < len(supervisor.QAClean) {
			continue
		}
		if strings.EqualFold(line[:len(supervisor.QAClean)], supervisor.QAClean) {
			return "", true
		}
	}
	if strings.TrimSpace(final) == "" {
		return "QA finished without reporting anything, so nothing was verified.", false
	}
	return strings.TrimSpace(final), false
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
