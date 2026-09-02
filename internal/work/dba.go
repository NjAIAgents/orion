package work

// The database stage (OR-135).
//
//	implementer commits -> does this change touch data? (free, deterministic)
//	                    -> yes: the database architect reviews the schema,
//	                       the migrations and the indexes
//	                    -> findings? -> implementer fixes -> reviewed again
//	                    -> sound, or the round ceiling and a person is told
//
// It runs after the slice is committed and BEFORE QA, which is the ordering
// that matters. A schema finding forces a change to the data model, and QA's
// tests are written against the schema as it stands when QA runs: reviewing
// the data model after verification would leave a suite that was verified
// against a schema the fix then replaced.
//
// A LOOP, NOT A GATE, exactly as QA is. It reports; Orion routes; the
// implementer fixes. It never blocks on its own authority -- a review actor
// that can stop a run has to be right every time, and it is not -- so the
// worst it can do is spend its rounds and tell a person what is still open.
//
// THE GATE IS FREE. Most tickets touch no data, and those must not pay for
// this stage at all. internal/dba answers that from the diff's paths and the
// ticket's markers with no model call, which is what makes "one more stage"
// affordable: the cost lands only on the tickets the stage is about.
//
// IT NEVER REACHES PRODUCTION. See config.DBA: the target is an explicit
// non-production DSN or there is no target, and with no target the review is
// static and says so. Nothing here infers a database from the environment.
//
// Every failure inside here is a warning, never a failed run, for the reason
// QA's are: the change is committed by this point, and turning "the review
// could not be reached" into a failed ticket would throw away finished work
// over the review of it.

import (
	"fmt"
	"io"
	"strings"

	"github.com/orion-sdlc/orion/internal/actors"
	"github.com/orion-sdlc/orion/internal/config"
	"github.com/orion-sdlc/orion/internal/dba"
	"github.com/orion-sdlc/orion/internal/events"
	"github.com/orion-sdlc/orion/internal/notify"
	"github.com/orion-sdlc/orion/internal/supervisor"
	"github.com/orion-sdlc/orion/internal/ui"
	"github.com/orion-sdlc/orion/internal/workspace"
)

// dbaOutcome is what the stage ended with.
type dbaOutcome struct {
	Ran      bool
	Sound    bool
	Rounds   int
	Findings string
	// ImplSession is the developer's session as this stage leaves it. A schema
	// fix round resumes that session and moves it on, and QA runs next against
	// the same developer: handing QA the session id from before the fix would
	// deliver its findings into a conversation that predates the schema the
	// branch now carries.
	ImplSession string
}

// Verdict is what the boundary out of this stage says: the result, not that it
// ran. Same reason qaOutcome.Verdict exists.
func (o dbaOutcome) Verdict() string {
	switch {
	case o.Sound && o.Rounds > 0:
		return fmt.Sprintf("data model sound after %d fix round(s)", o.Rounds)
	case o.Sound:
		return "data model sound"
	case o.Findings != "":
		return fmt.Sprintf("schema findings still open after %d fix round(s)", o.Rounds)
	}
	return "not reviewed"
}

// dbaJob is one stage run's inputs. A struct rather than nine parameters, for
// the reason qaJob is one.
type dbaJob struct {
	Key         string
	Summary     string
	Description string
	// Fields are the ticket's issue type, components and labels, which is half
	// the detection signal -- see internal/dba.
	Fields []string
	// ImplSession is the session that wrote the change, so a finding is a
	// message in the conversation that produced the schema rather than a fresh
	// run paying for the whole context again.
	ImplSession string
	// Actor is whoever route() picked for this ticket. A fix round resumes THIS
	// actor, for the reason qaJob.Actor exists (OR-171).
	Actor      string
	WS         *workspace.Workspace
	MaxMinutes int
	MaxTurns   int
	// BaseSHA is the commit the branch was cut from. The diff base to HEAD is
	// what the review is scoped to; without it there is no change to review and
	// the stage does not run.
	BaseSHA string
}

