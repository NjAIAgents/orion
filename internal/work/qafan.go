package work

import (
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/orion-sdlc/orion/internal/actors"
	"github.com/orion-sdlc/orion/internal/config"
	"github.com/orion-sdlc/orion/internal/events"
	"github.com/orion-sdlc/orion/internal/suite"
	"github.com/orion-sdlc/orion/internal/supervisor"
	"github.com/orion-sdlc/orion/internal/ui"
)

// suiteTimeout bounds one Orion-run suite. A var so a test can shrink it.
//
// Twenty minutes, matching scripts/test.sh's own -timeout: internal/work runs
// close to ten minutes on Linux and internal/collect took 559s on Windows
// (OR-292), so a shorter bound would report a healthy repository as hung.
var suiteTimeout = 20 * time.Minute

// fanAuthoring writes the derived cases across several subagents at once
// (OR-305), and returns the case list the QA session that follows should be
// given.
//
// WHAT IT RETURNS, AND WHY THAT IS THE INTERESTING PART. On the serial path
// it returns the cases unchanged, so the stage behaves exactly as it did. On
// the fanned path it returns them unchanged too -- QA still gets the full
// list, still reads the diff, still owns the verdict. The children write
// files; they do not decide anything. That is what keeps the fan an
// optimisation rather than a second opinion nobody asked for.
//
// NOBODY RUNS ANYTHING HERE. The children write spec files and end their
// turns. Compilation and execution happen once, afterwards, which is what
// keeps ADR 0016's "builds are not isolated" hazard out of reach: there is no
// window in which one child compiles against another's half-written file.
//
// The serial path is taken whenever the fan cannot run -- no Fan injected, a
// width below two, too few cases to divide, an empty list. Each of those is
// silent by design: they are the ordinary shape of a small ticket, not a
// degradation worth a warning.
func fanAuthoring(job qaJob, cfg config.Config, cases string,
	deps Deps, log *events.Log, w io.Writer) string {

	// The two features are independent and must not be entangled here.
	// Authoring fans only when there is enough to divide; the SUITE runs
	// either way, because "Orion runs the tests rather than an agent"
	// (OR-306) is a property of the stage and not a side effect of the fan.
	// Running it only on the fanned path would leave every small ticket
	// verified the old way, which is the silent half-adoption this comment
	// exists to prevent.
	if deps.Fan == nil || strings.TrimSpace(cases) == "" {
		return cases
	}
	groups := caseGroups(cases, cfg.QA.Authors())
	if len(groups) < 2 {
		return cases
	}

	key := job.Key
	// Said out loud before the spend, per nj-agents CONVENTIONS-orchestration
	// §C: a fan is a multiplication of cost, and a reader who sees the agent
	// count only in the bill has been told too late.
	ui.Say(w, key, events.ActorQA, ui.VerbWorking,
		"writing %d case(s) across %d authors", countCases(cases), len(groups))
	log.Emit(events.Event{Kind: events.KindRunStart, Actor: events.ActorQA,
		Model: actors.Model(events.ActorQA),
		Msg: "fanning test authoring: " + strconv.Itoa(countCases(cases)) +
			" cases across " + strconv.Itoa(len(groups)) + " authors"})
	// The live row shows the fan while it is happening rather than only in
	// the log afterwards. A row that says "qa" while five subagents work is
	// the display lying by omission -- the same class of gap OR-265 closed
	// for finished rows.
	ui.LiveActivityNote(key, events.ActorQA, "authoring x"+strconv.Itoa(len(groups)))
	// The row's subagent count, one per author, said EXPLICITLY.
	//
	// ActivityLogger increments this when an agent calls the Task or Agent
	// TOOL, which is how an ordinary delegation becomes visible. This fan
	// dispatches sessions from Go through supervisor.Fan, so no such tool
	// call ever happens and nothing else would tell the display that five
	// authors exist.
	//
	// Observed on the first real run of the feature: the note read
	// "authoring x5" while the agents column stayed blank, so the row
	// contradicted itself about the same fan.
	for range groups {
		ui.LiveAgents(key)
	}

	jobs := make([]supervisor.Options, 0, len(groups))
	for _, g := range groups {
		jobs = append(jobs, supervisor.Options{
			Stage:  "qa",
			Prompt: supervisor.QAAuthorPrompt(key, job.Summary, g),
			Model:  actors.Model(events.ActorQA),
			Effort: actors.Effort(events.ActorQA),
			// The same allowance the single QA run gets. A child writing a
			// fifth of the cases is not a fifth of the work: it still reads
			// the ticket and the surrounding tests before it writes anything.
			MaxMinutes: job.MaxMinutes, MaxTurns: job.MaxTurns,
			OnActivity: ActivityLogger(log, w, key, events.ActorQA),
			Actor:      events.ActorQA, Key: key,
		})
	}

	results := deps.Fan(job.WS, jobs)

	// A child that failed is reported and otherwise ignored. Its cases are
	// still in the list handed to the QA session below, which reads the diff
	// and writes what it finds missing -- so a failed author costs a retry of
	// that group's work, never the cases themselves.
	failed := 0
	for _, r := range results {
		if r.Err != nil || r.Result == nil || r.Result.ExitCode != 0 {
			failed++
		}
	}
	if failed > 0 {
		ui.Say(w, key, events.ActorQA, ui.VerbWarn,
			"%d of %d authors did not finish; QA covers their cases itself",
			failed, len(groups))
		log.Emitf(events.KindNote, events.ActorQA,
			"%d of %d authoring subagents failed; their cases stay in the list QA verifies",
			failed, len(groups))
	}
	return cases
}

