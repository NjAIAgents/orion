package work

// Holding a ticket the machine stopped, instead of blaming the ticket for it.
//
// Three tickets were claimed and dead within five seconds on 2026-08-30, each
// reported as "stage ticket failed: claude exited 1" and each left wearing
// orion-failed. Nothing had touched them: no turn, no token, no branch work.
// Recovering them by hand was nine operations -- three labels cleared, three
// transitions reversed, three worktrees and branches removed -- for a fault
// that took thirty seconds to fix and that no ticket had anything to do with
// (OR-212, OR-214).
//
// So this ending, which is neither success nor failure:
//
//	the claim goes back to the QUEUE, and orion-failed is never applied
//	the empty worktree and branch are removed, so the retry does not collide
//	the fault is announced ONCE, naming the fix, not once per ticket
//	a reaction says "I fixed it", and the doctor check says whether that is true
//	the second identical fault escalates instead of asking again
//
// Slack is detected, never required. With none, the fault and its fix are
// printed in the run output and the hold clears on the first tick that finds
// the environment healthy. The reaction is a convenience for the unattended
// case, not the mechanism.

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/orion-sdlc/orion/internal/actors"
	"github.com/orion-sdlc/orion/internal/collect"
	"github.com/orion-sdlc/orion/internal/config"
	"github.com/orion-sdlc/orion/internal/events"
	"github.com/orion-sdlc/orion/internal/notify"
	"github.com/orion-sdlc/orion/internal/tracker"
	"github.com/orion-sdlc/orion/internal/ui"
	"github.com/orion-sdlc/orion/internal/workspace"
)

// SlackAPI is what a held fault needs from Slack.
//
// The merge-approval surface, plus a threaded reply. Reused rather than
// reinvented on purpose: internal/slack/approval.go and collect's ReadDecision
// already post a request, offer the affordances, read a reaction, check it
// against an allowlist and exclude Orion's own emoji. A second confirmation
// path would be a second place for "the bot approved its own request" to be
// rediscovered.
type SlackAPI interface {
	collect.SlackAPI
	Reply(channelID, threadTS, text string) error
}

// Hold is one environmental fault and the tickets it stopped.
//
// Keyed by KIND, not by ticket. That is the whole difference between one
// message saying "claude is logged out, three tickets are waiting" and three
// messages each saying it about one ticket.
type Hold struct {
	Kind  FaultKind `json:"kind"`
	Cause string    `json:"cause"`
	Fix   string    `json:"fix"`
	// Keys are the tickets held, in the order they hit it.
	Keys []string `json:"keys"`
	// Channel and TS are the Slack ask, when there was one to make.
	Channel string `json:"channel,omitempty"`
	TS      string `json:"ts,omitempty"`
	// Approvers is the allowlist as it stood when the question was asked, so
	// a later config edit cannot retroactively change who answered.
	Approvers []string  `json:"approvers,omitempty"`
	HeldAt    time.Time `json:"held_at"`
	// Escalated: some ticket hit this fault for the second time, so the fix
	// did not work. Orion stops asking and waits for a person.
	Escalated bool `json:"escalated,omitempty"`
	// Answered records the confirmation already replied to, so a fix that is
	// still broken is reported once rather than on every tick.
	Answered string `json:"answered,omitempty"`
}

// holdFile is the state on disk.
//
// In ORION_HOME rather than in a workspace: an expired login and a missing
// nj-agents are properties of the MACHINE, and a per-project record of a
// machine-wide fault is one message per project for one problem.
type holdFile struct {
	Version int                `json:"version"`
	Holds   map[FaultKind]Hold `json:"holds"`
	// Faults counts how many times each ticket has hit each fault, and
	// survives the hold being cleared -- which is the point. The bound is
	// "one automatic retry per ticket per fault", so the count has to outlive
	// the confirmation that released the first one.
	Faults map[string]int `json:"faults"`
}

func holdPath(home string) string { return filepath.Join(home, "holds.json") }

// holdMu serialises the read-modify-write of holds.json.
//
// Every mutating function here loads the file, changes it, and writes it back.
// Without this, two jobs meeting the same fault in the same tick both read a
// file with no hold in it, both conclude they created it, and both ask in
// Slack -- which is the exact duplicate-message outcome RecordFault's "only
// the creating call asks" contract exists to prevent. The concurrency is real:
// the watcher runs jobs in parallel, and an outage hits all of them at once.
//
// A process mutex, so it is honest about what it covers: one watcher. Two
// orion processes against one ORION_HOME can still interleave, which would
// need a lock file. Not built, because nothing supports two watchers on one
// home today -- the claim label is the cross-process lock, and it is per
// ticket, not per home.
var holdMu sync.Mutex

