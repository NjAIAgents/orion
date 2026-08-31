// Package done answers one question about a finished run, at the moment
// before a person is asked to approve it: is this genuinely done, or does it
// only look done?
//
// A green pull request is not evidence that a ticket is done. On 2026-08-30 it
// was evidence of nothing three times, and each time a person caught it by
// reading the run against the diff rather than by reading the status:
//
//	OR-217  green and mergeable. The test that caught its off-by-one was
//	        written by QA and never committed, so CI tested a commit that did
//	        not contain it. Committing the test by hand turned it red at once.
//	OR-116  green with three passing checks. QA had crashed (claude exited 1),
//	        so the change went to review UNVERIFIED, and one of its test files
//	        had been destroyed rather than stranded.
//	OR-229  green. Its own test passed at -count=1 and failed at -count=2:
//	        under concurrency the fan paired answers with the wrong questions,
//	        and one run happened to schedule the goroutines the way the
//	        assertions expected.
//
// THE MECHANICAL CHECKS DECIDE MOST OF IT, AND THEY GO FIRST. All three cases
// above are visible in evidence that already exists -- an event QA emitted, a
// file sitting in a worktree, a flag on a test command -- so Check below is a
// pure function over that evidence and cannot hallucinate any of it. The
// model is asked ONLY when the rules are clean, which is both the cheap path
// and the honest one: paying a model to agree with a rule that already
// answered buys nothing.
//
// WHERE THE MODEL EARNS ITS PLACE is the one question no rule expresses --
// whether the change matches its intent. The interesting failures on
// 2026-08-30 were each novel, and the value of this pass is catching the class
// nobody has written a rule for yet.
//
// IT REPORTS. It never merges, never approves, never edits the change. NOT
// DONE hands the ticket back rather than holding a queue open, because a gate
// that stalls is a gate somebody switches off.
//
// NEVER A SCORE. Two values and no third. A number invites a threshold, and a
// threshold turns "is this done" into "is this done enough", which is the
// question nobody wanted asked.
package done

import (
	"strings"

	"github.com/orion-sdlc/orion/internal/events"
)

// Check identifiers. Stable, because they are written into the event log and
// into the comment a person reads on the ticket.
const (
	// CheckQAVerdict: the QA stage did not produce one, so the change went to
	// review unverified (OR-116).
	CheckQAVerdict = "qa-gave-no-verdict"
	// CheckStranded: a test exists in the worktree and not in the diff, so
	// the checks that went green ran on a commit without it (OR-217).
	CheckStranded = "tests-not-in-the-diff"
	// CheckRerun: the new or changed tests fail when run twice (OR-229).
	CheckRerun = "tests-fail-at-count-2"
	// CheckIntent: the model could not find the ticket's acceptance criteria
	// in the diff. The one check no rule expresses.
	CheckIntent = "criteria-not-in-the-diff"
)

// Finding is one reason a run is not done, with the evidence it rests on.
//
// Evidence is not decoration. The acceptance criterion is "NOT DONE with the
// specific evidence", and a hand-back a person cannot check is one they learn
// to wave through -- which is worse than not running this pass at all.
type Finding struct {
	// Check is which recogniser fired.
	Check string
	// What is the one-line statement of the problem.
	What string
	// Evidence is what it was concluded from: log lines, file names, the tail
	// of a failing run.
	Evidence []string
}

// Verdict is the answer.
type Verdict struct {
	// Done is the answer. Findings is empty exactly when it is true.
	Done     bool
	Findings []Finding
	// Judged records whether the model was asked at all, and Note says why
	// not, or what became of the reply. Both are reported: a pass whose model
	// half silently did not run reads exactly like one where it ran and found
	// nothing, and those are different facts.
	Judged bool
	Note   string
}