// runAuthoredSuite runs the repository's own suite as a process Orion owns
// (OR-306), after the authors have written their files and before the QA
// session forms its verdict.
//
// WHY ORION RUNS IT RATHER THAN THE AGENT. Until now the suite was executed
// only because a prompt asked an agent to execute it, which handed the agent
// three decisions it should not have: what to run, whether to run it, and how
// to report the result. A stage could go green because an agent ran a
// narrower subset than it claimed. A process exits 0 or it does not.
//
// IT DOES NOT DECIDE ANYTHING. The result is reported and logged; the verdict
// still belongs to the QA session, which reads the diff and the cases. This
// is evidence for that session, not a second opinion competing with it.
//
// Detection failure is not an error here. suite.Detect is deliberately narrow
// and returns ErrNotFound for anything it cannot be certain of, which leaves
// the delegated path exactly as it was -- the agent is still told how to run
// the tests. Degrading is allowed; degrading silently is not, so an
// undetectable suite says so.
// The RESULT is returned, not only reported (OR-312).
//
// The first cut printed "the suite is red", logged the output, and returned
// nothing. QA then formed its verdict having never been told, so a ticket
// whose suite Orion had ALREADY RUN AND FAILED reported "every case passes".
// Observed on four tickets in one run: a gofmt error found in the worktree
// reached a shared branch and failed CI twenty minutes later.
//
// That is worse than not running the suite at all. The stage had more
// information and acted on less, and a red line nobody acts on teaches a
// reader to skip it.
//
// A nil result means no verdict is available -- the suite could not be
// detected, or the workspace was absent. Not knowing is not the same as
// failing, and the caller must treat it as the former.
func runAuthoredSuite(job qaJob, cfg config.Config, log *events.Log, w io.Writer) *suite.Result {
	key := job.Key
	// No workspace means there is nowhere to run, which is a caller error
	// rather than a repository without tests. Returning beats panicking on
	// the QA path: a stage that crashes takes the whole ticket with it, and
	// this step is evidence-gathering, not the verdict.
	if job.WS == nil {
		return nil
	}
	dir := job.WS.RepoDir()

	argv, err := suite.Detect(dir, cfg.QA.Procs())
	if err != nil {
		ui.Say(w, key, events.ActorQA, ui.VerbOK,
			"no suite Orion can run here, so QA runs the tests itself")
		log.Emitf(events.KindNote, events.ActorQA,
			"suite detection found nothing certain; QA runs the tests in its own session")
		return nil
	}

	ui.LiveActivityNote(key, events.ActorQA, "running the suite")
	ui.Say(w, key, events.ActorQA, ui.VerbWorking, "running %s", argv[0])

	res := suite.Run(dir, argv, suiteTimeout)

	switch {
	case res.Err != nil:
		// Neither a pass nor a fail. An unknown verdict must never read as a
		// pass, so it is said plainly and the QA session decides regardless.
		ui.Say(w, key, events.ActorQA, ui.VerbWarn,
			"could not run the suite: %v", res.Err)
		log.Emitf(events.KindNote, events.ActorQA, "suite did not run: %v", res.Err)
	case res.TimedOut:
		ui.Say(w, key, events.ActorQA, ui.VerbWarn,
			"the suite did not finish within %s", suiteTimeout)
		log.Emitf(events.KindNote, events.ActorQA,
			"suite timed out after %s: %s", suiteTimeout, res.Cmd)
	case res.Passed:
		ui.Say(w, key, events.ActorQA, ui.VerbOK, "the suite is green")
		log.Emitf(events.KindNote, events.ActorQA, "suite passed: %s", res.Cmd)
	default:
		ui.Say(w, key, events.ActorQA, ui.VerbWarn, "the suite is red")
		// The OUTPUT, not just the fact. This is what the fix loop needs and
		// what `orion logs` reads afterwards; a recorded failure with no
		// output is a failure nobody can act on (the same argument
		// release-gate.sh makes for not discarding its own).
		log.Emit(events.Event{Kind: events.KindQA, Actor: events.ActorQA,
			Model: actors.Model(events.ActorQA),
			Msg:   "suite failed: " + res.Cmd + "\n" + res.Output})
	}
	return &res
}

