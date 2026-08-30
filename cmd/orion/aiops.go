package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/orion-sdlc/orion/internal/actors"
	"github.com/orion-sdlc/orion/internal/aiops"
	"github.com/orion-sdlc/orion/internal/config"
	"github.com/orion-sdlc/orion/internal/events"
	"github.com/orion-sdlc/orion/internal/registry"
	"github.com/orion-sdlc/orion/internal/supervisor"
	"github.com/orion-sdlc/orion/internal/tracker"
	"github.com/orion-sdlc/orion/internal/workspace"
)

// `orion aiops <KEY>` -- read a finished run's event log and report what is
// worth filing, as a report plus draft tickets nobody has created (OR-168).
//
// POST-RUN, NOT LIVE, and that is the design rather than a first cut. The
// output is tickets, not intervention: a pass over one ticket's event log
// after the run reaches the same conclusions with no concurrency risk, no
// coupling to a run's lifecycle, and a complete picture instead of a partial
// one. Live watching would only earn its cost if the goal were to STOP
// something mid-run, which it is not.
//
// It also means this cannot break what it observes -- the rule
// internal/supervisor/stream.go states, that observability must never be able
// to fail the thing it observes. There is nothing to fail: the run is over,
// this is a separate process, and it writes nothing anywhere.

// Bounds for the triage subagent. Tighter than any run that changes
// something: it is handed a few dozen already-structured lines and asked one
// question, and a triage still thinking after ten turns has stopped being the
// cheap second opinion it was started as.
const (
	aiopsMaxMinutes = 5
	aiopsMaxTurns   = 10
)

// aiopsOptions is what the triage subagent runs with, split from runAIOps so
// the actor, model and prompt it is configured with can be asserted without
// spawning a process -- the same reason exploreOptions and triageOptions are
// split from their callers.
func aiopsOptions(key, lines string) supervisor.Options {
	return supervisor.Options{
		Stage:      "aiops",
		Prompt:     supervisor.AIOpsPrompt(key, lines),
		MaxMinutes: aiopsMaxMinutes,
		MaxTurns:   aiopsMaxTurns,
		// Its own actor on its own model, attributed to the ticket whose log
		// it read, so a nightly triage pass is a visible row in that ticket's
		// cost report rather than spend nothing can account for.
		Actor: events.ActorAIOps, Key: key,
		Model:  actors.Model(events.ActorAIOps),
		Effort: actors.Effort(events.ActorAIOps),
	}
}

func runAIOps(args []string) {
	target := ""
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		target = args[0]
		args = args[1:]
	}
	if target == "" {
		fmt.Fprintln(os.Stderr, "orion: usage: orion aiops <KEY> [--no-agent]")
		os.Exit(64)
	}

	ws, _, err := resolveWorkspace(target)
	exitOn(err)

	// The globally configured roster (OR-132), so evidence lines name agents
	// the way the operator named them. A bad roster is fatal here for the
	// same reason it is in `orion logs`: two agents sharing one name destroys
	// exactly what the evidence lines exist to provide.
	agents, err := config.LoadAgents(workspace.Home())
	exitOn(err)
	exitOn(actors.Configure(agents))

	evs, err := events.Read(events.Path(ws.Dir))
	if err != nil {
		if os.IsNotExist(err) {
			fmt.Printf("no events yet for %s, so there is nothing to triage.\n", ws.ID)
			return
		}
		exitOn(err)
	}

	// A project key reads the whole project's log. Allowed, because a person
	// asking about a project rather than a ticket is asking the same question
	// over a wider window -- but the report has to say so, or an empty result
	// reads as "this one ticket was clean".
	//
	// Through registry.ProjectOf rather than by looking for a hyphen: some
	// project keys legitimately contain one, and that function already knows
	// the difference.
	key := registry.NormalizeKey(target)
	project := registry.ProjectOf(key)
	if key == project {
		key = ""
	}
	evs = onlyKey(evs, key)

	rep := aiops.Report{Key: reportSubject(key, ws.ID), Scanned: len(evs)}
	found := aiops.Scan(evs)

	if !hasFlag(args, "--no-agent") {
		novel, note := judgeNovel(ws, key, evs, found)
		found = append(found, novel...)
		rep.AgentNote = note
	} else {
		rep.AgentNote = "not started (--no-agent); rules only"
	}
	aiops.Sort(found)

	// Dedupe LAST, over every finding including the agent's, so a pattern the
	// agent re-proposes each night is silenced by the ticket somebody filed
	// the first time.
	open, note := openIssues(project)
	rep.DedupeNote = note
	rep.Fresh, rep.Tracked = aiops.Dedupe(found, open)

	fmt.Print(rep.Text())
}

// reportSubject names what the report is about, so a report over a whole
// project cannot be mistaken for one about a ticket.
func reportSubject(key, wsID string) string {
	if key != "" {
		return key
	}
	return wsID + " (whole project)"
}

func onlyKey(evs []events.Event, key string) []events.Event {
	if key == "" {
		return evs
	}
	var kept []events.Event
	for _, e := range evs {
		if strings.EqualFold(e.Key, key) {
			kept = append(kept, e)
		}
	}
	return kept
}