// Evidence is what a finished run left behind, gathered mechanically. Every
// field is something a caller read from the log, from git, or from a test
// command -- nothing here is inferred.
type Evidence struct {
	Key     string
	Summary string
	// Criteria is the ticket's own text: what the change was supposed to do.
	// Only the model reads it.
	Criteria string
	// Events are this ticket's events, already narrowed to the run that
	// worked it. See LastQARun.
	Events []events.Event
	Diff   Diff
	Rerun  Rerun
}

// Diff is what the branch actually carries, versus what is lying around
// beside it.
type Diff struct {
	// Files are the paths the branch changed against its base, as committed.
	Files []string
	// Stat is `git diff --stat`, for the model's prompt.
	Stat string
	// Patch is the diff text, possibly truncated. For the model only.
	Patch string
	// Truncated says Patch is not the whole diff, so a criterion the model
	// cannot find may simply have been cut off.
	Truncated bool
	// Unreadable is why the diff could not be read at all. Set means the
	// three fields above are empty and prove nothing.
	Unreadable string
	// Stranded are test files present in the job worktree that the branch
	// does not carry -- written, and then left where CI cannot see them.
	Stranded []string
}

// Rerun is what running the new or changed tests a second time produced.
type Rerun struct {
	// Packages is what was re-run, in the repository's own terms.
	Packages []string
	// Skipped is why nothing was re-run: no changed test files, no toolchain,
	// a language this cannot drive. Set means Failed is false and proves
	// nothing, which is why it is reported rather than dropped.
	Skipped string
	// Failed is true only when the re-run COMPLETED and something in it
	// failed. A run that could not start is Skipped, never Failed: handing a
	// ticket back because a checkout failed would be a worse fault than the
	// one this catches.
	Failed bool
	// Output is the tail of what it printed. The evidence for Failed.
	Output string
}

// Asker puts the one question the rules cannot answer -- does this diff do
// what the ticket asked for -- and returns the model's reply verbatim.
//
// Injected so the whole package is testable without spending anything, the
// same seam internal/advise uses. Nil means the model half does not run, and
// the rules stand on their own; that is a supported configuration, not a
// degraded one, because the rules are the part with recorded evidence behind
// them.
type Asker func(ev Evidence) (string, error)

// The reply contract. One line, one prefix, so a decision can be told from a
// model explaining itself. Free prose would mean pattern-matching a model's
// phrasing to decide whether it said yes.
const (
	ReplyDone    = "DONE"
	ReplyNotDone = "NOT DONE:"
)

// Triage returns the verdict for one finished run.
//
// The rules first, and the model only if they came back clean. Not an
// optimisation: a rule that has already found a stranded test file has
// answered the question, and a second opinion on an answered question is
// spend with no decision attached to it.
func Triage(ev Evidence, ask Asker) Verdict {
	if f := Check(ev); len(f) > 0 {
		return Verdict{Findings: f,
			Note: "not asked: the evidence already answered"}
	}
	if ask == nil {
		return Verdict{Done: true, Note: "not asked: no model was configured for this pass"}
	}

	out, err := ask(ev)
	if err != nil {
		return Verdict{Done: true, Note: "could not be asked (" + err.Error() +
			"); the mechanical checks above stand on their own"}
	}
	f, ok := ParseReply(out)
	if !ok {
		// Deliberately DONE rather than NOT DONE. The acceptance criterion is
		// "NOT DONE with the specific evidence", and a reply nobody could
		// parse carries none -- so a hand-back here would be a hand-back on a
		// formatting accident, which costs a person a ticket and teaches them
		// that this verdict is noise. The three checks with recorded evidence
		// behind them are unaffected.
		return Verdict{Done: true, Judged: true,
			Note: "the reply named neither " + ReplyDone + " nor " + ReplyNotDone +
				", so the intent question went unanswered: " + truncate(out, 200)}
	}
	if f == nil {
		return Verdict{Done: true, Judged: true,
			Note: "the acceptance criteria correspond to something in the diff"}
	}
	return Verdict{Judged: true, Findings: []Finding{*f},
		Note: "the acceptance criteria were not found in the diff"}
}

