// Package aiops is the post-run triage pass: it reads one ticket's event log
// AFTER the run has finished and reports what is worth a person's attention,
// as a report plus draft tickets that nobody has filed.
//
// It exists because nobody reads the logs during an unattended run. Every
// defect Orion has found in itself so far -- OR-162's false exhaustion,
// OR-167's swallowed QA findings -- was found by a human pasting a log to
// somebody who read it, and that does not happen overnight (OR-168).
//
// Four design choices govern everything here, and each of them is load
// bearing.
//
// IT READS THE EVENT LOG, NOT THE TRANSCRIPT. internal/events is structured,
// small, append-only and already carries actor, kind and key. The transcript
// is tens of thousands of tokens of tool output that would have to be re-read
// on every pass. Scan below takes a slice of typed events and nothing else,
// so there is no path by which a transcript could get in.
//
// MOST DETECTION NEEDS NO AGENT. Every rule in this file is a pure function
// over typed events. A rule cannot hallucinate a breaker trip that did not
// happen, cannot cost anything, and can be tested against a fixture. The
// agent is reserved for the far smaller and more honest job of judging
// whether a pattern NOTHING here recognises is worth reporting -- which is
// what Concerning exists to decide, and why it can answer "nothing".
//
// IT PROPOSES; IT DOES NOT FILE. Not a convention: the Open interface below
// carries a search method and nothing else, so this package could not create
// an issue if some later change asked it to. Three reasons the rule is
// absolute. The backlog is already hard to scan and an autonomous filer makes
// that worse fastest; a human decides what gets created, everywhere else in
// this codebase; and, most of all --
//
// ORION DEGRADES ON PURPOSE. A lock timeout proceeds unlocked and says so. A
// QA failure is a warning, not a block. An absent gh is fine. So a pass that
// matched on the word "error" would file tickets for behaviour that is
// working exactly as designed, and after the third such ticket nobody would
// read the fourth. EVERY rule below therefore carries a Why that says what
// separates it from the designed degradation it most resembles, and the ones
// deliberately NOT written are listed at notRules.
package aiops

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/orion-sdlc/orion/internal/actors"
	"github.com/orion-sdlc/orion/internal/events"
	"github.com/orion-sdlc/orion/internal/tracker"
)

// Finding is one thing in a finished run worth a person's minute.
type Finding struct {
	// Rule is the stable identifier of what recognised this, and doubles as
	// the dedupe signature: a defect class is filed once, not once per night.
	Rule string
	// Title is the draft ticket's summary.
	Title string
	// Why says what makes this different from Orion degrading on purpose.
	// Carried into the draft because a reader deciding whether to file it
	// needs the distinction more than they need the evidence.
	Why string
	// Evidence is the event lines the finding rests on, already rendered.
	// A finding whose evidence cannot be read is a finding nobody can check.
	Evidence []string
	// At is the earliest event behind it, so a report can order by when.
	At time.Time
	// Novel marks a finding the agent proposed rather than a rule matched.
	// Weighed differently on purpose: a rule cannot be wrong about what it
	// saw, and the agent can.
	Novel bool
}

// Marker opens the trailer line every draft carries. When a person files the
// draft, this line goes with it, and the next pass finds it and stays quiet.
// A triage that re-files OR-162 every night is worse than no triage at all.
const Marker = "orion-aiops:"

// rule is one recogniser: what it is called, what it would file, why it is
// not designed behaviour, and the events that trigger it.
//
// hits returns the TRIGGERING events rather than a bool so a finding can
// carry its evidence, and so a rule that fires four times reports once with
// four lines rather than four times with one.
type rule struct {
	ID    string
	Title string
	Why   string
	hits  func(evs []events.Event) []events.Event
}

