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
		Prompt: supervisor.QAPrompt(key, job.Summary, job.Description, tools),
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
		qaPostFindings(deps, key, out.Rounds+1, findings)

		if out.Rounds >= max {
			// The ceiling. Two agents can disagree about a test for as long
			// as somebody keeps paying them, and the thing that ends it has
			// to be a count rather than either of their judgements.
			qaEscalate(job, out, cfg, deps, log, w)
			return out
		}
		out.Rounds++

		ui.Say(w, key, job.Actor, ui.VerbWorking,
			"fixing what QA found (round %d of %d)", out.Rounds, max)
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
			qaGiveUp(key, "the developer's fix run did not finish", log, w)
			return out
		}
		if fix.SessionID != "" {
			job.ImplSession = fix.SessionID
		}

		ui.Say(w, key, events.ActorQA, ui.VerbWorking, "re-verifying")
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
			return out
		}
		if res.SessionID != "" {
			qaSession = res.SessionID
		}
		findings, clean = qaVerdict(tailOf(res))
	}

	out.Clean, out.Findings = true, ""
	log.Emit(events.Event{Kind: events.KindQA, Actor: events.ActorQA,
		Model: actors.Model(events.ActorQA),
		Msg:   fmt.Sprintf("verified the change; every case passes (%d fix round(s))", out.Rounds)})
	ui.Say(w, key, events.ActorQA, ui.VerbOK, "every case passes")
	return out
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

// qaPostFindings puts one round's findings on the ticket. Every round, not
// only the one that hits the ceiling -- by the time someone reads the ticket
// weeks later, the in-memory findings and the raw stage log are both gone,
// and the ticket is the only place left to look (OR-167).
func qaPostFindings(deps Deps, key string, round int, findings string) {
	if deps.Jira == nil {
		return
	}
	body := fmt.Sprintf("QA round %d found:\n\n%s", round, findings)
	_ = deps.Jira.Comment(key, actors.Comment(events.ActorQA, body))
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
		Level: notify.Blocked, Workspace: job.WS.ID, Actor: events.ActorQA,
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
