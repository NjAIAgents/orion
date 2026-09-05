package main

// The database architect in planning: recommend, confirm, then design
// (OR-154).
//
// `orion run <id> --stage database`, at any point in the planning phase. It
// is ONE verb rather than two because the sequence it drives is not one a
// person can run in a single sitting: the database is recommended, somebody
// confirms it in Slack, and only then is the schema designed against it. So
// the command is resumable and idempotent -- it reads the two record files,
// works out which step is next, does that one, and stops:
//
//	nothing yet            -> gather, recommend a database, ask in Slack
//	the choice is pending  -> read the answer; unconfirmed means STOP
//	the choice is confirmed-> design the schema against it, recommend that
//	the schema is pending   -> read the answer
//	the schema is confirmed -> nothing to do
//
// NEITHER BECOMES A PREMISE UNTIL IT IS CONFIRMED, which is the invariant
// from OR-153 and this is its most expensive instance. An unconfirmed record
// lives in decide.PendingDir, which is in no agent's scope and in no
// implementer's prompt, so a schema nobody agreed to cannot be built against
// by accident. The schema step is not merely LABELLED as blocked on the
// choice: it reads the confirmed record and cannot run without one.
//
// THE REASONING IS PART OF THE RECOMMENDATION, not a courtesy. A report that
// names a database and argues nothing is refused here and nothing is written
// -- see planDBAProposal. "Postgres" on its own is not something a person can
// evaluate when they confirm it, and it is nothing at all to somebody asking
// in eighteen months why this project runs what it runs.
//
// THERE IS NO TRACKER COMMENT ON THIS PATH. decide annotates the ticket a
// recommendation was raised on, and in planning there is no ticket: the tree
// is created later, by the decompose stage. The tracker PROJECT key is what
// the record is filed under, and commenting on a project key would be a call
// against an issue that does not exist. Slack still carries the question,
// which is where the confirmation comes from anyway.

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/orion-sdlc/orion/internal/actors"
	"github.com/orion-sdlc/orion/internal/config"
	"github.com/orion-sdlc/orion/internal/decide"
	"github.com/orion-sdlc/orion/internal/events"
	"github.com/orion-sdlc/orion/internal/slack"
	"github.com/orion-sdlc/orion/internal/supervisor"
	"github.com/orion-sdlc/orion/internal/workspace"
)

// planDBAStage is the --stage name that drives this. Not one of
// supervisor's own stages: those are a prompt and nothing else, and this is a
// sequence with a human confirmation in the middle of it.
const planDBAStage = "database"

// The two topics one project carries, and the reason decide.Record has a
// topic at all: both records are filed under the same key, and without the
// topic the schema would overwrite the confirmed database choice.
const (
	planDBATopicDatabase = "database"
	planDBATopicSchema   = "schema"
)

// planDBADeps are the seams, injected so the sequence can be driven in a test
// without spawning an agent or reaching Slack.
type planDBADeps struct {
	Dispatch func(*workspace.Workspace, supervisor.Options) (*supervisor.Result, error)
	Decide   decide.Deps
	Out      io.Writer
}

// planDBA runs whichever step of the sequence is next.
func planDBA(ws *workspace.Workspace, deps planDBADeps) error {
	dir := ws.RepoDir()
	key := planDBAKey(ws)
	database, schema := key+"-"+planDBATopicDatabase, key+"-"+planDBATopicSchema

	if fileExists(confirmedRecord(dir, schema)) {
		fmt.Fprintf(deps.Out, "%s: the database and the initial schema are both confirmed.\n"+
			"  %s\n  %s\n", key,
			decide.ConfirmedDir+"/"+database+".md", decide.ConfirmedDir+"/"+schema+".md")
		return nil
	}

	// The choice, before anything else. A pending record is a question that
	// has already been asked, so this reads the answer rather than asking it
	// again -- a second question on the same choice would split the approvals
	// between two messages and neither would carry the decision.
	if fileExists(pendingRecord(dir, database)) {
		ok, err := planDBAConfirm(deps, dir, database, "the database")
		if err != nil || !ok {
			return err
		}
	} else if !fileExists(confirmedRecord(dir, database)) {
		return planDBARecommend(ws, deps, planDBAJob{
			Key:   key,
			Topic: planDBATopicDatabase,
			Title: "the database for " + ws.Task.Slug,
			What:  "the database",
			Prompt: supervisor.DBAChoosePrompt(key, ws.Task.Idea,
				planDBAArtifacts(ws)),
		})
	}

	if fileExists(pendingRecord(dir, schema)) {
		_, err := planDBAConfirm(deps, dir, schema, "the initial schema")
		return err
	}

	// The confirmed record itself, quoted into the prompt. Orion's paraphrase
	// of what was agreed is not what was agreed.
	choice, err := os.ReadFile(confirmedRecord(dir, database))
	if err != nil {
		return fmt.Errorf("the database choice reads as confirmed but %s could not be read: %w",
			decide.ConfirmedDir+"/"+database+".md", err)
	}
	return planDBARecommend(ws, deps, planDBAJob{
		Key:   key,
		Topic: planDBATopicSchema,
		Title: "the initial schema for " + ws.Task.Slug,
		What:  "the initial schema",
		Prompt: supervisor.DBASchemaPrompt(key, ws.Task.Idea, string(choice),
			planDBAArtifacts(ws)),
	})
}

