// Package conform answers the third question about a finished change: is
// this the thing we agreed to build?
//
// Three readers already look at a change and none of them asks it.
//
//	the review class     reads the DIFF: is this code good
//	QA (OR-126)          reads the ACCEPTANCE CRITERIA: does it do what the
//	                     ticket said
//	done triage (OR-244) reads the same criteria against the diff: is it
//	                     genuinely finished, or does it only look finished
//
// All three can pass on a change that is well written, tested, satisfies its
// ticket, and quietly builds something other than what was agreed during
// planning. That is the expensive failure, because it is the one nobody is
// looking for: every status is green and the divergence is only visible to
// somebody who reads the plan and the diff side by side, which is exactly
// what nobody does at approval time.
//
// THE PLAN ARTIFACT IS THE ONLY THING IT COMPARES AGAINST. Not the ticket --
// QA and done triage own that ground, and a third pass re-deriving the same
// verdict would be two-thirds cost for no new information. Not style, not
// tests, not whether the code is good. One question, one source of truth.
//
// AND ONLY A CONFIRMED ONE. This is why the pass depends on internal/decide:
// before that package there was no artifact in this repository that a later
// stage could treat as agreed, because a recommendation nobody answered and a
// decision somebody made were the same bytes in the same directory. Checking
// conformance against an unconfirmed proposal would enforce a plan nobody
// approved, which is worse than not checking at all.
//
// IT REPORTS. It never blocks, never merges, never hands a ticket back, and
// never edits anything -- the same posture QA has (OR-126: findings go to a
// person, the pull request still opens). That is not timidity, it is the
// point: a divergence is frequently an IMPROVEMENT the implementer found
// while building, and a gate that stopped the pipeline for one would be
// switched off within a week. What must not happen is that it lands
// unremarked. So the verdict below has no boolean a caller could gate on;
// there is a report, and a person reads it.
//
// NOTHING TO CONFORM TO IS A STATED RESULT, NOT A PASS. A ticket with no
// confirmed plan artifact is not one that matches its plan; it is one with no
// plan, and the two are different facts. Reporting the first as the second
// would make the pass look like it ran on every ticket while it silently ran
// on almost none.
package conform

import "strings"

// The reply contract. One prefix per answer, so a verdict can be told apart
// from a model explaining itself. Free prose would mean pattern-matching
// phrasing to decide whether it said yes, which is the misread
// internal/work/qa.go spent a whole file learning to avoid.
const (
	ReplyConforms = "CONFORMS"
	ReplyDiverges = "DIVERGES:"
)

// MaxDivergences is how many findings a reply may contribute.
//
// ConformPrompt already tells the model AT MOST THREE, and gives the reason:
// past three it is listing differences rather than judging them, and none of
// them will be read. That instruction is a request, not a guarantee -- the
// one thing a prompt cannot do is bind the model that receives it -- so the
// ceiling is enforced here as well. Stated in the prompt AND enforced in the
// parser, both: dropping either leaves the contract in one place and the
// behaviour in the other.
//
// The FIRST three are kept. A model asked for its most important findings
// writes them first, and a cap that kept the tail would report the ones it
// thought least of.
const MaxDivergences = 3

// Source is one confirmed plan artifact, as it was read.
//
// Path is carried beside the text because it is what goes into the audit
// record: "diverges from the plan" is unactionable, and "diverges from
// docs/recommendations/confirmed/OR-158.md" is something a person can open.
type Source struct {
	Path string
	Text string
	// Truncated says Text is not the whole file, so a clause the model
	// cannot find may simply have been cut off.
	Truncated bool
}

// Diff is what the branch actually carries. Only the model reads it.
type Diff struct {
	Stat  string
	Patch string
	// Truncated has the same meaning it has on Source.
	Truncated bool
	// Unreadable is why the diff could not be read at all. Set means the two
	// fields above are empty and prove nothing.
	Unreadable string
}

