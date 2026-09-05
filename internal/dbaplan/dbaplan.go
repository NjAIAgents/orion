// Package dbaplan is the database architect's part in PLANNING (OR-154).
//
// Three steps, in this order, and the order is the whole design:
//
//	recommend  the database, with the reasoning, from the intent and the spec
//	confirm    a person says yes in Slack, and only then is it a decision
//	design     the initial schema, on the database that was agreed
//
// NEITHER THE CHOICE NOR THE SCHEMA IS A PREMISE UNTIL IT IS CONFIRMED. That
// is internal/decide's invariant and this is its highest-stakes instance: a
// schema is inherited by everything written against it and is expensive to
// reverse once there is data in it, which is why the database architect is a
// separate actor at all rather than something the implementer decides in
// passing. Nothing here writes into the confirmed directory itself; it calls
// decide.Recommend, which writes the proposal to the pending directory that
// no later stage is allowed to read.
//
// THE SCHEMA IS NOT DESIGNED WHILE THE CHOICE IS UNCONFIRMED, and that is a
// spend decision as much as a correctness one. A schema drawn against a
// database nobody has agreed to is thrown away the moment they say no, and
// until then it is a second unconfirmed document arguing for the first.
//
// THE STATE IS READ OFF THE DISK, FOR FREE. Which of the three steps a run is
// at is decided by which records exist and where they sit -- the same two
// directories that carry the meaning -- so the stage is idempotent and the
// operator drives it the way the flow actually works: run it, answer in
// Slack, run it again. There is no state file, because a second record of
// which step we are at is a record that can disagree with the records
// themselves.
package dbaplan

import (
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/orion-sdlc/orion/internal/actors"
	"github.com/orion-sdlc/orion/internal/config"
	"github.com/orion-sdlc/orion/internal/decide"
	"github.com/orion-sdlc/orion/internal/events"
	"github.com/orion-sdlc/orion/internal/supervisor"
	"github.com/orion-sdlc/orion/internal/ui"
	"github.com/orion-sdlc/orion/internal/workspace"
)

// Stage is this step's name in supervisor's stage vocabulary, so
// `orion run <workspace> --stage database` reaches it.
const Stage = "database"

// The two records this stage produces, suffixed onto the workspace slug.
//
// TWO RECORDS, NOT ONE, because they are confirmed at different times by
// different arguments: somebody may agree that this project needs Postgres
// and still want a column changed. One file would also mean the schema's
// confirmation overwrote the choice's, and the reasoning behind the choice is
// the part this ticket exists to keep.
const (
	choiceTopic = "-database"
	schemaTopic = "-schema"
)

// Bounds for one planning run. The review stage's allowance, for its reason:
// reading a spec and reasoning about a data model takes as long as it takes,
// and this one connects to nothing so it cannot wait on a database.
const (
	maxMinutes = 20
	maxTurns   = 60
)

// Deps are the seams. Every absence degrades in the safe direction: no Slack
// means the recommendation is written and stays unconfirmed, which is where
// nothing downstream reads it.
type Deps struct {
	Supervise func(*workspace.Workspace, supervisor.Options) (*supervisor.Result, error)
	Slack     decide.SlackAPI
	Log       *events.Log
	Now       func() time.Time
}

// Options are what one invocation was asked for.
type Options struct {
	Out    io.Writer
	DryRun bool
	// MaxMinutes and MaxTurns override the bounds above when the operator
	// gave them on the command line; zero means the default.
	MaxMinutes int
	MaxTurns   int
}