// rules are in the order they are worth reading. First is the one that is
// definitionally a defect in this build; last is the general "the run ended
// badly and stayed that way".
//
// Whole-log rules (ask-unanswered, run-failed-terminal) sit in the same table
// as per-event ones because the difference is invisible to a caller and
// splitting the table would invite the two halves to drift.
var rules = []rule{{
	ID:    "rate-limit-unrecognised",
	Title: "the CLI reported a rate-limit status this build does not recognise",
	Why: "The message says so itself: an unrecognised status stops work to be safe, " +
		"and if work should have continued that is a bug in the classifier. " +
		"OR-162 is the same defect class with a different symptom.",
	hits: func(evs []events.Event) []events.Event {
		return filter(evs, func(e events.Event) bool {
			// Matched on the phrase Describe writes rather than on a shared
			// constant, so this package does not have to import the
			// supervisor to read one string. The coupling is real either way,
			// so it is pinned by a test that builds an unrecognised limit,
			// renders it, and asserts this rule matches the result.
			return e.Kind == events.KindBudget &&
				strings.Contains(e.Msg, "does not recognise")
		})
	},
}, {
	ID:    "qa-no-verdict",
	Title: "QA finished without reporting a verdict",
	Why: "QA reporting findings is the system working -- QA reports, it does not block. " +
		"QA reporting NOTHING is not: the change went to review unverified, and " +
		"no fix round could run because nothing was described to fix.",
	hits: func(evs []events.Event) []events.Event {
		return filter(evs, func(e events.Event) bool {
			return e.Kind == events.KindEscalate && e.Actor == events.ActorQA &&
				strings.Contains(e.Msg, "never reported a verdict")
		})
	},
}, {
	ID:    "fix-loop-exhausted",
	Title: "the CI fix loop stopped without a green build",
	Why: "The loop is bounded on purpose, so stopping is correct behaviour. Reaching " +
		"the bound is not the same thing: money was spent on attempts that never " +
		"converged, and the branch is still red.",
	hits: func(evs []events.Event) []events.Event {
		return filter(evs, func(e events.Event) bool {
			return e.Kind == events.KindFailed && strings.HasPrefix(e.Msg, "stopped fixing:")
		})
	},
}, {
	ID:    "no-commit-blocked",
	Title: "a run ended cleanly having produced no commits",
	Why: "NOT the no-change ending, which is a correct outcome and is recorded as a " +
		"note rather than as blocked. This is a run that exited zero, changed " +
		"nothing, and did not say it had nothing to do -- so its closing message " +
		"had to be read as a question instead.",
	hits: func(evs []events.Event) []events.Event {
		return filter(evs, func(e events.Event) bool {
			return e.Kind == events.KindBlocked && strings.Contains(e.Msg, "produced no commits")
		})
	},
}, {
	ID:    "ask-unanswered",
	Title: "a question was raised and the log never says what became of it",
	Why: "A refusal is a valid close -- an advisor that will not invent an answer is " +
		"working correctly. An ask with NEITHER an answer nor a refusal is the " +
		"defect internal/events documents at OR-201: whatever the implementer then " +
		"did on the strength of the reply cannot be explained afterwards.",
	hits: unansweredAsks,
}, {
	ID:    "run-failed-terminal",
	Title: "a run exited non-zero and nothing afterwards recovered it",
	Why: "A non-zero exit the fix loop then recovered from is the loop doing its job, " +
		"so those are excluded. This is the ticket ending on a failure with no " +
		"commit, push, pull request or merge after it.",
	hits: terminalFailures,
}}

// notRules records the recognisers deliberately NOT written, and why. It is
// documentation rather than code because the risk it guards against is a
// future change adding one of them in good faith.
//
//   - A no-change ending. internal/work/noop.go is explicit that a run which
//     inspected the repository and found the work already present is a
//     RESULT, not a failure. Filing it teaches the reader that these tickets
//     mean nothing.
//   - A single lock timeout. procsafe returns it precisely so the caller can
//     proceed unserialized and say so; one is the design working. (Repeated
//     ones under parallel load would be worth knowing -- but they are written
//     to stderr, not to the event log, so nothing here can see them.)
//   - An absent optional tool. "Detected, never required" runs through this
//     codebase; a missing gh is a supported configuration.
//   - QA findings that a fix round cleared. The round clearing them is the
//     system working end to end.
//   - QA findings that escalated to a person. Real, but a defect in the
//     CHANGE rather than in Orion, and a person was already told at the
//     moment it happened -- by Slack and by a comment on the ticket. This
//     pass exists for what nobody was told.
var notRules struct{}