// Evidence is what the question is put on. Every field is something a caller
// read from disk or from git; nothing here is inferred.
type Evidence struct {
	Key string
	// Plan is the confirmed plan artifacts, in the order they are worth
	// reading. Empty means there is nothing to conform to.
	Plan []Source
	// NoPlan says why Plan is empty, in words a person can act on. Reported
	// rather than dropped: see the package comment.
	NoPlan string
	Diff   Diff
}

// Divergence is one place the change and the plan disagree.
//
// Evidence is not decoration, for the reason done.Finding gives: a report a
// person cannot check is one they learn to skim, and this pass has no other
// consequence than being read.
type Divergence struct {
	// What the change does that the plan did not say to do, or the other way
	// round, in one sentence.
	What string
	// Evidence is what that rests on: the plan's own words, the part of the
	// diff that departs from them.
	Evidence []string
}

// Verdict is the answer.
//
// DELIBERATELY WITHOUT A PASS/FAIL FIELD. A boolean here would be a boolean
// somebody eventually gates on, and the moment this pass can stop a pipeline
// it stops being something people leave switched on. Diverged() exists to
// decide what to WRITE, never whether to proceed.
type Verdict struct {
	// Reviewed says the question was actually put to a model and answered.
	// False with no divergences means the pass did not run, which reads
	// identically to "it ran and found nothing" unless it is recorded.
	Reviewed    bool
	Divergences []Divergence
	// Note is why it did not run, or what became of the reply.
	Note string
	// Plan is the artifacts the answer was given against, for the audit
	// record. A divergence naming no plan is one nobody can re-check.
	Plan []string
}

// Diverged reports whether anything was found. What to write, not whether to
// continue.
func (v Verdict) Diverged() bool { return len(v.Divergences) > 0 }

// Asker puts the question and returns the model's reply verbatim.
//
// The same seam internal/done and internal/advise use. Nil means the pass
// does not run and says so; there is no rule-based half here, because
// "does this build what we agreed" is the question no rule expresses -- that
// is the entire reason this pass exists rather than being another check in
// internal/done.
type Asker func(ev Evidence) (string, error)

// Review compares the change against the confirmed plan and reports.
//
// Every early return is a STATED non-answer rather than a quiet pass. The
// value of this pass is entirely in what it writes down, so a run that could
// not reach a verdict has to leave a different record from one that reached
// "no divergence" -- otherwise the audit trail an auditor reads months later
// says the change was checked when it was not.
func Review(ev Evidence, ask Asker) Verdict {
	plan := paths(ev.Plan)
	if len(ev.Plan) == 0 {
		why := strings.TrimSpace(ev.NoPlan)
		if why == "" {
			why = "no confirmed plan artifact was found for this ticket"
		}
		return Verdict{Note: "not reviewed: " + why}
	}
	if ev.Diff.Unreadable != "" {
		return Verdict{Plan: plan,
			Note: "not reviewed: the diff could not be read (" + ev.Diff.Unreadable + ")"}
	}
	if strings.TrimSpace(ev.Diff.Patch) == "" {
		return Verdict{Plan: plan,
			Note: "not reviewed: the branch's diff came back empty, so there is " +
				"nothing to compare the plan against"}
	}
	if ask == nil {
		return Verdict{Plan: plan,
			Note: "not reviewed: no model was configured for this pass"}
	}

	out, err := ask(ev)
	if err != nil {
		return Verdict{Plan: plan, Note: "could not be asked (" + err.Error() + ")"}
	}
	found, ok := ParseReply(out)
	if !ok {
		// Not a divergence. A reply nobody could parse carries no evidence,
		// and reporting one on a formatting accident would put a claim on the
		// ticket that nobody can check -- which is the failure mode this
		// package's report is built to avoid.
		return Verdict{Reviewed: true, Plan: plan,
			Note: "the reply named neither " + ReplyConforms + " nor " + ReplyDiverges +
				", so the conformance question went unanswered: " + truncate(out, 200)}
	}
	if len(found) == 0 {
		return Verdict{Reviewed: true, Plan: plan,
			Note: "the change matches the confirmed plan"}
	}
	return Verdict{Reviewed: true, Plan: plan, Divergences: found,
		Note: "the change departs from the confirmed plan"}
}