// dbaScope decides whether the stage has anything to review, and says why not
// when it does not.
//
// SEPARATE FROM runDBA and called first, because the answer decides which
// stage the run announces it is entering. A boundary that said "implementing
// -> qa" and then ran a database review in between describes a pipeline the
// operator does not have; OR-189 exists because the boundaries have to be the
// truth.
//
// It costs nothing but one `git diff --name-only`: no model call, by design.
// See the file comment and internal/dba.
func dbaScope(job dbaJob, cfg config.Config, deps Deps,
	log *events.Log, w io.Writer) ([]dba.Signal, bool) {

	if !cfg.DBA.On() || deps.Supervise == nil || job.BaseSHA == "" {
		return nil, false
	}
	// The reviewer must not review its own change. When a ticket routed to the
	// database architect in the first place -- `orion routes` sends a
	// `database` label here -- the diff under review is its own, and the
	// finding it would report would be a finding about its own edit. That is
	// the boundary QAPrompt draws for the same reason, applied one level up.
	//
	// A CONSEQUENCE WORTH KNOWING, because it looks like a bug from outside:
	// the routing table and this stage read the same word list, so a ticket
	// whose ONLY data signal is a marker that also routes it here is worked by
	// this actor and then not reviewed by it. The ticket's marker still
	// triggers the stage in the case it is for -- a ticket that routes
	// elsewhere by precedence (a `frontend` ticket also marked `database`) and
	// whose diff happens to touch no schema file. QA still runs on both.
	// Removing the guard would buy a review of the reviewer's own diff, which
	// is the one thing this actor exists not to be.
	if job.Actor == events.ActorDBA {
		ui.Say(w, job.Key, events.ActorOrion, ui.VerbOK,
			"the database architect worked this ticket, so there is no independent review to run")
		return nil, false
	}

	// Paths first, because they are ground truth: what the change actually
	// touched, rather than what somebody expected it to touch when they wrote
	// the ticket.
	paths, err := changedPaths(job.WS.RepoDir(), job.BaseSHA)
	if err != nil {
		log.Emitf(events.KindNote, events.ActorOrion,
			"could not list the changed paths, so the database stage read the ticket alone: %v", err)
	}
	sigs := dba.Signals(paths, job.Fields)
	if len(sigs) == 0 {
		// Said out loud, and as an outcome rather than a miss. A stage that
		// silently did not run is indistinguishable from one that ran and
		// found nothing, and those are different claims about a change --
		// the same reason route()'s default is announced (OR-191).
		log.Emitf(events.KindNote, events.ActorOrion,
			"no database review: %s", dba.Reason(nil))
		ui.Say(w, job.Key, events.ActorOrion, ui.VerbOK,
			"nothing in this change touches the data model, so there is no database review to pay for")
		return nil, false
	}
	return sigs, true
}