// Run advances the flow by exactly one step and says where it now is.
//
// One step per invocation, deliberately. Two of the three steps are somebody
// else's: the confirmation is a person's, and a run that waited for it would
// hold a terminal open for however long that person takes.
func Run(ws *workspace.Workspace, cfg config.Config, deps Deps, opts Options) error {
	out := opts.Out
	if out == nil {
		out = io.Discard
	}
	slug := ws.Task.Slug
	if strings.TrimSpace(slug) == "" {
		return fmt.Errorf("this workspace has no slug, so there is nothing to name the record after")
	}
	choiceKey, schemaKey := slug+choiceTopic, slug+schemaTopic

	choice, err := settle(deps, ws.RepoDir(), choiceKey)
	if err != nil {
		return err
	}
	switch choice.state {
	case none:
		return recommend(ws, cfg, deps, opts, out, choiceKey,
			"which database this project should use",
			supervisor.DBAPlanChoosePrompt(ws.Task.Idea, intentPath(cfg, slug), specPath(cfg, slug)))
	case pending:
		// The stage stops here rather than designing a schema on a proposal
		// nobody has agreed to. Said out loud, because "waiting for a person"
		// and "this stage did nothing" look identical from a terminal.
		ui.Say(out, choiceKey, events.ActorOrion, ui.VerbWarn,
			"the database is still a recommendation, so no schema was designed on it: %s",
			choice.why)
		fmt.Fprintf(out, "  it is recorded in %s, which no later stage reads\n",
			recordPath(decide.PendingDir, choiceKey))
		return nil
	}

	schema, err := settle(deps, ws.RepoDir(), schemaKey)
	if err != nil {
		return err
	}
	switch schema.state {
	case none:
		return recommend(ws, cfg, deps, opts, out, schemaKey,
			"the initial schema for "+slug,
			supervisor.DBAPlanSchemaPrompt(choice.body, intentPath(cfg, slug), specPath(cfg, slug)))
	case pending:
		ui.Say(out, schemaKey, events.ActorOrion, ui.VerbWarn,
			"the schema is still a recommendation: %s", schema.why)
		fmt.Fprintf(out, "  it is recorded in %s, which no later stage reads\n",
			recordPath(decide.PendingDir, schemaKey))
		return nil
	}

	ui.Say(out, slug, events.ActorDBA, ui.VerbOK,
		"the database and the initial schema are both confirmed; later stages may build on them")
	return nil
}

// recordState is where one record has got to.
type recordState int

const (
	none recordState = iota
	pending
	confirmed
)

// record is one record's state, plus what a reader needs to know about it:
// the confirmed text when there is one, and why it is not confirmed when
// there is not.
type record struct {
	state recordState
	body  string
	why   string
}

// settle reads one record's state, ASKING SLACK on the way past.
//
// The read is what advances the flow: a pending record whose question has
// since been answered becomes confirmed here, which is what makes running the
// stage again the whole interface. decide.Confirm is the only path -- there is
// no second way to confirm anything, for the reason internal/decide gives: a
// second approval mechanism is a second place to rediscover that the bot
// approved its own request.
func settle(deps Deps, repoDir, key string) (record, error) {
	if b, err := os.ReadFile(filepath.Join(repoDir, decide.ConfirmedDir, key+".md")); err == nil {
		return record{state: confirmed, body: string(b)}, nil
	}
	if _, err := os.Stat(filepath.Join(repoDir, decide.PendingDir, key+".md")); err != nil {
		return record{state: none}, nil
	}

	d, ok, err := decide.Confirm(decide.Deps{Slack: deps.Slack, Log: deps.Log, Now: deps.Now},
		repoDir, key)
	if err != nil {
		return record{}, err
	}
	if !ok {
		why := strings.TrimSpace(d.Why)
		if why == "" {
			why = "nobody has confirmed it yet"
		}
		return record{state: pending, why: why}, nil
	}
	b, err := os.ReadFile(filepath.Join(repoDir, decide.ConfirmedDir, key+".md"))
	if err != nil {
		return record{}, err
	}
	return record{state: confirmed, body: string(b)}, nil
}