// suiteEvidence is what the QA session is told about a suite Orion already
// ran (OR-312).
//
// EVIDENCE, NOT A VERDICT. QA still decides; this gives it the one thing it
// could not otherwise know, which is what the repository's own suite says
// about the whole tree rather than about the cases QA chose to run.
//
// Empty for anything that is not a definite failure. A pass needs no telling
// -- QA runs its own cases regardless -- and a suite that could not run is
// not evidence of anything. Only a red suite changes what QA should conclude.
func suiteEvidence(res *suite.Result) string {
	if res == nil || res.Passed || res.Err != nil {
		return ""
	}
	if res.TimedOut {
		return "\n" + strings.Join([]string{
			"",
			"ORION RAN THE SUITE AND IT DID NOT FINISH within its wall clock:",
			"  " + res.Cmd,
			"",
			"That is not the same as a failure, and it is not the same as a pass.",
			"Say so in your verdict rather than reporting the change verified.",
		}, "\n")
	}
	return "\n" + strings.Join([]string{
		"",
		"ORION RAN THE SUITE AND IT FAILED. This is the repository's own suite,",
		"run as a process over the whole tree after every test was written:",
		"  " + res.Cmd,
		"",
		res.Output,
		"",
		"DO NOT RUN THE SUITE AGAIN. Orion has run it; the result above is the",
		"one that counts, and re-running it wastes several minutes and tells you",
		"nothing new. Your job now is the VERDICT.",
		"",
		"Read that before you decide. It covers ground your own cases do not --",
		"formatting, vet, the coverage floor, and every package rather than the",
		"ones this ticket touched.",
		"",
		"If the failure belongs to this change, it is a finding: report it and",
		"let the fix round address it. If it does not -- a pre-existing failure",
		"elsewhere in the tree -- say which, and say why this change is still",
		"verified. What you must NOT do is report every case passing without",
		"mentioning it: a green verdict over a red suite is how a formatting",
		"error reached a shared branch and failed CI twenty minutes later.",
	}, "\n")
}