// runDBA reviews the data model of a change dbaScope has already decided is
// worth reviewing.
func runDBA(job dbaJob, sigs []dba.Signal, cfg config.Config, opts Options, deps Deps,
	log *events.Log, w io.Writer) dbaOutcome {

	key := job.Key
	diff, err := runGit(job.WS.RepoDir(), "diff", job.BaseSHA, "HEAD")
	if err != nil || strings.TrimSpace(diff) == "" {
		dbaGiveUp(key, "the change could not be read as a diff", log, w)
		return dbaOutcome{}
	}

	target := supervisor.DBATarget{DSN: cfg.DBA.NonProdDSN}
	// The production guard, before a single turn is bought. A DSN that names
	// itself production is refused rather than warned about: this stage exists
	// to be pointed at a database, and warning about the one value it must
	// never be pointed at is a warning nobody reads until afterwards.
	if word, isProd := cfg.DBA.ProductionDSN(); isProd {
		target.DSN = ""
		log.Emitf(events.KindNote, events.ActorOrion,
			"dba.non_prod_dsn contains %q, so it was refused and this review is static; "+
				"point it at a throwaway database or leave it empty", word)
		ui.Say(w, key, events.ActorOrion, ui.VerbWarn,
			"dba.non_prod_dsn looks like production (%q), so nothing was connected to; "+
				"the review is static", word)
	}

	// Which path it took, said before it runs. A static review and a measured
	// one are different claims about a change, and the difference is the whole
	// reason the degraded path is allowed to exist.
	ui.Say(w, key, events.ActorDBA, ui.VerbWorking,
		"reviewing the data model (%s) -- %s", target.Path(), dba.Reason(sigs))
	log.Emit(events.Event{Kind: events.KindRunStart, Actor: events.ActorDBA,
		Model: actors.Model(events.ActorDBA),
		Msg:   "reviewing the data model using " + target.Path() + "; " + dba.Reason(sigs)})

	res, err := deps.Supervise(job.WS, supervisor.Options{
		Stage:  "dba",
		Prompt: supervisor.DBAPrompt(key, job.Summary, job.Description, diff, target),
		Model:  actors.Model(events.ActorDBA),
		Effort: actors.Effort(events.ActorDBA),
		// The implementer's allowance, for QA's reason: reading a schema and
		// asking a database for a query plan takes as long as it takes,
		// whichever actor does it.
		MaxMinutes: job.MaxMinutes, MaxTurns: job.MaxTurns,
		OnActivity: ActivityLogger(log, w, key, events.ActorDBA),
		Actor:      events.ActorDBA, Key: key,
	})
	if !dbaRan(res, err, key, log, w) {
		return dbaOutcome{}
	}

	// ImplSession carried on every path out, including the failures: a fix
	// round that ran and then hit a broken re-review still moved the
	// developer's session on, and QA has to resume where it actually is.
	out := dbaOutcome{Ran: true, ImplSession: job.ImplSession}
	session := res.SessionID
	findings, kind := dbaVerdict(tailOf(res))
	if kind == qaVerdictNone {
		dbaNoVerdict(job, deps, log, w)
		return out
	}

	for max := cfg.DBA.Rounds(); kind != qaVerdictClean; {
		out.Findings = findings
		// The full text into the event log, for OR-167's reason: the terminal
		// has a width limit and the log does not, and on the common path where
		// the fix lands in round one this is the only durable record of what
		// was actually found.
		log.Emit(events.Event{Kind: events.KindQA, Actor: events.ActorDBA,
			Model: actors.Model(events.ActorDBA), Msg: findings})
		ui.Say(w, key, events.ActorDBA, ui.VerbWarn,
			"schema findings: %s", firstSubstantiveLine(findings))

		if out.Rounds >= max {
			dbaEscalate(job, out, deps, log, w)
			return out
		}
		out.Rounds++

		handoff(w, log, deps, opts, ui.Handoff{Key: key, From: "dba",
			To: fmt.Sprintf("schema fix round %d", out.Rounds),
			By: events.ActorDBA, Next: job.Actor,
			Detail: fmt.Sprintf("round %d of %d", out.Rounds, max)})
		fix, fixErr := deps.Supervise(job.WS, supervisor.Options{
			Stage: "ticket", Resume: job.ImplSession,
			Prompt: supervisor.DBAFindingsMessage(findings),
			// The actor that opened this branch, on its own model and effort:
			// fixing a schema finding is implementation work, and it must not
			// silently switch actors (OR-171) or fall to the CLI's own default
			// model (OR-133).
			Model:      actors.Model(job.Actor),
			Effort:     actors.Effort(job.Actor),
			MaxMinutes: job.MaxMinutes, MaxTurns: job.MaxTurns,
			OnActivity: ActivityLogger(log, w, key, job.Actor),
			Actor:      job.Actor, Key: key,
		})
		if fixErr != nil || fix == nil || fix.ExitCode != 0 {
			dbaPostRound(deps, job, out.Rounds, findings,
				"The fix run did not finish, so this was never reviewed again.")
			dbaGiveUp(key, "the developer's fix run did not finish", log, w)
			return out
		}
		if fix.SessionID != "" {
			job.ImplSession, out.ImplSession = fix.SessionID, fix.SessionID
		}

		handoff(w, log, deps, opts, ui.Handoff{Key: key,
			From: fmt.Sprintf("schema fix round %d", out.Rounds), To: "dba",
			By: job.Actor, Next: events.ActorDBA, Detail: "reviewing again"})
		res, err = deps.Supervise(job.WS, supervisor.Options{
			Stage: "dba", Resume: session,
			Prompt:     supervisor.DBAReviewAgainMessage(),
			Model:      actors.Model(events.ActorDBA),
			Effort:     actors.Effort(events.ActorDBA),
			MaxMinutes: job.MaxMinutes, MaxTurns: job.MaxTurns,
			OnActivity: ActivityLogger(log, w, key, events.ActorDBA),
			Actor:      events.ActorDBA, Key: key,
		})
		if !dbaRan(res, err, key, log, w) {
			dbaPostRound(deps, job, out.Rounds, findings,
				"The second review did not finish, so whether the fix cleared this is unknown.")
			return out
		}
		if res.SessionID != "" {
			session = res.SessionID
		}
		findings, kind = dbaVerdict(tailOf(res))
		if kind == qaVerdictNone {
			dbaPostRound(deps, job, out.Rounds, out.Findings,
				"The second review ended without a verdict, so whether the fix cleared this "+
					"is unknown.")
			dbaNoVerdict(job, deps, log, w)
			return out
		}
		verdict := "Reviewed again: the findings are still open."
		if kind == qaVerdictClean {
			verdict = "Reviewed again: the data model is sound."
		}
		dbaPostRound(deps, job, out.Rounds, out.Findings, verdict)
	}

	out.Sound, out.Findings = true, ""
	log.Emit(events.Event{Kind: events.KindQA, Actor: events.ActorDBA,
		Model: actors.Model(events.ActorDBA),
		Msg: fmt.Sprintf("reviewed the data model and found it sound (%d fix round(s)); %s",
			out.Rounds, target.Path())})
	ui.Say(w, key, events.ActorDBA, ui.VerbOK, "the data model in this change is sound")
	if out.Rounds == 0 {
		dbaPostNothingFound(deps, key, target)
	}
	return out
}

