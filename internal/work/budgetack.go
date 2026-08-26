package work

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/orion-sdlc/orion/internal/budget"
	"github.com/orion-sdlc/orion/internal/collect"
	"github.com/orion-sdlc/orion/internal/config"
	"github.com/orion-sdlc/orion/internal/events"
	"github.com/orion-sdlc/orion/internal/slack"
	"github.com/orion-sdlc/orion/internal/ui"
	"github.com/orion-sdlc/orion/internal/workspace"
)

// Asking permission to keep spending, instead of only refusing to.
//
// The budget checkpoint is a good gate and it was delivered badly. What it
// did was print a wall of text to a terminal and decline to start the job:
//
//	BUDGET CHECKPOINT: 72% of your weekly Orion budget is used.
//	  Continue:  orion budget ack 50
//	WARNING   FCIA-8 was not started; waiting rather than retrying immediately
//
// Three things wrong with that, all of the same kind. It sent NO Slack
// message, so an unattended watcher stopped spending in silence and the
// person who configured the budget learned about it only by going to look.
// It offered no way to answer where the question was asked -- the only route
// forward was to kill the watcher, run another command, and start again. And
// the loop then re-asked the same unanswerable question every two minutes
// until somebody did.
//
// A gate that can only be satisfied by stopping the thing it gated is not a
// checkpoint. It is a crash with instructions.
//
// So the gate now ASKS, by the same two routes an approval already uses: a
// Slack message an allowlisted person can tick, and -- when someone is
// actually at the terminal -- a prompt. Either answer acknowledges the
// checkpoint and the run continues. Neither is invented authority: this is
// the same consent `orion budget ack` grants, collected where the person
// already is.
//
// Consent is still for ONE checkpoint. The next threshold stops again.

// budgetRequest remembers the Slack message asking about one checkpoint.
type budgetRequest struct {
	Threshold int       `json:"threshold"`
	Channel   string    `json:"channel"`
	TS        string    `json:"ts"`
	AskedAt   time.Time `json:"asked_at"`
}

func budgetAckPath(home string) string {
	return filepath.Join(home, "budget-ack.json")
}

func loadBudgetRequest(home string) budgetRequest {
	var r budgetRequest
	b, err := os.ReadFile(budgetAckPath(home))
	if err != nil {
		return r
	}
	_ = json.Unmarshal(b, &r)
	return r
}

func saveBudgetRequest(home string, r budgetRequest) error {
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(budgetAckPath(home), append(b, '\n'), 0o600)
}

func clearBudgetRequest(home string) { _ = os.Remove(budgetAckPath(home)) }

// budgetGate decides whether this run may spend, asking if it must.
//
// Returns proceed=true when the checkpoint is clear or has just been
// acknowledged. When it returns false the caller must not start the job --
// but by then a person has been asked, in Slack and at the terminal, so the
// refusal is a question awaiting an answer rather than a dead end.
func budgetGate(opts Options, cfg config.Config, ws *workspace.Workspace,
	log *events.Log, w io.Writer) (bool, string) {

	st, ok := budgetStatus(opts.Home, cfg)
	if !ok || st.Crossed == 0 {
		// Nothing owing. Drop any stale question so a LATER checkpoint is
		// asked afresh rather than being answered by an old tick.
		clearBudgetRequest(opts.Home)
		return true, ""
	}
	if opts.DryRun {
		return false, st.Message()
	}

	channel, _ := resolveChannel(ws)
	req := loadBudgetRequest(opts.Home)

	// 1. Has somebody already answered in Slack?
	if req.Threshold == st.Crossed && req.TS != "" && channel != "" {
		if by, decided := readBudgetAck(req, cfg); decided {
			if err := ackBudget(opts.Home, st.Crossed); err != nil {
				ui.Warn(w, "could not record the acknowledgement: %v", err)
				return false, st.Message()
			}
			clearBudgetRequest(opts.Home)
			log.Emitf(events.KindBudget, events.ActorHuman,
				"%s acknowledged the %d%% checkpoint in Slack", by, st.Crossed)
			ui.Ok(w, "ok", "%s acknowledged the %d%% budget checkpoint; continuing", by, st.Crossed)
			return true, ""
		}
	}

	// 2. Ask, once per threshold.
	if req.Threshold != st.Crossed && channel != "" {
		title, body := msgBudgetCheckpoint(st)
		if ts, err := postWithAffordances(channel, title+"\n"+body); err != nil {
			ui.Warn(w, "could not ask about the budget in Slack: %v", err)
		} else {
			_ = saveBudgetRequest(opts.Home, budgetRequest{
				Threshold: st.Crossed, Channel: channel, TS: ts, AskedAt: time.Now(),
			})
			log.Emitf(events.KindBudget, events.ActorOrion,
				"asked Slack to acknowledge the %d%% checkpoint", st.Crossed)
			ui.Ok(w, "asked", "Slack to acknowledge the %d%% budget checkpoint", st.Crossed)
		}
	}

	// 3. Somebody at the terminal can answer here and now.
	//
	// Offered AFTER the Slack message so a person who says no has still left
	// the question somewhere a colleague can answer later, and so an
	// unattended watcher is never worse off than an attended one.
	fmt.Fprint(w, st.Message())
	if askedInline(w, st.Crossed) {
		if err := ackBudget(opts.Home, st.Crossed); err != nil {
			ui.Warn(w, "could not record the acknowledgement: %v", err)
			return false, ""
		}
		clearBudgetRequest(opts.Home)
		log.Emitf(events.KindBudget, events.ActorHuman,
			"acknowledged the %d%% checkpoint at the terminal", st.Crossed)
		ui.Ok(w, "ok", "acknowledged the %d%% checkpoint; continuing", st.Crossed)
		return true, ""
	}

	if channel != "" {
		fmt.Fprintf(w, "          %s\n", ui.Dim(w, fmt.Sprintf(
			"asked in Slack too -- tick it there and the next pass continues, "+
				"or run: orion budget ack %d", st.Crossed)))
	}
	return false, ""
}

