// Package decide keeps a RECOMMENDATION apart from a DECISION.
//
// An agent that recommends something is proposing it. Every stage downstream
// reads the committed artifacts as truth -- the implementer's prompt names
// them, and internal/advise scopes an advisor to them and forbids it to
// answer from anything else -- so a recommendation that lands in that set
// unmarked stops being a proposal the moment it is written. Three stages
// later it is a premise, cited back with a file and a clause, and nobody can
// tell it apart from something a person actually agreed to. That is how a
// project ends up built on a decision nobody made.
//
// So the two states are STRUCTURAL, in two independent ways, because a
// convention that lives only in prose is a convention some later change
// quietly stops honouring:
//
//	the file says so       -- "- Status: unconfirmed" versus "- Status: confirmed"
//	the directory says so  -- PendingDir versus ConfirmedDir
//
// and only ConfirmedDir is in scope for a role (see advise.Artifacts) or in
// the implementer's prompt. An unconfirmed recommendation is therefore not
// merely LABELLED as unsettled, it is outside the set of documents any later
// stage is allowed to reason from. Deleting the label would not be enough to
// launder it; the file would still have to be moved.
//
// CONFIRMATION IS THE SLACK REACTION THAT ALREADY EXISTS. Not a second
// approval mechanism: collect.ReadDecision, the merge_approvers allowlist,
// the bot's own reactions excluded, a rejection beating every approval. A
// second path would be a second place to rediscover that the bot approved
// its own request -- and it would need its own allowlist, which is the part
// nobody would keep in step.
//
// The audit record is the confirmation APPENDED to the record, naming the
// person and pointing at the Slack message it was given in. Pointing at,
// rather than replacing: Slack holds who reacted, when, and everything said
// around it, and a record that paraphrased all that would be a second
// account of the same event that can disagree with the first.
package decide

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/orion-sdlc/orion/internal/actors"
	"github.com/orion-sdlc/orion/internal/collect"
	"github.com/orion-sdlc/orion/internal/events"
)

// Where the two states live, relative to the repository root.
//
// Not under docs/decisions: that directory is already in every advisor's
// scope, so a pending record placed beneath it would be read as agreed on
// the day it was written, which is the whole failure this package exists to
// prevent.
const (
	PendingDir   = "docs/recommendations/pending"
	ConfirmedDir = "docs/recommendations/confirmed"
)

// The status line, spelled once. It is matched exactly on confirmation, so
// a record whose status has been hand-edited into something else is refused
// rather than guessed at.
const (
	statusUnconfirmed = "- Status: unconfirmed"
	statusConfirmed   = "- Status: confirmed"
)

// confirmEmoji is the affordance Orion adds to its own question so a phone
// user taps rather than types. It is excluded when the answer is read --
// collect.ReadDecision drops the bot's own reactions -- or this line alone
// would confirm every recommendation Orion made.
const confirmEmoji = "white_check_mark"

// Record is one recommendation and, once somebody confirms it, the decision
// it became.
type Record struct {
	Key            string // the ticket it was raised on
	Title          string
	Recommendation string
	Grounding      string // where it was derived from, in the advisor's sense
	// By is an actor IDENTIFIER from internal/events, never a display name:
	// names are an operator setting and this file is persisted, so a renamed
	// agent must not orphan the record it wrote.
	By string
	At time.Time
	// Channel and TS are the Slack question. TS is the durable handle on it,
	// and without one the record can never be confirmed by reaction.
	Channel string
	TS      string
	// Approvers is the allowlist as it stood when the question was asked, so
	// a later config edit cannot retroactively change who was permitted to
	// answer it. Same reasoning as work.Hold.Approvers.
	Approvers []string
}

// SlackAPI is the slice of Slack a confirmation needs: the merge-approval
// surface, unchanged.
type SlackAPI interface {
	collect.SlackReader
	PostTS(channel, text string) (string, error)
	React(channel, ts, emoji string)
	BotID() string
}

// TrackerAPI is the one call this package makes on the tracker.
type TrackerAPI interface {
	Comment(key, text string) error
}

// Deps are the seams. Every one is optional and every absence degrades in
// the safe direction: no Slack means the record stays unconfirmed, no
// tracker means the ticket is not annotated, and neither can turn a
// recommendation into a decision.
type Deps struct {
	Slack SlackAPI
	Jira  TrackerAPI
	Log   *events.Log
	Now   func() time.Time
}