// Scan applies every rule to one ticket's events and returns what fired,
// newest evidence last within a finding and rule order between them.
//
// Pure, and deliberately so: no clock, no filesystem, no network, no tracker.
// Everything this pass concludes has to be reproducible from a log file,
// because the alternative is a report nobody can check.
//
// A rule that fires more than once yields ONE finding carrying every hit. Four
// drafts for four symptoms of one defect is the backlog noise this whole
// design is arranged to avoid.
func Scan(evs []events.Event) []Finding {
	var out []Finding
	for _, r := range rules {
		hits := r.hits(evs)
		if len(hits) == 0 {
			continue
		}
		f := Finding{Rule: r.ID, Title: r.Title, Why: r.Why, At: hits[0].At}
		for _, h := range hits {
			f.Evidence = append(f.Evidence, Line(h))
			if !h.At.IsZero() && (f.At.IsZero() || h.At.Before(f.At)) {
				f.At = h.At
			}
		}
		if len(hits) > 1 {
			f.Title = fmt.Sprintf("%s (%d times)", f.Title, len(hits))
		}
		out = append(out, f)
	}
	return out
}

// unansweredAsks returns every ask that no later answer or refusal closed.
//
// Positional rather than paired by identifier, because the log carries no
// question id: an ask is closed by the next answer or refusal on the same
// ticket. That is exactly how a reader reads it, and matching what a reader
// would conclude is the point -- a rule that disagreed with the log's own
// plain reading would be reporting on itself.
func unansweredAsks(evs []events.Event) []events.Event {
	var open []events.Event
	for _, e := range evs {
		switch e.Kind {
		case events.KindAsk:
			open = append(open, e)
		case events.KindAnswer, events.KindRefuse:
			if len(open) > 0 {
				open = open[:len(open)-1]
			}
		}
	}
	return open
}

// terminalFailures returns the non-zero run exits that nothing later undid.
//
// "Later undid" means a commit, a push, a pull request or a merge appears
// after it. Those four are the only events that say the ticket made progress
// past the failure; a note or a stage boundary says only that something
// happened next.
func terminalFailures(evs []events.Event) []events.Event {
	var out []events.Event
	for i, e := range evs {
		if e.Kind != events.KindRunEnd || exitCode(e.Msg) == 0 {
			continue
		}
		if recoveredAfter(evs[i+1:]) {
			continue
		}
		out = append(out, e)
	}
	return out
}

func recoveredAfter(rest []events.Event) bool {
	for _, e := range rest {
		switch e.Kind {
		case events.KindCommit, events.KindPush, events.KindPR, events.KindMerge:
			return true
		}
	}
	return false
}

// exitCode reads the code out of the run-end message, which internal/work
// writes as "exit N". An unparseable message yields 0 -- treated as "did not
// fail" -- because a rule that guesses a failure from a format it does not
// understand is the hallucination this design refuses to allow.
func exitCode(msg string) int {
	rest, ok := strings.CutPrefix(strings.TrimSpace(msg), "exit ")
	if !ok {
		return 0
	}
	n, err := strconv.Atoi(strings.TrimSpace(rest))
	if err != nil {
		return 0
	}
	return n
}

// concerningKinds are the kinds that mean something stopped and needs a
// person. Used only to decide whether the agent has anything to look at, so
// the cost of getting this wrong is money, not a wrong report.
//
// Two kinds that look like they belong here are deliberately absent, and both
// omissions are the "degrades on purpose" rule applied to spending.
//
// KindRefuse is an advisor declining to invent an answer it cannot ground.
// That is the advisor working, and no rule claims a refusal -- so including
// it would send every correctly-refused question to a paid agent, every run.
//
// KindRunEnd is either a failure nothing recovered, which run-failed-terminal
// already claims, or one the fix loop recovered from, which is the loop doing
// its job. Neither needs judging.
var concerningKinds = map[string]bool{
	events.KindFailed:   true,
	events.KindBlocked:  true,
	events.KindEscalate: true,
}

// Concerning returns the events that went wrong and that NO rule claimed.
//
// This is the cheap-path-first gate on spending money. If every concerning
// event in the log is already explained by a rule, there is nothing novel to
// judge and the agent is not started at all: a subagent staring at a clean
// log to conclude "nothing here" is the cost with none of the value.
//
// Claimed by evidence line rather than by event identity because events carry
// no id, and the rendered line is what the finding actually reported -- so
// what is passed to the agent is exactly what no finding already says.
func Concerning(evs []events.Event, found []Finding) []events.Event {
	claimed := map[string]bool{}
	for _, f := range found {
		for _, e := range f.Evidence {
			claimed[e] = true
		}
	}
	var out []events.Event
	for _, e := range evs {
		if !concerningKinds[e.Kind] || claimed[Line(e)] {
			continue
		}
		out = append(out, e)
	}
	return out
}