// ParseReply reads the model's answer.
//
// Returns (nil, true) for CONFORMS, the divergences for DIVERGES:, and
// ok=false for a reply that is neither. Tolerant of a model that explains
// itself first, because that happens; strict about the prefix, because the
// prefix is the only thing separating a verdict from a paragraph about one.
//
// DIVERGES WINS OVER CONFORMS when a reply contains both. A model that named
// a specific departure and then wrote CONFORMS has still named one, and the
// named part is what a person can check.
//
// CAPPED AT MaxDivergences, silently. The prompt asks for at most three and
// says why; this is where that holds even when the model ignores it. Silent
// because the alternative -- a note saying findings were dropped -- would
// send a reader looking for a longer list that, by the reasoning behind the
// cap, was not worth reading in the first place.
func ParseReply(out string) ([]Divergence, bool) {
	var found []Divergence
	var conforms bool
	for _, raw := range strings.Split(out, "\n") {
		if len(found) >= MaxDivergences {
			// Keep scanning would only add findings this drops. Stopping
			// here also means a CONFORMS after the third divergence cannot
			// flip anything, which is already the rule below.
			break
		}
		line := strings.TrimSpace(raw)
		// The decoration a model puts in front of a line -- a bullet, a bold
		// marker -- must not hide the verdict behind it. Same rule
		// internal/work/qa.go applies to its sentinels.
		line = strings.TrimLeft(line, "*#->_ ")
		switch {
		case strings.HasPrefix(line, ReplyDiverges):
			why := strings.TrimSpace(strings.TrimPrefix(line, ReplyDiverges))
			if why == "" {
				// A divergence with no reason attached is exactly the
				// unreadable finding this package refuses to produce.
				continue
			}
			found = append(found, Divergence{What: why})
		case line == ReplyConforms:
			conforms = true
		}
	}
	if len(found) > 0 {
		return found, true
	}
	return nil, conforms
}

// Report renders the verdict for a tracker comment and the console.
//
// It opens by saying nothing is blocked. A reader who meets a list of
// findings without that sentence assumes the pipeline has stopped, goes
// looking for what to unblock, and finds nothing -- and the next time this
// pass reports they read it as noise.
func (v Verdict) Report() string {
	var b strings.Builder
	switch {
	case v.Diverged():
		b.WriteString("DIVERGES FROM THE CONFIRMED PLAN.\n")
	case v.Reviewed:
		b.WriteString("CONFORMS to the confirmed plan.\n")
	default:
		b.WriteString("NOT REVIEWED against a confirmed plan.\n")
	}
	if len(v.Plan) > 0 {
		b.WriteString("\nchecked against:\n")
		for _, p := range v.Plan {
			b.WriteString("    " + p + "\n")
		}
	}
	for _, d := range v.Divergences {
		b.WriteString("\n  " + d.What + "\n")
		for _, e := range d.Evidence {
			b.WriteString("    " + e + "\n")
		}
	}
	if v.Note != "" {
		b.WriteString("\nthe conformance check: " + v.Note + "\n")
	}
	if v.Diverged() {
		b.WriteString("\nNothing is blocked by this and nothing was changed. A divergence " +
			"may well be an improvement found while building; it is reported so that " +
			"a person decides, rather than it landing unremarked.\n")
	}
	return b.String()
}

// Summary is the one line the console and the event log carry.
func (v Verdict) Summary() string {
	switch {
	case v.Diverged():
		return "diverges from the confirmed plan"
	case v.Reviewed:
		return "conforms to the confirmed plan"
	default:
		return "not reviewed"
	}
}

// Whats renders the findings as plain strings, for the audit record's Detail
// map -- so the event log carries what was found and not merely that
// something was.
func (v Verdict) Whats() []string {
	out := make([]string, 0, len(v.Divergences))
	for _, d := range v.Divergences {
		out = append(out, d.What)
	}
	return out
}

func paths(src []Source) []string {
	if len(src) == 0 {
		return nil
	}
	out := make([]string, 0, len(src))
	for _, s := range src {
		out = append(out, s.Path)
	}
	return out
}

func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