// askedInline prompts, and answers NO for anything that is not a terminal.
//
// A watcher on a timer, a CI runner, a cron line: none of those has anybody
// to ask, and reading a blocked stdin would hang the run rather than refuse
// it. Silence is not consent to spend.
func askedInline(w io.Writer, pct int) bool {
	fi, err := os.Stdin.Stat()
	if err != nil || fi.Mode()&os.ModeCharDevice == 0 {
		return false
	}
	fmt.Fprintf(w, "\nContinue past the %d%% checkpoint for this run? [y/N] ", pct)
	var ans string
	_, _ = fmt.Scanln(&ans)
	ans = strings.ToLower(strings.TrimSpace(ans))
	return ans == "y" || ans == "yes"
}

// ackBudget records consent for one checkpoint.
func ackBudget(home string, pct int) error {
	l, err := budget.Load(home)
	if err != nil && l == nil {
		return err
	}
	// AckAll rather than Ack: returning after a long gap should not stop the
	// next four runs in a row for thresholds already passed.
	l.AckAll(pct)
	return l.Save(home)
}

// readBudgetAck reports whether an allowlisted person ticked the request.
//
// Reuses the merge-approval reader deliberately. Both questions are "may
// Orion do the expensive, hard-to-undo thing", both are answered by a
// reaction on one specific message, and both must exclude Orion's own
// affordance reactions -- a second implementation would eventually differ
// from this one on that last point, which is the one that matters.
func readBudgetAck(req budgetRequest, cfg config.Config) (string, bool) {
	c, err := slack.FromEnv()
	if err != nil {
		return "", false
	}
	allow := cfg.Slack.MergeApprovers
	if len(allow) == 0 {
		// Nobody may approve a merge, but somebody has to be able to say
		// "keep going" about their own money. Fall back to the people this
		// project already puts in the room.
		allow = cfg.Slack.InviteUsers
	}
	d, err := collect.ReadDecision(c, req.Channel, req.TS, c.BotID(), allow)
	if err != nil || !d.Approved || d.Rejected {
		return "", false
	}
	return d.By, true
}

func postWithAffordances(channel, text string) (string, error) {
	c, err := slack.FromEnv()
	if err != nil {
		return "", err
	}
	ts, err := c.PostTS(channel, text)
	if err != nil {
		return "", err
	}
	c.React(channel, ts, "white_check_mark")
	return ts, nil
}

// msgBudgetCheckpoint asks the question in a way that can be refused.
//
// Names the number, says whose limit it is, and says exactly what ticking it
// authorises -- one more checkpoint's worth, not the rest of the week.
func msgBudgetCheckpoint(st budget.Status) (string, string) {
	title := fmt.Sprintf("Budget checkpoint: %d%% used — keep going?", st.Percent)
	body := strings.Join([]string{
		"Orion has stopped before starting the next ticket.",
		"",
		fmt.Sprintf("• used      %d%% of the weekly budget you configured", st.Percent),
		"• limit     this is *your* `orion.json` budget, not your Anthropic plan's",
		"",
		"React ✅ to acknowledge this checkpoint and let the queue continue.",
		"That is consent for one checkpoint; the next one stops again.",
		"",
		"_Nothing is spent until somebody answers._",
	}, "\n")
	return title, body
}