func (d Deps) now() time.Time {
	if d.Now == nil {
		return time.Now()
	}
	return d.Now()
}

// Recommend records a proposal as UNCONFIRMED and asks about it in Slack.
//
// The record is written whether or not the question reached Slack. A
// recommendation that exists only in a failed API call is one the next stage
// re-derives from scratch, and the unconfirmed state is the safe one to be
// stuck in: nothing downstream will read it. The Slack error is returned so
// the caller can say so, not swallowed.
func Recommend(deps Deps, dir string, r Record) (Record, error) {
	if strings.TrimSpace(r.Key) == "" || strings.TrimSpace(r.Recommendation) == "" {
		return r, errors.New("a recommendation needs a ticket and something to recommend")
	}
	r.At = deps.now().UTC()

	var slackErr error
	if deps.Slack != nil && r.Channel != "" {
		ts, err := deps.Slack.PostTS(r.Channel, question(r))
		if err != nil {
			slackErr = err
		} else {
			r.TS = ts
			deps.Slack.React(r.Channel, ts, confirmEmoji)
		}
	}

	if err := write(filepath.Join(dir, PendingDir, r.Key+".md"), r.markdown()); err != nil {
		return r, err
	}

	if deps.Jira != nil {
		// Attributed to the actor that recommended it, not to Orion. Orion
		// posts under its operator's account, so a ticket read months later
		// already carries comments that look like the operator wrote them --
		// and the architect defaults to the operator's own name. Without the
		// role spelled out a reader cannot tell which of the two proposed
		// this.
		_ = deps.Jira.Comment(r.Key, actors.Comment(r.By, fmt.Sprintf(
			"%s\n\nThis is a RECOMMENDATION, not a decision. It is recorded unconfirmed "+
				"at %s, and no later stage reads it: it moves to %s -- and into what the "+
				"agents may reason from -- only when somebody confirms it with :%s: in Slack.",
			strings.TrimSpace(r.Recommendation), filepath.Join(PendingDir, r.Key+".md"),
			ConfirmedDir, confirmEmoji)))
	}

	deps.Log.Emitf(events.KindNote, r.By,
		"recommended %s, unconfirmed until somebody says so", r.Key)
	return r, slackErr
}

// Confirm reads the Slack answer and, only on an allowlisted approval,
// promotes the record.
//
// Returns the decision as it was read, so a caller can report "nobody has
// answered yet" and "someone objected" differently -- they are different
// facts and only one of them means stop asking.
//
// A rejection leaves the record exactly where it is. There is nothing to
// write: unconfirmed is already what the artifact says, and a rejected
// recommendation that got its own third state would be a state every reader
// of the directory would have to learn.
func Confirm(deps Deps, dir, key string) (collect.Decision, bool, error) {
	pending := filepath.Join(dir, PendingDir, key+".md")
	b, err := os.ReadFile(pending)
	if err != nil {
		return collect.Decision{}, false, err
	}
	body := string(b)
	if !strings.Contains(body, statusUnconfirmed) {
		return collect.Decision{}, false, fmt.Errorf(
			"%s does not say %q, so it is not a pending recommendation and "+
				"promoting it would be inventing a confirmation", pending, statusUnconfirmed)
	}
	r := parseHeader(body)

	if deps.Slack == nil || r.Channel == "" || r.TS == "" {
		return collect.Decision{Why: "it was never asked about in Slack, so there is " +
			"no reaction to read; it stays unconfirmed"}, false, nil
	}
	d, err := collect.ReadDecision(deps.Slack, r.Channel, r.TS, deps.Slack.BotID(), r.Approvers)
	if err != nil || !d.Approved {
		return d, false, err
	}

	at := deps.now().UTC()
	confirmed := strings.Replace(body, statusUnconfirmed, statusConfirmed, 1) +
		"\n## Confirmation\n\n" +
		"- Confirmed by: " + d.By + "\n" +
		"- Confirmed at: " + at.Format(time.RFC3339) + "\n" +
		"- Via: " + d.How + " on the Slack message " + slackRef(r) + "\n"

	// Written before the pending copy is removed. A crash between the two
	// leaves the record in both places, which every reader resolves as
	// confirmed; the other order can lose it entirely.
	if err := write(filepath.Join(dir, ConfirmedDir, key+".md"), confirmed); err != nil {
		return d, false, err
	}
	if err := os.Remove(pending); err != nil {
		return d, true, err
	}

	if deps.Jira != nil {
		_ = deps.Jira.Comment(key, actors.Comment(events.ActorOrion, fmt.Sprintf(
			"%s confirmed this with %s in Slack (%s), so it is now a decision: %s.\n\n"+
				"Later stages read it from here on.",
			d.By, d.How, slackRef(r), filepath.Join(ConfirmedDir, key+".md"))))
	}
	// A decision in the log's sense: the alternative was to leave it
	// unconfirmed, and the reason it was not taken is a named person's
	// reaction on a specific message.
	deps.Log.Emit(events.Event{
		Kind: events.KindDecision, Actor: events.ActorHuman, Key: key,
		Msg: fmt.Sprintf("%s confirmed the recommendation with %s; it is a decision now", d.By, d.How),
		Detail: map[string]any{
			"approver": d.By, "how": d.How,
			"slack_channel": r.Channel, "slack_ts": r.TS,
			"record": filepath.Join(ConfirmedDir, key+".md"),
		},
	})
	return d, true, nil
}