// dbaVerdict reads the review's closing message.
//
// TWO SENTINELS, and nothing read from prose. QA can recognise its findings
// from the words a failing test report uses; a schema finding contains none of
// them -- "this is a sequential scan at ten million rows" names no failure --
// so inferring would send every real finding to a person instead of to the
// developer. See supervisor.DBAFindings.
//
// Clean wins a message carrying both, because the sentinel a review ends on is
// the one it means and DBAClean is instructed to be the last line. Neither is
// qaVerdictNone: unknown, never dispatched as findings, for the OR-204 reason
// -- a developer told to fix a defect nobody described relaxes whatever looks
// closest, and here that is a constraint.
func dbaVerdict(final string) (findings string, kind qaVerdictKind) {
	if hasSentinel(final, supervisor.DBAClean) {
		return "", qaVerdictClean
	}
	if !hasSentinel(final, supervisor.DBAFindings) {
		return "", qaVerdictNone
	}
	// Everything from the marker on. The review's own preamble is not a
	// finding, and handing it to a developer as one wastes the round.
	for i, line := range strings.Split(final, "\n") {
		trimmed := strings.TrimLeft(strings.TrimSpace(line), "*#->_ ")
		if len(trimmed) < len(supervisor.DBAFindings) ||
			!strings.EqualFold(trimmed[:len(supervisor.DBAFindings)], supervisor.DBAFindings) {
			continue
		}
		rest := strings.TrimSpace(strings.Join(strings.Split(final, "\n")[i+1:], "\n"))
		if rest == "" {
			// The marker with nothing under it is not a finding. Unknown
			// rather than clean: it said it had something and then did not
			// say what.
			return "", qaVerdictNone
		}
		return rest, qaVerdictFindings
	}
	return "", qaVerdictNone
}

// changedPaths lists what the branch touched, base to HEAD.
func changedPaths(dir, baseSHA string) ([]string, error) {
	out, err := runGit(dir, "diff", "--name-only", baseSHA, "HEAD")
	if err != nil {
		return nil, err
	}
	var paths []string
	for _, line := range strings.Split(out, "\n") {
		if p := strings.TrimSpace(line); p != "" {
			paths = append(paths, p)
		}
	}
	return paths, nil
}

// dbaRan reports whether a review run produced a usable result, warning when
// it did not. A review that failed is not a failed ticket: see the file
// comment.
func dbaRan(res *supervisor.Result, err error, key string, log *events.Log, w io.Writer) bool {
	if err == nil && res != nil && res.ExitCode == 0 {
		return true
	}
	why := "the database review did not finish"
	if err != nil {
		why += ": " + err.Error()
	} else if res != nil {
		why += fmt.Sprintf(": exit %d, %s", res.ExitCode, res.Reason)
	}
	dbaGiveUp(key, why, log, w)
	return false
}

func dbaGiveUp(key, why string, log *events.Log, w io.Writer) {
	log.Emitf(events.KindQA, events.ActorOrion, "the database stage stopped: %s", why)
	ui.Say(w, key, events.ActorOrion, ui.VerbWarn,
		"%s, so this change goes to review with its data model unexamined", why)
}

// dbaPostRound puts one round on the ticket, every round, for OR-167's reason:
// weeks later the in-memory findings and the raw stage log are both gone and
// the ticket is the only place left to look.
//
// Text only, deliberately: see qaPostRound. A raw log is hundreds of kilobytes
// and has published a live access key once already (OR-105, OR-200) -- and a
// database review's log is the one most likely to contain a connection string.
func dbaPostRound(deps Deps, job dbaJob, round int, findings, verdict string) {
	if deps.Jira == nil {
		return
	}
	body := fmt.Sprintf("Database review round %d found:\n\n%s\n\nHanded to %s.\n\n%s",
		round, strings.TrimSpace(findings), actors.Attribution(job.Actor), verdict)
	_ = deps.Jira.Comment(job.Key, actors.Comment(events.ActorDBA, body))
}