// planDBAJob is one recommendation: what to ask for, and what to file the
// answer under.
type planDBAJob struct {
	Key    string
	Topic  string
	Title  string
	What   string // for the console: "the database", "the initial schema"
	Prompt string
}

// planDBARecommend runs the architect and records what it proposed --
// unconfirmed, and only when it argued for it.
func planDBARecommend(ws *workspace.Workspace, deps planDBADeps, job planDBAJob) error {
	fmt.Fprintf(deps.Out, "%s: asking the database architect for %s\n", job.Key, job.What)

	res, err := deps.Dispatch(ws, supervisor.Options{
		// supervisor's own "dba" stage, with the prompt supplied: the stage
		// name is what the run is filed and costed under, and this is the
		// same actor doing the same kind of work as the review stage.
		Stage:      "dba",
		Prompt:     job.Prompt,
		Model:      actors.Model(events.ActorDBA),
		Effort:     actors.Effort(events.ActorDBA),
		MaxMinutes: dbaAskMaxMinutes,
		MaxTurns:   dbaAskMaxTurns,
		Actor:      events.ActorDBA, Key: job.Key,
	})
	if err != nil {
		return err
	}
	if res == nil || res.ExitCode != 0 {
		return fmt.Errorf("the database architect's run did not finish, so nothing was recommended")
	}

	what, why, err := planDBAProposal(res.Final)
	if err != nil {
		return err
	}

	rec, slackErr := decide.Recommend(deps.Decide, ws.RepoDir(), decide.Record{
		Key: job.Key, Topic: job.Topic, Title: job.Title,
		Recommendation: what, Grounding: why,
		By:        events.ActorDBA,
		Channel:   planDBAChannel(ws),
		Approvers: config.Load(ws.RepoDir()).Slack.MergeApprovers,
	})
	name := rec.Name()
	fmt.Fprintf(deps.Out, "%s: recommended, UNCONFIRMED, in %s\n",
		job.Key, decide.PendingDir+"/"+name+".md")
	if slackErr != nil {
		// Reported, never fatal: the record exists and unconfirmed is the
		// safe state to be stuck in. What is lost is the question, and a
		// re-run of this command asks it again.
		fmt.Fprintf(deps.Out, "  the Slack question could not be posted (%v), so nobody has been\n"+
			"  asked yet; nothing downstream reads this until somebody confirms it\n", slackErr)
		return nil
	}
	fmt.Fprintf(deps.Out, "  asked in Slack. Until somebody confirms it, no later stage reads it.\n"+
		"  Then: orion run %s --stage %s\n", ws.ID, planDBAStage)
	return nil
}

// planDBAConfirm reads the answer to a question already asked, and says which
// of the three things happened -- confirmed, refused, or nobody has answered.
// They are different facts and only one of them means carry on.
func planDBAConfirm(deps planDBADeps, dir, name, what string) (bool, error) {
	d, ok, err := decide.Confirm(deps.Decide, dir, name)
	if err != nil {
		return false, err
	}
	if !ok {
		why := d.Why
		if why == "" {
			why = "nobody on slack.merge_approvers has confirmed it"
		}
		fmt.Fprintf(deps.Out, "%s is still a recommendation: %s\n"+
			"  It stays in %s, and nothing is designed against it.\n",
			what, why, decide.PendingDir+"/"+name+".md")
		return false, nil
	}
	fmt.Fprintf(deps.Out, "%s is confirmed by %s: it is a decision now, in %s\n",
		what, d.By, decide.ConfirmedDir+"/"+name+".md")
	return true, nil
}