// slackRef points at the message rather than reproducing it.
func slackRef(r Record) string { return r.Channel + "/" + r.TS }

// question is what Slack is asked. It says what happens if nobody answers,
// because the honest answer -- nothing -- is the one a reader assumes least.
func question(r Record) string {
	lines := []string{
		"*Recommendation on " + r.Key + "*: " + r.Title,
		"",
		strings.TrimSpace(r.Recommendation),
	}
	if s := strings.TrimSpace(r.Grounding); s != "" {
		lines = append(lines, "", "_Derived from: "+s+"_")
	}
	return strings.Join(append(lines, "",
		"React :"+confirmEmoji+": to confirm it. Until somebody does it stays a "+
			"recommendation, and no later stage will read it as agreed."), "\n")
}

// markdown renders the record. The header is flat "- Key: value" lines
// because that is the part read back, and prose is left as prose because
// nothing parses it -- a confirmation is APPENDED to these bytes, so what a
// person confirmed is byte-for-byte what the confirmed record contains.
func (r Record) markdown() string {
	title := r.Title
	if strings.TrimSpace(title) == "" {
		title = "recommendation"
	}
	lines := []string{
		"# " + r.Key + ": " + title,
		"",
		statusUnconfirmed,
		"- Ticket: " + r.Key,
		"- Recommended by: " + r.By,
		"- Recommended at: " + r.At.UTC().Format(time.RFC3339),
	}
	if r.Channel != "" {
		lines = append(lines, "- Slack: "+slackRef(r))
	}
	if len(r.Approvers) > 0 {
		lines = append(lines, "- Approvers: "+strings.Join(r.Approvers, ", "))
	}
	lines = append(lines, "",
		"## Recommendation", "", strings.TrimSpace(r.Recommendation), "")
	if s := strings.TrimSpace(r.Grounding); s != "" {
		lines = append(lines, "## Grounding", "", s, "")
	}
	return strings.Join(lines, "\n")
}

// parseHeader reads back the fields a confirmation needs, and only those.
//
// The prose is never parsed. Confirming rewrites one line and appends a
// block, so a record whose body a person edited by hand still confirms
// correctly instead of being re-rendered into something they did not read.
func parseHeader(body string) Record {
	var r Record
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "- ") {
			// The header runs from the title to the first section; anything
			// after "## " is prose that happens to contain a list.
			if strings.HasPrefix(line, "## ") {
				break
			}
			continue
		}
		field, value, ok := strings.Cut(strings.TrimPrefix(line, "- "), ": ")
		if !ok {
			continue
		}
		value = strings.TrimSpace(value)
		switch strings.ToLower(field) {
		case "ticket":
			r.Key = value
		case "recommended by":
			r.By = value
		case "recommended at":
			r.At, _ = time.Parse(time.RFC3339, value)
		case "slack":
			r.Channel, r.TS, _ = strings.Cut(value, "/")
		case "approvers":
			for _, a := range strings.Split(value, ",") {
				if a = strings.TrimSpace(a); a != "" {
					r.Approvers = append(r.Approvers, a)
				}
			}
		}
	}
	return r
}

func write(path, body string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(body), 0o644)
}