// Check applies every mechanical rule and returns what fired, in the order
// the rules are worth reading.
//
// Pure: no clock, no filesystem, no network, no model. Everything this
// concludes is reproducible from the Evidence struct, because the alternative
// is a verdict nobody can check.
func Check(ev Evidence) []Finding {
	var out []Finding
	if f := checkQAVerdict(ev); f != nil {
		out = append(out, *f)
	}
	if f := checkStranded(ev); f != nil {
		out = append(out, *f)
	}
	if f := checkRerun(ev); f != nil {
		out = append(out, *f)
	}
	return out
}

// qaStopped is the prefix internal/work writes when the QA stage ended without
// running to a verdict -- a crashed CLI, a wall clock, a quota wall.
const qaStopped = "QA stopped:"

// qaNoVerdict is the phrase internal/work writes when QA ran, finished, and
// its closing message said nothing that could be read as a verdict.
const qaNoVerdict = "never reported a verdict"

// checkQAVerdict answers the first question: did QA actually give a verdict,
// or did the stage fail and the change go to review unverified?
//
// POSITIVE EVIDENCE ONLY. It fires on an event QA or Orion actually wrote,
// never on the ABSENCE of QA events -- the event log rotates by size, QA can
// be switched off for a project, and a ticket worked before this pass existed
// has no such events at all. Reading silence as a failure would hand back
// good work for want of a log file, which is exactly the fault this pass is
// supposed to prevent in the other direction.
//
// This is OR-116: QA crashed, so the change went to review with nothing
// verified, and three green checks said nothing about that.
func checkQAVerdict(ev Evidence) *Finding {
	var lines []string
	for _, e := range ev.Events {
		switch {
		case e.Kind == events.KindQA && strings.Contains(e.Msg, qaStopped):
			lines = append(lines, Line(e))
		case e.Kind == events.KindEscalate && e.Actor == events.ActorQA &&
			strings.Contains(e.Msg, qaNoVerdict):
			lines = append(lines, Line(e))
		}
	}
	if len(lines) == 0 {
		return nil
	}
	return &Finding{
		Check: CheckQAVerdict,
		What: "QA did not reach a verdict on this change, so it is going to review " +
			"unverified. The checks being green says the build compiles and the " +
			"existing tests pass; it says nothing about whether anything verified " +
			"what this ticket changed.",
		Evidence: lines,
	}
}

// checkStranded answers the second: do the tests QA wrote appear in the diff,
// or only in the worktree it left behind?
//
// This is OR-217. The pull request was green and mergeable; the test that
// caught its off-by-one was sitting on disk beside it, uncommitted, so CI had
// tested a commit that did not contain it. Committing the test by hand turned
// the pull request red immediately -- which means the green was never about
// the change at all.
func checkStranded(ev Evidence) *Finding {
	if len(ev.Diff.Stranded) == 0 {
		return nil
	}
	return &Finding{
		Check: CheckStranded,
		What: "a test exists in this ticket's worktree that the branch does not " +
			"carry, so the checks that went green ran on a commit without it. " +
			"Committing it is what decides whether they still would.",
		Evidence: ev.Diff.Stranded,
	}
}

// checkRerun answers the fourth: do the new or changed tests still pass when
// they are run twice?
//
// This is OR-229. Its own test passed at -count=1 and failed at -count=2:
// under concurrency the fan paired answers with the wrong questions, and one
// scheduling happened to match what the assertions expected. A single green
// run of a concurrent test is a sample, not a result.
func checkRerun(ev Evidence) *Finding {
	if !ev.Rerun.Failed {
		return nil
	}
	evidence := []string{"re-ran at -count=2: " + strings.Join(ev.Rerun.Packages, " ")}
	if out := strings.TrimSpace(ev.Rerun.Output); out != "" {
		evidence = append(evidence, out)
	}
	return &Finding{
		Check: CheckRerun,
		What: "the tests this branch adds or changes fail when run twice, so the " +
			"green check was one scheduling rather than a result.",
		Evidence: evidence,
	}
}