// Line renders one event the way both the report and the drafts show it.
//
// Through actors.Display, so a renamed agent renders with the name the
// operator chose. The identifier is what the log persists; the name is
// applied here, at render time, exactly as internal/actors requires.
func Line(e events.Event) string {
	at := "         "
	if !e.At.IsZero() {
		at = e.At.UTC().Format("15:04:05")
	}
	return fmt.Sprintf("%s  %-9s %-24s %s", at, e.Kind, actors.Display(e.Actor), oneLine(e.Msg))
}

// Open is the read-only slice of the tracker this pass may use.
//
// One method, and it is a search. PROPOSE, DO NOT FILE is enforced by this
// type rather than by a comment somebody deletes: there is no method here
// that could create, comment on or transition an issue, so no later change
// inside this package can start filing by accident. TestOpenCannotFile pins
// it.
type Open interface {
	Search(jql string, maxResults int) ([]tracker.Issue, error)
}

// MaxOpenScanned bounds the dedupe search. A project with more open issues
// than this is not silently truncated -- Dedupe reports the truncation, and
// the report says the dedupe may be incomplete, because a dedupe that
// quietly gave up reads exactly like one that found nothing.
const MaxOpenScanned = 200

// Dedupe splits findings into the ones with no open ticket and the ones
// already tracked.
//
// Matched on the marker trailer rather than on the summary text. A person who
// files a draft will reword its title -- that is the point of a draft -- and a
// title match would then miss it and re-propose the same defect tomorrow. The
// trailer survives rewording, and the draft prints it for exactly that reason.
//
// The signature is the RULE, not the rule plus the ticket. An unrecognised
// rate-limit status is one defect in this build however many tickets trip
// over it, and filing it per-ticket is the nightly re-filing this design
// exists to prevent.
func Dedupe(found []Finding, open []tracker.Issue) (fresh, tracked []Finding) {
	filed := map[string]bool{}
	for _, iss := range open {
		for _, sig := range markersIn(iss.Description) {
			filed[sig] = true
		}
	}
	for _, f := range found {
		if filed[f.Rule] {
			tracked = append(tracked, f)
			continue
		}
		fresh = append(fresh, f)
	}
	return fresh, tracked
}

// markersIn pulls every signature out of an issue description. More than one
// because a person consolidating two proposals into one ticket should not
// have the second re-proposed at them the next night.
func markersIn(desc string) []string {
	var out []string
	for _, line := range strings.Split(desc, "\n") {
		rest, ok := strings.CutPrefix(strings.TrimSpace(line), Marker)
		if !ok {
			continue
		}
		if sig := strings.TrimSpace(rest); sig != "" {
			out = append(out, sig)
		}
	}
	return out
}

// OpenIssues fetches the project's unfinished issues for the dedupe, and
// reports whether the cap cut the list short.
func OpenIssues(t Open, projectKey string) (issues []tracker.Issue, truncated bool, err error) {
	if t == nil || projectKey == "" {
		return nil, false, nil
	}
	jql := tracker.JQLAnd(tracker.JQLEq("project", projectKey), tracker.JQLNotDone())
	issues, err = t.Search(jql, MaxOpenScanned)
	if err != nil {
		return nil, false, err
	}
	return issues, len(issues) >= MaxOpenScanned, nil
}

// Draft renders one finding as a ticket a person can file by hand.
//
// The marker trailer is last and is the only machine-read part. Everything
// above it is written for the person deciding whether this is worth filing at
// all -- which is why Why comes before the evidence: the evidence says what
// happened, and Why says why that is not Orion working correctly, and only
// the second one answers the question they are actually asking.
func Draft(key string, f Finding) (title, body string) {
	title = f.Title
	var b strings.Builder
	if f.Novel {
		b.WriteString("Proposed by the post-run triage pass as a pattern no rule recognises. " +
			"A rule cannot be wrong about what it saw; this can, so read the evidence first.\n\n")
	} else {
		b.WriteString("Proposed by the post-run triage pass.\n\n")
	}
	if key != "" {
		fmt.Fprintf(&b, "Seen while working %s.\n\n", key)
	}
	b.WriteString("WHY THIS IS NOT ORION DEGRADING ON PURPOSE\n")
	b.WriteString(wrap(f.Why, 76) + "\n\n")
	b.WriteString("EVIDENCE (from the event log)\n")
	for _, e := range f.Evidence {
		b.WriteString("  " + e + "\n")
	}
	b.WriteString("\nNobody filed this. It was proposed, not created.\n")
	fmt.Fprintf(&b, "\n%s %s\n", Marker, f.Rule)
	return title, b.String()
}

