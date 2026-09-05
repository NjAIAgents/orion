package collect

// The plan-conformance pass (OR-158).
//
//	done triage says done -> read the same change against the CONFIRMED PLAN
//	                      -> conforms, and nothing more is said; diverges, and
//	                         the difference goes on the ticket and into the
//	                         event log, and the run continues either way
//
// It runs BESIDE done triage rather than anywhere else for two reasons. The
// evidence is identical -- the same green run, the same diff, already fetched
// -- so asking here costs one model call and no extra git work. And this is
// the last moment the answer is worth anything: after it a person is asked to
// approve, and "we built something other than what was agreed" is precisely
// the thing that must be visible before that click rather than in a
// post-mortem.
//
// IT NEVER BLOCKS, AND HAS NO WAY TO. It returns nothing a caller can gate
// on. That is deliberate and it is what separates this from done triage,
// which sits ten lines above and may hand a ticket back: a divergence is
// frequently the implementer finding something better while building, and a
// gate that stopped the pipeline for one would be switched off inside a week.
// The requirement is that a human SEES it, not that a machine acts on it.
//
// A TICKET WITH NO CONFIRMED PLAN GETS NO REPORT AND NO MODEL CALL. Most
// tickets have no confirmed plan artifact, and the pass says so in the event
// log and stops. Recording that as a pass would make the trail read as though
// every change had been checked against a plan.

import (
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/orion-sdlc/orion/internal/actors"
	"github.com/orion-sdlc/orion/internal/config"
	"github.com/orion-sdlc/orion/internal/conform"
	"github.com/orion-sdlc/orion/internal/done"
	"github.com/orion-sdlc/orion/internal/events"
	"github.com/orion-sdlc/orion/internal/supervisor"
	"github.com/orion-sdlc/orion/internal/ui"
	"github.com/orion-sdlc/orion/internal/workspace"
)

// maxPlan is how much plan text the model is given, across all sources.
// Beyond this the text is truncated and SAID to be truncated, for the reason
// maxPatch is: a clause the model cannot find because it was cut off is
// missing evidence, not a divergence.
const maxPlan = 40_000

// reviewConformance asks whether the change is what was agreed, and records
// the answer.
//
// Takes the diff done triage already read rather than reading its own. Two
// fetches of the same branch to answer two questions about the same commit
// would be paying twice for one fact, and worse, the two answers could then
// be about different commits.
func reviewConformance(key string, pr PR, diff done.Diff, cfg config.Config, branch string,
	opts Options, deps Deps, ws *workspace.Workspace, log *events.Log, w io.Writer) {

	if opts.DryRun {
		ui.Ok(w, "would", "%s: check the change against the confirmed plan", key)
		return
	}

	ev := conformEvidence(key, cfg, diff, ws, branch)
	var ask conform.Asker
	if deps.Conform != nil {
		ask = func(e conform.Evidence) (string, error) {
			return deps.Conform(ws, key, supervisorConformPrompt(e))
		}
	}
	v := conform.Review(ev, ask)

	// Recorded in every case, including the ones where nothing was asked.
	// "checked and matched" and "never checked" are different facts, and an
	// auditor reading this months later is asking which of the two happened
	// (ADR 0004: the event log IS the audit trail).
	log.Emit(events.Event{Kind: events.KindNote, Actor: events.ActorPlanConform,
		Model: actors.Model(events.ActorPlanConform),
		Msg:   "plan conformance: " + v.Summary() + ". " + v.Note,
		Detail: map[string]any{
			"reviewed": v.Reviewed, "plan": v.Plan, "divergences": v.Whats(),
		}})

	if !v.Diverged() {
		// Said once on the console and nowhere else. A ticket that matches
		// its plan is the ordinary case, and a comment for every one of them
		// is how a tracker becomes something people stop reading.
		ui.Say(w, key, events.ActorPlanConform, ui.VerbOK,
			"%s -- %s", v.Summary(), v.Note)
		return
	}

	report := v.Report()
	if deps.Jira != nil {
		_ = deps.Jira.Comment(key, actors.Comment(events.ActorPlanConform,
			"The checks on "+pr.URL+" are green and this change DIFFERS FROM THE "+
				"CONFIRMED PLAN.\n\n"+report))
	}
	ui.Say(w, key, events.ActorPlanConform, ui.VerbWarn,
		"this change departs from the confirmed plan; nothing is blocked")
	for _, d := range v.Divergences {
		ui.Say(w, key, events.ActorPlanConform, ui.VerbWarn, "%s", d.What)
	}
}

// conformEvidence gathers the confirmed plan and pairs it with the diff.
//
// Two sources, and only confirmed ones:
//
//	the plan stage's artifact for this task, which is what "we agreed to
//	build" means when the work came through `orion new` and `orion plan`, and
//
//	this ticket's confirmed recommendation record, which is the only artifact
//	in the repository that a later stage is allowed to read as agreed
//	(internal/decide). Its pending sibling is deliberately not read: a
//	recommendation nobody answered is not a plan, and enforcing one would be
//	holding a change to a decision that was never made.
func conformEvidence(key string, cfg config.Config, diff done.Diff,
	ws *workspace.Workspace, branch string) conform.Evidence {

	ev := conform.Evidence{Key: key, Diff: conform.Diff{
		Stat: diff.Stat, Patch: diff.Patch,
		Truncated: diff.Truncated, Unreadable: diff.Unreadable,
	}}

	dir := worktreeOrRepo(ws, branch)
	if dir == "" {
		ev.NoPlan = "there is no checkout to read a plan artifact from"
		return ev
	}

	var want []string
	if ws != nil && strings.TrimSpace(ws.Task.Slug) != "" {
		want = append(want, cfg.PlanPath(ws.Task.Slug))
	}
	want = append(want, config.ConfirmedDir+"/"+key+".md")

	budget := maxPlan
	for _, rel := range want {
		b, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(rel)))
		if err != nil || strings.TrimSpace(string(b)) == "" {
			continue
		}
		text, cut := string(b), false
		if len(text) > budget {
			text, cut = text[:budget], true
		}
		budget -= len(text)
		ev.Plan = append(ev.Plan, conform.Source{Path: rel, Text: text, Truncated: cut})
		if budget <= 0 {
			break
		}
	}
	if len(ev.Plan) == 0 {
		ev.NoPlan = "none of " + strings.Join(want, ", ") + " exists on this branch, " +
			"so nothing was agreed that this change could be held to"
	}
	return ev
}

// supervisorConformPrompt renders the plan sources into the prompt.
//
// Each source is headed by its own path, because a divergence naming no
// document is one nobody can re-check -- and with two artifacts in front of
// it the model has to be able to say which one it read.
func supervisorConformPrompt(ev conform.Evidence) string {
	var b strings.Builder
	truncated := ev.Diff.Truncated
	for i, s := range ev.Plan {
		if i > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString(s.Path + "\n\n" + strings.TrimSpace(s.Text))
		if s.Truncated {
			truncated = true
		}
	}
	stat := strings.TrimSpace(ev.Diff.Stat)
	if stat == "" {
		stat = "(the file summary could not be read)"
	}
	return supervisor.ConformPrompt(ev.Key, b.String(), stat,
		strings.TrimSpace(ev.Diff.Patch), truncated)
}
