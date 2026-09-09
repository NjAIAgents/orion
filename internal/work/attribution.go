package work

import (
	"fmt"
	"os/exec"
	"strings"
)

// Reading the attribution trailer off the commits a run just made.
//
// WHY THIS IS HERE RATHER THAN LEFT TO THE TOOL. whodunit's hook stamps each
// commit with an AI-Attribution trailer and says nothing on the way past --
// it is a git hook, its output goes nowhere a supervised run can see. So the
// one record that says whether an agent's work was actually attributed lives
// only in the commit message, and nobody reads it until someone runs a report
// weeks later and finds a month of commits claiming no agent was involved.
//
// THE FAILURE IS SILENT AND LOOKS LIKE A FINDING (OR-193). When whodunit
// cannot find the transcript for a run it does not report an error: it sees a
// repository with no agent sessions and concludes, correctly from what it can
// see, that a human wrote the code. The commit is stamped `unassisted`. That
// is a POSITIVE CLAIM THAT NO AI WAS INVOLVED, made over work an agent wrote
// end to end, and it is indistinguishable in the data from the truth.
//
// Measured on this repository, 2026-09-08: 33 of orionbot's commits carry
// `unassisted`. The transcripts existed the whole time -- 156 session
// directories, 5,263 journal events -- and whodunit could not see one of them
// because Claude Code encodes a dot in a path as `-` and whodunit leaves it
// alone, so every sandbox under ~/.orion resolved to a directory that does
// not exist. The fix belongs upstream. This does not fix it; it makes it
// VISIBLE at the moment it happens, in the run that it happened to.

// attribution is what the trailer says about one run's commits.
type attribution struct {
	Status  string // intersected, observed, assisted, unassisted, unmatched, undetermined
	Missing int    // commits carrying no trailer at all
	Total   int
}

// attributionStatuses is the vocabulary, worst-first.
//
// Ordered by how wrong the claim is rather than alphabetically, because
// reportAttribution reports the WORST status among a run's commits and needs
// to know which that is. `unassisted` outranks `unmatched` deliberately:
// unmatched says "I could not tell", which is honest, while unassisted says
// "a human wrote this", which on an agent's commit is false.
var attributionStatuses = []string{
	"unassisted",   // claims no agent was involved -- false on an agent's commit
	"undetermined", // the tool was missing or could not run
	"unmatched",    // an agent was active, but touched none of these files
	"observed",     // an agent was active in the same files
	"assisted",     // an agent contributed, ratio known
	"intersected",  // the agent's edits are in this diff -- the answer we want
}

// readAttribution reads the AI-Attribution trailer from the commits a run
// added, newest first.
//
// Best-effort by contract: attribution is a record ABOUT the work, and a run
// that produced good code must not be failed because a trailer could not be
// read. Every failure here returns a zero value the caller reports as
// unknown rather than an error that stops anything.
func readAttribution(dir, base string, commits int) attribution {
	if commits <= 0 {
		return attribution{}
	}
	out, err := exec.Command("git", "-C", dir, "log",
		fmt.Sprintf("-%d", commits),
		"--format=%(trailers:key=AI-Attribution,valueonly)%x00").CombinedOutput()
	if err != nil {
		return attribution{}
	}

	var a attribution
	rank := len(attributionStatuses) // nothing seen yet
	for _, rec := range strings.Split(string(out), "\x00") {
		rec = strings.TrimSpace(rec)
		if rec == "" {
			continue
		}
		a.Total++
		s := trailerStatus(rec)
		if s == "" {
			// A commit with no trailer is not the same as one whose trailer
			// says it could not tell: it means the hook never ran here, which
			// is a different fault with a different fix.
			a.Missing++
			continue
		}
		for i, known := range attributionStatuses {
			if known == s && i < rank {
				rank, a.Status = i, s
			}
		}
	}
	// Commits the log did not account for carry no trailer either.
	if n := commits - a.Total; n > 0 {
		a.Missing += n
	}
	return a
}

// trailerStatus pulls status= out of one trailer value.
func trailerStatus(s string) string {
	for _, f := range strings.Split(s, ";") {
		f = strings.TrimSpace(f)
		if rest, ok := strings.CutPrefix(f, "status="); ok {
			return strings.TrimSpace(rest)
		}
	}
	return ""
}

// wrong reports whether this attribution states something untrue about an
// agent's work, as opposed to merely failing to state anything.
//
// Only `unassisted` qualifies. It is the one value that makes a positive
// claim -- that a human wrote the code -- and on a commit an agent produced
// that claim is false. Everything else is an absence of evidence, which is
// disappointing but honest.
func (a attribution) wrong() bool { return a.Status == "unassisted" }

// line is the human sentence for the status, or "" when there is nothing
// worth saying.
//
// An attributed commit gets no line. The reader is watching a run, not
// auditing a ledger, and a run that worked should not spend a line saying a
// background tool also worked.
func (a attribution) line() string {
	switch {
	case a.Missing > 0 && a.Status == "":
		return fmt.Sprintf("%d commit(s) carry no attribution trailer: the whodunit hook did not run here", a.Missing)
	case a.wrong():
		return "attributed `unassisted` -- whodunit found no agent session for this sandbox, " +
			"so an agent's work is recorded as a human's (OR-193)"
	case a.Status == "unmatched":
		return "attributed `unmatched`: an agent was active, but whodunit matched none of these files"
	case a.Status == "undetermined":
		return "attributed `undetermined`: whodunit could not run, so the commits claim no evidence either way"
	case a.Missing > 0:
		return fmt.Sprintf("%d of %d commit(s) carry no attribution trailer", a.Missing, a.Total)
	}
	return ""
}