func emptyHolds() holdFile {
	return holdFile{Version: 1, Holds: map[FaultKind]Hold{}, Faults: map[string]int{}}
}

func loadHolds(home string) holdFile {
	f := emptyHolds()
	b, err := os.ReadFile(holdPath(home))
	if err != nil {
		return f
	}
	// A corrupt file is treated as empty rather than fatal, for the reason
	// collect's request file gives: the cost is one duplicate message, and the
	// alternative is a watcher that refuses to run because of a file it can
	// rewrite.
	if json.Unmarshal(b, &f) != nil || f.Holds == nil {
		return emptyHolds()
	}
	if f.Faults == nil {
		f.Faults = map[string]int{}
	}
	return f
}

func writeHolds(home string, f holdFile) error {
	p := holdPath(home)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	// Write-and-rename: a process interrupted mid-write must not leave a
	// half-file, because recovering from that means asking the same question
	// in Slack a second time.
	//
	// A UNIQUE temp name, not p+".tmp". Two writers sharing one temp path do
	// not merely race on the content -- the first rename moves the file out
	// from under the second, which then fails with ENOENT and loses its write
	// entirely. In the same directory so the rename stays on one filesystem,
	// which is what makes it atomic.
	tmp, err := os.CreateTemp(filepath.Dir(p), "holds-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // no-op once the rename succeeds
	if _, err := tmp.Write(append(b, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	// CreateTemp makes it 0600 already; named here so the mode does not depend
	// on that staying true.
	if err := os.Chmod(tmp.Name(), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), p)
}

// faultCount keys the per-ticket, per-fault counter.
func faultCount(k FaultKind, key string) string { return string(k) + "/" + key }

// Holds reports the faults currently holding tickets, in a stable order.
//
// A plain file read with no dependencies, so a watcher can gate on it every
// tick without a network call and `orion reset --held` can list them without
// a project.
func Holds(home string) []Hold {
	f := loadHolds(home)
	out := make([]Hold, 0, len(f.Holds))
	for _, h := range f.Holds {
		out = append(out, h)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Kind < out[j].Kind })
	return out
}

// RecordFault files a ticket under its fault.
//
// Returns the hold and whether this call CREATED it. Only the creating call
// asks in Slack, which is what makes it one message per fault rather than one
// per ticket -- and the answer is a property of the file, so two concurrent
// jobs hitting the same fault cannot both decide they were first.
//
// Exported alongside Holds, Release and ClearHold because they are one state
// machine and a caller that can read and clear a hold but not file one would
// have to reach into the file format to do it.
func RecordFault(home string, f Fault, key, channel string, approvers []string,
	now time.Time) (Hold, bool, error) {

	// Held across the whole read-modify-write, not just the write. The
	// "created it" answer is derived from what the file did NOT contain, so
	// two callers reading before either writes both answer yes -- and both
	// post.
	holdMu.Lock()
	defer holdMu.Unlock()

	hf := loadHolds(home)
	hf.Faults[faultCount(f.Kind, key)]++
	// The second identical fault on the same ticket means the fix did not
	// work. Escalating rather than asking again is what stops a wrong
	// confirmation becoming a loop.
	escalated := hf.Faults[faultCount(f.Kind, key)] > 1

	h, existing := hf.Holds[f.Kind]
	if !existing {
		h = Hold{Kind: f.Kind, Cause: f.Cause, Fix: f.Fix,
			Channel: channel, Approvers: approvers, HeldAt: now}
	}
	if !contains(h.Keys, key) {
		h.Keys = append(h.Keys, key)
	}
	h.Escalated = h.Escalated || escalated
	hf.Holds[f.Kind] = h
	return h, !existing, writeHolds(home, hf)
}

// forgetFault clears a ticket's fault counts once it has ended some other way.
//
// A ticket that ran -- and was pushed, blocked, failed on its own merits or
// found nothing to do -- has proved the environment worked for it. Leaving the
// count behind would mean the NEXT environmental fault it meets, months later,
// escalates immediately instead of getting the one retry it is owed.
func forgetFault(home, key string) {
	holdMu.Lock()
	defer holdMu.Unlock()

	f := loadHolds(home)
	changed := false
	for k := range f.Faults {
		if strings.HasSuffix(k, "/"+key) {
			delete(f.Faults, k)
			changed = true
		}
	}
	if changed {
		_ = writeHolds(home, f)
	}
}

// ClearHold forgets one fault, releasing every ticket it held.
func ClearHold(home string, kind FaultKind) error {
	holdMu.Lock()
	defer holdMu.Unlock()

	f := loadHolds(home)
	if _, ok := f.Holds[kind]; !ok {
		return nil
	}
	delete(f.Holds, kind)
	return writeHolds(home, f)
}

// held is the ending: the environment stopped this and no work was attempted.
//
// job is the worktree this run had cut, or nil when the fault landed before
// there was one. claimed says whether the tracker was written to at all -- a
// fault found before the claim has nothing to hand back.
func held(res Result, key string, f Fault, job *workspace.Job, claimed bool,
	cfg config.Config, opts Options, deps Deps, ws *workspace.Workspace,
	log *events.Log, w io.Writer) Result {

	res.Outcome = OutcomeHeld
	res.Fault = f
	res.Note = f.Describe()

	ui.Say(w, key, events.ActorOrion, ui.VerbFail, "%s", res.Note)
	ui.Say(w, key, events.ActorOrion, ui.VerbOK,
		"nothing was attempted, so %s is held in the queue rather than marked failed", key)
	log.Emitf(events.KindBlocked, events.ActorOrion, "%s", res.Note)
	if opts.DryRun {
		return res
	}

	// The residue first, because it is the part that makes the retry possible.
	// A worktree and branch left behind mean the next attempt cuts
	// orion/or-214-2 and the operator is looking at two branches for one
	// ticket that has never run.
	tidyResidue(job, key, ws, w)

	if claimed {
		// Back to the queue label the claim removed, and NOT to orion-failed:
		// the deferred rollback in one() adds that for a failed or blocked
		// outcome, and this outcome is neither.
		queue := cfg.Tracker.QueueLabel
		if queue == "" {
			queue = tracker.QueueLabelDefault
		}
		// And the stage labels with it: a queued ticket that still named an
		// actor would say somebody is working what nothing has started
		// (OR-225). That fix was written against auth.go's requeue, which this
		// replaces -- dropping it here would reintroduce the bug through the
		// new path while the old path's test went on passing.
		if err := deps.Jira.SetLabels(key, []string{queue},
			append([]string{tracker.LabelWorking}, actors.StageLabels()...)); err != nil {
			ui.Say(w, key, events.ActorOrion, ui.VerbWarn,
				"could not requeue it; remove its %s label by hand or nothing will pick it up: %v",
				tracker.LabelWorking, err)
		}
		// The claim moved it to In Progress. Left there it would say an agent
		// is working it while the queue says it is waiting, which is the same
		// contradiction the label rollback exists to prevent.
		if err := deps.Jira.TransitionTo(key, "To Do"); err != nil {
			ui.Say(w, key, events.ActorOrion, ui.VerbWarn, "left it In Progress: %v", err)
		}
	}

	channel, _ := resolveChannel(ws)
	h, first, err := RecordFault(opts.Home, f, key, channel, cfg.Slack.MergeApprovers, deps.Now())
	if err != nil {
		// The hold is what the watcher gates on, so losing it means the queue
		// keeps dispatching into a broken environment. Say so loudly rather
		// than letting the next ticket discover it.
		ui.Say(w, key, events.ActorOrion, ui.VerbWarn,
			"could not record the hold (%v); the next ticket will hit the same fault", err)
	}

	ask(opts.Home, h, first, key, deps, log, w)

	title, body := msgHeld(key, res.Summary, h, res.IssueURL)
	tell(w, log, ws, notify.Event{
		Key: key, Level: notify.Blocked, Workspace: ws.ID, Actor: events.ActorOrion,
		Title: title, Body: body,
	})
	return res
}

// tidyResidue removes a worktree and branch that hold nothing.
//
// RemoveWorktree already draws the empty-versus-dirty line and refuses to
// cross it (OR-122): uncommitted work, or commits on no remote, and it returns
// an error instead of discarding them. So this is the prune rule applied at
// the moment a hold is taken, not a second judgement about what is safe to
// delete -- and a worktree with ANYTHING in it is kept and reported, which is
// what the settle path further up the stack then commits and announces.
func tidyResidue(job *workspace.Job, key string, ws *workspace.Workspace, w io.Writer) {
	if job == nil || ws == nil {
		return
	}
	if err := workspace.RemoveWorktree(ws, job.Path, false); err != nil {
		ui.Say(w, key, events.ActorOrion, ui.VerbWarn,
			"kept %s: it is not empty, so it is yours to settle\n          %v", job.Path, err)
		return
	}
	// Only after the worktree is gone. Deleting the branch first would strand
	// a checkout pointing at a ref that no longer exists.
	if _, err := gitOut(ws, "branch", "-D", job.Branch); err != nil {
		ui.Say(w, key, events.ActorOrion, ui.VerbWarn,
			"left the branch %s behind: %v", job.Branch, err)
		return
	}
	ui.Say(w, key, events.ActorOrion, ui.VerbOK,
		"removed the empty worktree and branch %s, so the retry starts clean", job.Branch)
}

// ask puts the question in Slack, once per fault.
//
// first says this call created the hold. Every later ticket joins it with a
// threaded line instead of a new message: the question is about the machine
// and it has already been asked, so asking again would train the reader that
// these messages are noise.
func ask(home string, h Hold, first bool, key string, deps Deps,
	log *events.Log, w io.Writer) {

	if h.Escalated {
		// Said in the terminal whether or not Slack is reachable: this is the
		// message that tells an operator the automation has given up.
		escalate(h, key, deps, log, w)
		return
	}
	if deps.Slack == nil || h.Channel == "" {
		// A5: Slack is detected, never required. The fault and its fix are
		// already in the run output, and the hold clears on the first tick
		// that finds the environment healthy.
		return
	}
	if !first {
		if h.TS != "" {
			_ = deps.Slack.Reply(h.Channel, h.TS, "Also held by this: "+key+".")
		}
		return
	}

	title, body := msgFaultAsk(h)
	ts, err := deps.Slack.PostTS(h.Channel, title+"\n"+body)
	if err != nil {
		ui.Say(w, key, events.ActorOrion, ui.VerbWarn, "could not ask about the fault: %v", err)
		return
	}
	// The affordances, so a phone user taps rather than types. Orion's own
	// reactions are excluded when the answer is read, or this line alone would
	// confirm every fault it reported.
	deps.Slack.React(h.Channel, ts, "white_check_mark")

	// Not deferred: the unlock ends with the write, not with the function. The
	// log line below is unrelated to the file and holding through it would
	// serialise callers on an event append for no reason.
	holdMu.Lock()
	f := loadHolds(home)
	if held, ok := f.Holds[h.Kind]; ok {
		held.TS = ts
		f.Holds[h.Kind] = held
		if err := writeHolds(home, f); err != nil {
			ui.Say(w, key, events.ActorOrion, ui.VerbWarn,
				"asked, but could not record the message (%v); the hold clears on the doctor check alone", err)
		}
	}
	holdMu.Unlock()
	log.Emitf(events.KindNote, events.ActorOrion, "asked in Slack about %s", h.Kind)
}

// escalate says the fix did not work, and stops asking.
//
// A second identical fault on the same ticket is evidence about the previous
// answer, not a new question. Asking it again is how a wrong confirmation
// becomes a loop that spends a run per round.
func escalate(h Hold, key string, deps Deps, log *events.Log, w io.Writer) {

	msg := fmt.Sprintf("%s hit `%s` again after it was reported fixed, so the fix did not work. "+
		"Orion will not ask again: clear it with `orion reset --held %s` once it is genuinely "+
		"repaired, or `orion doctor` to see what it still says.", key, h.Kind, h.Kind)
	ui.Say(w, key, events.ActorOrion, ui.VerbFail, "%s", msg)
	log.Emitf(events.KindBlocked, events.ActorOrion, "%s", msg)
	if deps.Slack == nil || h.Channel == "" {
		return
	}
	if h.TS != "" {
		_ = deps.Slack.Reply(h.Channel, h.TS, msg)
		return
	}
	_, _ = deps.Slack.PostTS(h.Channel, msg)
}

// ReleaseDeps are the seams for clearing a hold.
//
// Every field is optional and every zero value degrades honestly: no Slack
// means the doctor check alone decides, and no Recheck means nothing can be
// verified -- which is the same answer quota gets, and it is bounded by the
// escalation counter rather than by optimism.
type ReleaseDeps struct {
	Slack SlackAPI
	// Recheck re-runs the check that speaks to one fault. It returns the
	// check's grade label ("OK", "WARN", "FAIL") and its detail; an empty
	// label means nothing can answer the question without spending a run.
	Recheck func(FaultKind) (string, string)
	// Manual marks an operator saying "I have fixed it" at a terminal. It
	// does not skip the check -- it decides what an UNVERIFIABLE answer
	// means, because a person standing in front of the machine is better
	// evidence about a quota reset than Orion has any other way to get.
	Manual bool
	// Only narrows the pass to one fault. The others are left exactly as they
	// were and still reported as standing, because a command that silently
	// dropped them would report "cleared" while the queue stayed shut.
	Only FaultKind
	Now  func() time.Time
}

// Release re-checks every hold and clears the ones that are genuinely fixed.
//
// The re-check is not optional and does not depend on anybody having reacted.
// A reaction means "I have fixed it", which is a claim; three runs that die
// the same way is what believing the claim costs. So the check runs on every
// tick, a confirmation only changes what is SAID when the check disagrees, and
// a fault nothing can check releases once -- bounded by the escalation counter,
// which is why the bound exists.
//
// Returns the holds still standing.
func Release(home string, deps ReleaseDeps, w io.Writer) []Hold {
	if deps.Now == nil {
		deps.Now = time.Now
	}
	var standing []Hold
	for _, h := range Holds(home) {
		if deps.Only != "" && h.Kind != deps.Only {
			standing = append(standing, h)
			continue
		}
		label, detail := "", "no check was available"
		if deps.Recheck != nil {
			label, detail = deps.Recheck(h.Kind)
		}
		switch {
		case label == "OK":
			resume(home, h, detail, deps, w)
		case label == "":
			// Unverifiable. The retry IS the check, so it is allowed exactly
			// once -- after that the hold is a person's to clear.
			if h.Escalated && !deps.Manual {
				report(w, h, "cannot be checked and has already failed twice; "+
					"clear it with `orion reset --held "+string(h.Kind)+"` once it is fixed")
				standing = append(standing, h)
				continue
			}
			resume(home, h, detail, deps, w)
		default:
			refuse(home, h, detail, deps, w)
			standing = append(standing, h)
		}
	}
	return standing
}

// resume clears a hold and says the queue is moving again.
func resume(home string, h Hold, detail string, deps ReleaseDeps, w io.Writer) {
	if err := ClearHold(home, h.Kind); err != nil {
		report(w, h, "could not clear the hold: "+err.Error())
		return
	}
	msg := fmt.Sprintf("%s is healthy again (%s). Releasing %s.",
		h.Kind, detail, strings.Join(h.Keys, ", "))
	ui.Say(w, "", events.ActorOrion, ui.VerbOK, "%s", msg)
	if deps.Slack != nil && h.Channel != "" && h.TS != "" {
		_ = deps.Slack.Reply(h.Channel, h.TS, msg)
	}
}

// refuse keeps the hold, and answers a confirmation that turned out to be
// wrong -- once, in the thread it was given in.
//
// Reported to the person who said it was fixed, rather than as a new message,
// because the useful fact is not "still broken": it is "still broken AFTER you
// fixed it", and that only reads correctly under the question they answered.
func refuse(home string, h Hold, detail string, deps ReleaseDeps, w io.Writer) {
	report(w, h, "still broken ("+detail+"). "+h.Fix)
	if deps.Slack == nil || h.Channel == "" || h.TS == "" {
		return
	}
	d, err := collect.ReadDecision(deps.Slack, h.Channel, h.TS, deps.Slack.BotID(), h.Approvers)
	if err != nil || !d.Approved {
		return
	}
	answer := d.By + "|" + d.How
	if h.Answered == answer {
		return // already told them; saying it every tick is noise, not news
	}
	_ = deps.Slack.Reply(h.Channel, h.TS, fmt.Sprintf(
		"Thanks %s — but the check still says: %s\nNothing has been released; "+
			"%s is still waiting. React again once it passes.",
		d.By, detail, strings.Join(h.Keys, ", ")))

	holdMu.Lock()
	defer holdMu.Unlock()

	f := loadHolds(home)
	if cur, ok := f.Holds[h.Kind]; ok {
		cur.Answered = answer
		f.Holds[h.Kind] = cur
		_ = writeHolds(home, f)
	}
}

// report is the held line, and it says the same thing every tick on purpose:
// identical consecutive lines are collapsed with a count (ui.Flush), so a hold
// that stands for an hour is one line rather than sixty.
//
// It names the CAUSE as well as the current check. "claude-auth still broken"
// is a status; "claude is not authenticated -- run: claude, sign in" is
// something the reader can act on without opening anything else.
func report(w io.Writer, h Hold, what string) {
	ui.Say(w, "", events.ActorOrion, ui.VerbWaiting, "holding %s: %s -- %s",
		strings.Join(h.Keys, ", "), h.Cause, what)
}