// Report renders the whole pass for a person to read in one screen.
//
// Plain text, no colour and no box drawing, the same argument internal/report
// makes: the same bytes have to be readable in a terminal, in a cron mail and
// in a Slack message.
type Report struct {
	Key string
	// Fresh are the findings with no open ticket; Tracked are the ones that
	// already have one and are reported as a count, not re-proposed.
	Fresh, Tracked []Finding
	// Scanned is how many events the pass read, so a suspiciously empty
	// report can be told from an empty log.
	Scanned int
	// DedupeNote explains any reason the dedupe is less than complete: the
	// tracker was unreachable, or the open-issue scan hit its cap. Empty
	// means the dedupe was whole.
	DedupeNote string
	// AgentNote says what the agent did, or why it was not started.
	AgentNote string
}

func (r Report) Text() string {
	var b strings.Builder
	fmt.Fprintf(&b, "orion aiops  %s  (%d events read)\n", r.Key, r.Scanned)
	if r.DedupeNote != "" {
		// Said up front. A reader who does not know the dedupe was partial
		// will read a re-proposal as a new defect.
		fmt.Fprintf(&b, "dedupe: %s\n", r.DedupeNote)
	}
	if r.AgentNote != "" {
		fmt.Fprintf(&b, "agent: %s\n", r.AgentNote)
	}

	if len(r.Fresh) == 0 {
		b.WriteString("\nnothing worth filing.\n")
	} else {
		fmt.Fprintf(&b, "\nWORTH FILING (%d)\n", len(r.Fresh))
		for _, f := range r.Fresh {
			fmt.Fprintf(&b, "  [%s] %s\n", f.Rule, f.Title)
		}
	}
	if len(r.Tracked) > 0 {
		fmt.Fprintf(&b, "\nALREADY TRACKED (%d, not proposed again)\n", len(r.Tracked))
		for _, f := range r.Tracked {
			fmt.Fprintf(&b, "  [%s] %s\n", f.Rule, f.Title)
		}
	}

	for _, f := range r.Fresh {
		title, body := Draft(r.Key, f)
		b.WriteString("\n" + strings.Repeat("-", 72) + "\n")
		fmt.Fprintf(&b, "DRAFT TICKET  %s\n\n%s\n", title, body)
	}
	if len(r.Fresh) > 0 {
		b.WriteString("\nNone of these were created. File the ones you want, keeping the\n" +
			Marker + " line, and this pass will not propose them again.\n")
	}
	return b.String()
}

// Sort orders findings for reading: rules first, then the agent's proposals,
// oldest first within each.
//
// Rules before proposals because a rule cannot be wrong about what it saw and
// the agent can, so the checkable findings should be what a reader meets
// first. It also stops a proposal carrying no timestamp from sorting to the
// top of the report on the strength of a zero time.
func Sort(f []Finding) {
	sort.SliceStable(f, func(i, j int) bool {
		if f[i].Novel != f[j].Novel {
			return !f[i].Novel
		}
		return f[i].At.Before(f[j].At)
	})
}

func filter(evs []events.Event, keep func(events.Event) bool) []events.Event {
	var out []events.Event
	for _, e := range evs {
		if keep(e) {
			out = append(out, e)
		}
	}
	return out
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

// wrap breaks prose at a column so a draft pasted into a tracker does not
// arrive as one unreadable line.
func wrap(s string, width int) string {
	var b strings.Builder
	col := 0
	for i, word := range strings.Fields(s) {
		switch {
		case i == 0:
			col = len(word)
		case col+1+len(word) > width:
			b.WriteString("\n")
			col = len(word)
		default:
			b.WriteString(" ")
			col += 1 + len(word)
		}
		b.WriteString(word)
	}
	return b.String()
}