// judgeNovel runs the triage subagent over the concerning events no rule
// recognised, and returns what it proposed.
//
// It is NOT started when there is nothing left over. That is the cheap path
// first: a subagent asked to stare at a clean log to conclude "nothing here"
// is the whole cost with none of the value, and most runs are clean.
//
// Every failure of the agent degrades to the rules' own findings rather than
// failing the pass. The rules are the part that cannot hallucinate and cost
// nothing; losing the second opinion is survivable, losing the report is not.
func judgeNovel(ws *workspace.Workspace, key string, evs []events.Event,
	found []aiops.Finding) ([]aiops.Finding, string) {

	left := aiops.Concerning(evs, found)
	if len(left) == 0 {
		return nil, "not started: every concerning event was already recognised by a rule"
	}
	if key == "" {
		// The prompt names one ticket, and a whole-project log spans many.
		// Rather than misattribute the spend or lie in the prompt, the agent
		// half is skipped and the report says so.
		return nil, fmt.Sprintf("not started: %d unrecognised event(s), but a whole-project "+
			"pass has no single ticket to attribute the run to. Re-run with an issue key.", len(left))
	}

	var b strings.Builder
	for _, e := range left {
		b.WriteString(aiops.Line(e) + "\n")
	}
	res, err := supervisor.Run(ws, aiopsOptions(key, b.String()))
	if err != nil {
		return nil, fmt.Sprintf("could not run (%v); the rules above stand on their own", err)
	}
	if res.ExitCode != 0 {
		return nil, fmt.Sprintf("exited %d (%s); the rules above stand on their own",
			res.ExitCode, res.Reason)
	}
	novel := parseAIOps(res.Final)
	return novel, fmt.Sprintf("judged %d unrecognised event(s), proposed %d", len(left), len(novel))
}

// parseAIOps reads the subagent's proposals off its closing message.
//
// Only lines that START with the marker count. An agent explaining itself, or
// quoting its own instructions, would otherwise have its prose read as a
// proposal -- and this is the one path in the pass that can invent a ticket,
// so it is the one that has to be strict.
//
// Anything the marker cannot be found on yields nothing at all, including the
// case where the agent wrote its refusal in prose instead of the marker. A
// pass that guessed at an unparseable answer would be proposing tickets from
// text nobody can point at.
func parseAIOps(final string) []aiops.Finding {
	var out []aiops.Finding
	for _, line := range strings.Split(final, "\n") {
		// Leading decoration only: a model reaches for a bullet or a bold run
		// when a line is a list item, and a proposal lost to a "- " in front
		// of it is a finding silently dropped.
		line = strings.TrimLeft(strings.TrimSpace(line), "*#->_ ")
		rest, ok := strings.CutPrefix(line, supervisor.AIOpsProposePrefix)
		if !ok {
			continue
		}
		title, why, _ := strings.Cut(strings.TrimSpace(rest), "|")
		title = strings.TrimSpace(strings.Trim(title, "*_ "))
		if title == "" {
			continue
		}
		why = strings.TrimSpace(why)
		if why == "" {
			why = "The agent proposed this without saying what makes it a defect rather " +
				"than Orion degrading on purpose. Treat that as a reason for scepticism."
		}
		out = append(out, aiops.Finding{
			// Signed by the title so a proposal filed once is not re-proposed
			// the next night. Slugged rather than used raw because the
			// signature is matched exactly, and a title that rewords itself
			// slightly between runs would dedupe against nothing.
			Rule:  "novel-" + slug(title),
			Title: title,
			Why:   why,
			Novel: true,
		})
	}
	return out
}

// slug reduces a title to a stable signature: lowercase words joined by
// hyphens, capped so a marker line stays readable.
func slug(s string) string {
	var parts []string
	for _, f := range strings.Fields(strings.ToLower(s)) {
		var keep strings.Builder
		for _, r := range f {
			if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
				keep.WriteRune(r)
			}
		}
		if keep.Len() > 0 {
			parts = append(parts, keep.String())
		}
		if len(parts) == 8 {
			break
		}
	}
	if len(parts) == 0 {
		return "unnamed"
	}
	return strings.Join(parts, "-")
}

// openIssues fetches the project's open issues for the dedupe, and explains
// any way in which the dedupe came up short.
//
// An unreachable tracker is NOT fatal. The report is still worth reading
// without it -- the findings are real either way -- but it has to say the
// dedupe did not happen, because a re-proposal that looks deduped reads as a
// new defect and gets filed twice.
func openIssues(project string) ([]tracker.Issue, string) {
	if project == "" {
		return nil, "not attempted: no project key, so already-filed proposals may repeat here"
	}
	j, err := tracker.NewJiraFromEnv()
	if err != nil {
		return nil, fmt.Sprintf("not attempted (%v); already-filed proposals may repeat here", err)
	}
	issues, truncated, err := aiops.OpenIssues(j, project)
	if err != nil {
		return nil, fmt.Sprintf("search failed (%v); already-filed proposals may repeat here", err)
	}
	if truncated {
		return issues, fmt.Sprintf("only the first %d open issues in %s were scanned, "+
			"so a proposal filed beyond that may repeat here", aiops.MaxOpenScanned, project)
	}
	return issues, ""
}