// dbaPostNothingFound is the one line a clean review leaves, and it says WHICH
// review it was. Silence on the ticket cannot be told from "no review ran", and
// a static review reported as a clean one is a stronger claim than was made.
func dbaPostNothingFound(deps Deps, key string, target supervisor.DBATarget) {
	if deps.Jira == nil {
		return
	}
	_ = deps.Jira.Comment(key, actors.Comment(events.ActorDBA,
		"reviewed the schema, the migrations and the indexes in this change and found "+
			"nothing to raise. This was "+target.Path()+"."))
}

// dbaNoVerdict ends the stage when the review named neither sentinel.
//
// A person, never a fix round, for the OR-204 reason applied to this actor: an
// unknown verdict dispatched as findings tells a developer to fix something
// nobody described, and the cheapest way to satisfy that instruction against a
// schema is to drop the constraint that looks closest -- which makes the
// migration succeed rather than fail, so nothing downstream catches it.
//
// The run CONTINUES, as qaNoVerdict's does: this stage does not block on its
// own authority, and "the review could not be understood" is weaker ground to
// strand a committed change on than findings would have been.
func dbaNoVerdict(job dbaJob, deps Deps, log *events.Log, w io.Writer) {
	key := job.Key
	const why = "the database review reported no verdict: its closing message named neither " +
		supervisor.DBAClean + " nor " + supervisor.DBAFindings

	log.Emit(events.Event{Kind: events.KindEscalate, Actor: events.ActorDBA,
		Model: actors.Model(events.ActorDBA),
		Msg:   why + "; no fix round was dispatched, because nothing was described to fix"})
	ui.Say(w, key, events.ActorDBA, ui.VerbFail,
		"gave no verdict on the data model. A person needs to look.")

	if deps.Jira != nil {
		_ = deps.Jira.Comment(key, actors.Comment(events.ActorDBA, why+
			".\n\nSo the data model in this change is UNREVIEWED, which is not the same as "+
			"unsound -- and is why no fix round ran: there was nothing described to fix. The "+
			"branch is going to review anyway; this stage reports, it does not block."))
	}

	tell(w, log, job.WS, notify.Event{
		Key: key, Level: notify.Blocked, Workspace: job.WS.ID, Actor: events.ActorDBA,
		Title: key + ": the database review gave no verdict",
		Body: strings.Join([]string{
			"*" + job.Summary + "*",
			"",
			"The database review ended without " + supervisor.DBAClean + " and without",
			supervisor.DBAFindings + ". The data model in this change was not reviewed, and no",
			"fix round ran -- there was nothing described to fix.",
			"",
			"_The pull request still opens: this stage reports, it does not block a change._",
			"_Read it as unreviewed, not as sound._",
		}, "\n"),
	})
}

// dbaEscalate hands the open findings to a person.
//
// The run CONTINUES afterwards, for qaEscalate's reason: stopping here would
// strand a committed, CI-gated change on the say-so of an actor that was told
// it does not get that call. What a person gets instead is the findings,
// before the review, with the pull request in front of them.
func dbaEscalate(job dbaJob, out dbaOutcome, deps Deps, log *events.Log, w io.Writer) {
	key := job.Key
	log.Emit(events.Event{Kind: events.KindEscalate, Actor: events.ActorDBA,
		Model: actors.Model(events.ActorDBA),
		Msg: fmt.Sprintf("%d fix round(s) did not clear these schema findings; escalating to a person",
			out.Rounds)})
	ui.Say(w, key, events.ActorDBA, ui.VerbFail,
		"%d fix round(s) did not clear these schema findings. A person needs to look.", out.Rounds)

	if deps.Jira != nil {
		_ = deps.Jira.Comment(key, actors.Comment(events.ActorDBA, fmt.Sprintf(
			"reviewed the data model in this change and these findings are still open after "+
				"%d fix round(s):\n\n%s\n\nThe branch is going to review anyway -- this stage "+
				"reports, it does not block. A schema decision is inherited by everything "+
				"written against it afterwards and is expensive to reverse once there is data "+
				"in it, so these are worth reading before the merge rather than after.",
			out.Rounds, out.Findings)))
	}

	tell(w, log, job.WS, notify.Event{
		Key: key, Level: notify.Blocked, Workspace: job.WS.ID, Actor: events.ActorDBA,
		Title: fmt.Sprintf("%s: schema findings are still open", key),
		Body: strings.Join([]string{
			"*" + job.Summary + "*",
			"",
			fmt.Sprintf("The database review raised these and %d fix round(s) did not clear them:",
				out.Rounds),
			"",
			quote(out.Findings),
			"",
			"_The pull request still opens: this stage reports, it does not block a change._",
			"_A schema is expensive to reverse once there is data in it -- read these first._",
		}, "\n"),
	})
}