// recommend runs the architect once and records what it asked to have
// recorded, as an unconfirmed proposal.
func recommend(ws *workspace.Workspace, cfg config.Config, deps Deps, opts Options,
	out io.Writer, key, title, prompt string) error {

	if deps.Supervise == nil {
		return fmt.Errorf("nothing to run the database architect with")
	}
	ui.Say(out, key, events.ActorDBA, ui.VerbWorking, "%s", title)
	if opts.DryRun {
		fmt.Fprintf(out, "  %s\n",
			"--dry-run: nothing was spent, and no recommendation was recorded")
		return nil
	}

	res, err := deps.Supervise(ws, supervisor.Options{
		Stage:  Stage,
		Prompt: prompt,
		// Its own actor on its own model, so the spend is a row against this
		// actor in the cost report and never falls to the CLI's own default
		// model (OR-133).
		Model:      actors.Model(events.ActorDBA),
		Effort:     actors.Effort(events.ActorDBA),
		MaxMinutes: orDefault(opts.MaxMinutes, maxMinutes),
		MaxTurns:   orDefault(opts.MaxTurns, maxTurns),
		Actor:      events.ActorDBA, Key: key,
	})
	if err != nil {
		return err
	}
	// A run that recorded nothing has not advanced the flow, so it is an
	// ERROR rather than a warning -- the opposite of the review stage next
	// door, and for the opposite reason. There the change is already
	// committed and failing the ticket would throw away finished work; here
	// nothing exists yet, and reporting success would tell the operator, or
	// the script driving them, that the step is done.
	if res == nil || res.ExitCode != 0 {
		return fmt.Errorf("the database architect's run did not finish, so nothing was recommended")
	}

	text, ok := supervisor.DBARecommends(res.Final)
	if !ok {
		// Recording the closing message instead would put an unmarked blob in
		// front of somebody to confirm, and a confirmation is only worth what
		// the confirmer actually read.
		deps.Log.Emitf(events.KindNote, events.ActorDBA,
			"the planning run named no %s, so nothing was recorded", supervisor.DBARecommendation)
		return fmt.Errorf("the run marked no %s, so nothing was recorded; a person needs to read "+
			"what it said", supervisor.DBARecommendation)
	}

	rec, slackErr := decide.Recommend(decide.Deps{Slack: deps.Slack, Log: deps.Log, Now: deps.Now},
		ws.RepoDir(), decide.Record{
			Key: key, Title: title, Recommendation: text,
			Grounding: intentPath(cfg, ws.Task.Slug) + " and " + specPath(cfg, ws.Task.Slug),
			By:        events.ActorDBA,
			Channel:   channelOf(ws),
			Approvers: cfg.Slack.MergeApprovers,
		})
	if slackErr != nil {
		// Not fatal: the record is written either way, and unconfirmed is the
		// safe state to be stuck in. Said out loud, because a recommendation
		// nobody was asked about will never be confirmed by itself.
		ui.Say(out, key, events.ActorDBA, ui.VerbWarn,
			"the record is written but Slack was not asked: %v", slackErr)
	}

	ui.Say(out, key, events.ActorDBA, ui.VerbOK,
		"recommended %s -- unconfirmed, and no later stage reads it until somebody says so", title)
	fmt.Fprintf(out, "  recorded in %s\n", recordPath(decide.PendingDir, key))
	if rec.TS == "" {
		fmt.Fprintf(out, "  %s\n", "nobody was asked in Slack, so it can only be confirmed there once it is")
	}
	return nil
}

// channelOf is the project's Slack channel, or empty. Empty is a normal
// answer: decide.Recommend writes the record regardless and leaves it
// unconfirmable, which is the safe direction.
func channelOf(ws *workspace.Workspace) string {
	if ws.Task.Slack != nil {
		return ws.Task.Slack.ID
	}
	return ""
}

// intentPath and specPath are the two documents this stage reads, in the
// repository's own spelling, from config rather than from a literal so a
// project that moved them gets a prompt that agrees with what the earlier
// stages actually wrote.
func intentPath(cfg config.Config, slug string) string {
	return path.Join(cfg.Paths.Intent, slug+".md")
}

func specPath(cfg config.Config, slug string) string {
	return path.Join(cfg.Paths.Specs, slug+".spec.md")
}

// recordPath names a record as a REPOSITORY path: slash-separated on every
// platform, because it is printed for a person to read rather than opened
// (OR-341).
func recordPath(dir, key string) string { return dir + "/" + key + ".md" }

func orDefault(given, fallback int) int {
	if given > 0 {
		return given
	}
	return fallback
}
