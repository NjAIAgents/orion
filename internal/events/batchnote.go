package events

// The batch note's format, written and read in ONE place (OR-258).
//
// It used to live in two: internal/collect built the sentence with a Printf,
// and internal/dashboard took it apart with a Sscanf that expected a shape the
// writer did not always produce. Run against this repository's own log on
// 2026-09-01 the parser matched nothing, so the dashboard reported "no batch
// has integrated yet" and an absent "CI runs saved" -- the number the whole
// batching design is justified by -- on a repository that had run batches.
//
// Neither side was obviously wrong to read. That is the point: a format
// agreed by two Printf strings in different packages is not an agreement, it
// is a coincidence that holds until someone edits a sentence. Here the
// builder and the parser sit next to each other and are tested against each
// other, so a change to the wording breaks a test rather than a dashboard.
//
// In internal/events rather than in either party because both import it
// already, and the alternative -- collect importing dashboard -- inverts the
// layering for no gain.

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// BatchNote is one batch's outcome, as the note recorded in the event log.
type BatchNote struct {
	Ref      string
	Runs     int
	Elapsed  time.Duration
	Landed   []string
	Ejected  []string
	Culprit  []string
	Deferred []string
	// Median and Samples are the per-branch baseline this batch is measured
	// against (OR-250). Zero samples means there was none yet.
	Median  time.Duration
	Samples int
}

const batchNotePrefix = "batch on "

// String renders the note. The only place this sentence is written.
func (n BatchNote) String() string {
	return fmt.Sprintf(
		"%s%s: %d run(s) in %s, landed=%v ejected=%v culprit=%v deferred=%v "+
			"(per-branch median %s over %d landing(s))",
		batchNotePrefix, n.Ref, n.Runs, dur(n.Elapsed),
		n.Landed, n.Ejected, n.Culprit, n.Deferred, dur(n.Median), n.Samples)
}

// batchNoteRe reads the fields the dashboard needs.
//
// A regexp rather than Sscanf, which is what broke: `%fm` against a duration
// parses "3m0s" by luck and fails on "45s" or "1h2m0s" outright, so the
// dashboard silently emptied itself for any batch that did not happen to take
// a whole number of minutes.
//
// The elapsed and baseline groups are optional so a note written before OR-250
// added elapsed still yields its run count and its members. A partial reading
// of a real batch beats discarding it.
var batchNoteRe = regexp.MustCompile(
	`^batch on (\S+): (\d+) run\(s\)(?: in (\S+?))?, landed=\[([^\]]*)\]`)

// ParseBatchNote reads a note back, reporting whether it was one.
func ParseBatchNote(msg string) (BatchNote, bool) {
	if !strings.HasPrefix(msg, batchNotePrefix) {
		return BatchNote{}, false
	}
	m := batchNoteRe.FindStringSubmatch(msg)
	if m == nil {
		return BatchNote{}, false
	}
	n := BatchNote{Ref: m[1], Landed: fields(m[4])}
	n.Runs, _ = strconv.Atoi(m[2])
	if m[3] != "" {
		n.Elapsed, _ = time.ParseDuration(m[3])
	}
	// The baseline is read separately: it is absent on a repository with no
	// history, and a note that carries no baseline is still a batch.
	if b := baselineRe.FindStringSubmatch(msg); b != nil {
		n.Median, _ = time.ParseDuration(b[1])
		n.Samples, _ = strconv.Atoi(b[2])
	}
	n.Ejected = bracketed(msg, "ejected=")
	n.Culprit = bracketed(msg, "culprit=")
	n.Deferred = bracketed(msg, "deferred=")
	return n, true
}

var baselineRe = regexp.MustCompile(`per-branch median (\S+) over (\d+) landing`)

// Members is every key the batch named, whatever became of it.
func (n BatchNote) Members() []string {
	out := append([]string{}, n.Landed...)
	out = append(out, n.Ejected...)
	out = append(out, n.Culprit...)
	return append(out, n.Deferred...)
}

func bracketed(msg, key string) []string {
	at := strings.Index(msg, key)
	if at < 0 {
		return nil
	}
	rest := msg[at+len(key):]
	if !strings.HasPrefix(rest, "[") {
		return nil
	}
	end := strings.Index(rest, "]")
	if end < 0 {
		return nil
	}
	return fields(rest[1:end])
}

func fields(s string) []string {
	f := strings.Fields(s)
	if len(f) == 0 {
		return nil
	}
	return f
}

// dur formats a duration the way the batch reports it, and "unknown" for a
// zero -- which is what an unmeasured baseline is, and must not be printed as
// "0s" as though it had been measured and found instant.
func dur(d time.Duration) string {
	if d <= 0 {
		return "unknown"
	}
	if d < time.Minute {
		return d.Round(time.Second).String()
	}
	return d.Round(time.Minute).String()
}