// planDBAProposal reads what was recommended and why, and REFUSES a report
// that has one without the other.
//
// Refusing rather than recording what there is: a recommendation with no
// argument is confirmed by somebody who had nothing to read, and the record
// it leaves answers "what" to a reader who came for "why".
func planDBAProposal(final string) (what, why string, err error) {
	lines := strings.Split(final, "\n")
	// The markers are matched on a line with its decoration stripped, the way
	// dbaVerdict matches its own: a model that writes "**DBA RECOMMENDS**"
	// has still named the marker.
	at := func(marker string) (int, string) {
		for i, line := range lines {
			t := strings.TrimLeft(strings.TrimSpace(line), "*#->_ ")
			if len(t) >= len(marker) && strings.EqualFold(t[:len(marker)], marker) {
				return i, strings.TrimSpace(strings.Trim(t[len(marker):], " :*-"))
			}
		}
		return -1, ""
	}
	r, rTail := at(supervisor.DBARecommends)
	b, bTail := at(supervisor.DBABecause)

	switch {
	case r < 0 && b < 0:
		return "", "", fmt.Errorf(
			"the database architect named neither %s nor %s, so it recommended nothing "+
				"that could be recorded", supervisor.DBARecommends, supervisor.DBABecause)
	case b < 0:
		return "", "", fmt.Errorf(
			"the database architect wrote %s without %s. A choice with no argument is not "+
				"something anybody can evaluate now or revisit later, so it was refused and "+
				"nothing was recorded", supervisor.DBARecommends, supervisor.DBABecause)
	case r < 0:
		return "", "", fmt.Errorf(
			"the database architect wrote %s without %s, so there is reasoning and nothing "+
				"it argues for; nothing was recorded",
			supervisor.DBABecause, supervisor.DBARecommends)
	case b < r:
		return "", "", fmt.Errorf("%s came before %s, so what was recommended cannot be told "+
			"from why; nothing was recorded", supervisor.DBABecause, supervisor.DBARecommends)
	}

	what = strings.TrimSpace(strings.Join(append([]string{rTail}, lines[r+1:b]...), "\n"))
	why = strings.TrimSpace(strings.Join(append([]string{bTail}, lines[b+1:]...), "\n"))
	if what == "" || why == "" {
		return "", "", fmt.Errorf(
			"the database architect wrote %s and %s with nothing under one of them, so "+
				"nothing was recorded", supervisor.DBARecommends, supervisor.DBABecause)
	}
	return what, why, nil
}

// planDBAArtifacts lists the planning documents that EXIST, so the prompt
// names only real files -- artifactsFor's rule, applied at planning time.
// Nothing here is the schema: at this point there is not one.
func planDBAArtifacts(ws *workspace.Workspace) []string {
	dir := ws.RepoDir()
	cfg := config.Load(dir)
	var out []string
	for _, rel := range []string{
		"docs/intent/" + ws.Task.Slug + ".md",
		cfg.Paths.Specs + "/" + ws.Task.Slug + ".spec.md",
		cfg.PlanPath(ws.Task.Slug),
		"docs/decisions",
		// Confirmed recommendations only, for internal/advise's reason: a
		// proposal handed to an agent is cited back as agreed.
		decide.ConfirmedDir,
	} {
		if fileExists(filepath.Join(dir, filepath.FromSlash(rel))) {
			out = append(out, rel)
		}
	}
	return out
}

// planDBAKey is what the records are filed under: the tracker project this
// workspace is bound to, and the workspace id when it is bound to nothing.
func planDBAKey(ws *workspace.Workspace) string {
	if b := trackerBinding(ws); b.Key != "" {
		return b.Key
	}
	return ws.ID
}

func planDBAChannel(ws *workspace.Workspace) string {
	if ws.Task.Slack != nil {
		return ws.Task.Slack.ID
	}
	return ""
}

func pendingRecord(dir, name string) string {
	return filepath.Join(dir, filepath.FromSlash(decide.PendingDir), name+".md")
}

func confirmedRecord(dir, name string) string {
	return filepath.Join(dir, filepath.FromSlash(decide.ConfirmedDir), name+".md")
}

// runPlanDBA is the CLI path: the real agent, the real Slack, the workspace's
// own event log.
//
// Slack absent is not fatal, for slackForApproval's reason: the record is
// still written, unconfirmed, which is the safe state. What is lost is the
// asking, and a re-run asks.
func runPlanDBA(ws *workspace.Workspace) error {
	agents, err := config.LoadAgents(workspace.Home())
	if err != nil {
		return err
	}
	if err := actors.Configure(agents); err != nil {
		return err
	}

	log, _ := events.Open(events.Path(ws.Dir), events.Event{Key: planDBAKey(ws)})
	deps := planDBADeps{
		Dispatch: supervisor.Run,
		Out:      os.Stdout,
		// No tracker: see the file comment. There is no ticket in planning.
		Decide: decide.Deps{Log: log},
	}
	if c, cErr := slack.FromEnv(); cErr == nil {
		deps.Decide.Slack = c
	} else {
		fmt.Fprintf(os.Stderr, "orion: Slack is unavailable (%v), so nobody can be asked to "+
			"confirm this yet\n", cErr)
	}
	return planDBA(ws, deps)
}