// ParseReply reads the model's answer to the intent question.
//
// Returns (nil, true) for DONE, a finding for NOT DONE, and ok=false for a
// reply that is neither -- which Triage treats as unanswered rather than
// guessing at. Tolerant of a model that explains itself first, because that
// happens and discarding a real answer over a preamble helps nobody; strict
// about the prefix, because this is the one path that can hand work back.
func ParseReply(out string) (*Finding, bool) {
	var found *Finding
	var done bool
	for _, raw := range strings.Split(out, "\n") {
		line := strings.TrimSpace(raw)
		switch {
		case strings.HasPrefix(line, ReplyNotDone):
			why := strings.TrimSpace(strings.TrimPrefix(line, ReplyNotDone))
			if why == "" {
				// A hand-back with no reason attached is exactly the
				// unactionable verdict the acceptance criterion forbids.
				continue
			}
			found = &Finding{Check: CheckIntent,
				What:     "the ticket asks for something the diff does not contain.",
				Evidence: []string{why}}
		case line == ReplyDone:
			done = true
		}
	}
	// NOT DONE wins over DONE if a reply somehow contains both: a model that
	// named a missing criterion and then wrote DONE has still named one, and
	// the reason is the part a person can check.
	if found != nil {
		return found, true
	}
	return nil, done
}

// LastQARun narrows a workspace's events to the run that last worked this
// ticket, so a QA failure from an earlier attempt is not read as this one's.
//
// The run is identified by its Run id, and chosen as the LAST group that
// contains any QA-stage event. A ticket retried after a crash appends a second
// run to the same log; without this, the first run's "QA stopped" would be
// found forever, and every later attempt handed back for a failure that had
// already been fixed.
//
// A log with no QA events at all yields nothing, and checkQAVerdict then
// stays silent by construction -- see its comment on positive evidence.
func LastQARun(evs []events.Event, key string) []events.Event {
	want := ""
	for _, e := range evs {
		if !strings.EqualFold(e.Key, key) {
			continue
		}
		if e.Kind == events.KindQA || e.Actor == events.ActorQA {
			want = e.Run
		}
	}
	if want == "" {
		return nil
	}
	var out []events.Event
	for _, e := range evs {
		if e.Run == want && strings.EqualFold(e.Key, key) {
			out = append(out, e)
		}
	}
	return out
}

// Line renders one event as evidence, the way internal/aiops does: what
// happened, who did it, and when, on one line a person can find in the log.
func Line(e events.Event) string {
	at := ""
	if !e.At.IsZero() {
		at = e.At.Format("15:04:05") + "  "
	}
	return at + e.Actor + "  " + e.Msg
}

// Report renders the verdict for a tracker comment and the console.
//
// Findings first and in full. The verdict line on its own is a status, and a
// status is the thing this pass exists to distrust.
func (v Verdict) Report() string {
	var b strings.Builder
	if v.Done {
		b.WriteString("DONE.\n")
	} else {
		b.WriteString("NOT DONE.\n")
	}
	for _, f := range v.Findings {
		b.WriteString("\n" + f.Check + "\n  " + f.What + "\n")
		for _, e := range f.Evidence {
			b.WriteString("    " + e + "\n")
		}
	}
	if v.Note != "" {
		b.WriteString("\nthe intent check: " + v.Note + "\n")
	}
	return b.String()
}

// Summary is the one line the console and the event log carry.
func (v Verdict) Summary() string {
	if v.Done {
		return "done"
	}
	names := make([]string, 0, len(v.Findings))
	for _, f := range v.Findings {
		names = append(names, f.Check)
	}
	return "not done: " + strings.Join(names, ", ")
}

func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
