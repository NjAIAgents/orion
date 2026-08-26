// Package discovery decides whether an idea is ready to be designed from,
// and gates the chain until it is.
//
// The problem it fixes: the intent stage's prompt tells the agent to
// "interrogate the idea the way an analyst would", but that stage runs
// through `claude -p`, which is non-interactive. There was nobody to
// interrogate, so an ambiguous sentence propagated unchallenged into spec,
// plan, scaffold and a tracker tree. Each stage carries a token floor of
// roughly 30k, so a wrong premise costs nine floors plus the rework, against
// a discovery conversation costing about one.
//
// The gate deliberately mirrors require_plan_before_edit rather than
// inventing a second pattern: an artifact must be complete before the next
// stage may read it.
package discovery

import (
	"bufio"
	"fmt"
	"os"
	"regexp"
	"strings"
)

// Question is one thing the intent capture could not answer.
type Question struct {
	Text     string
	Answer   string
	Answered bool
}

// Assessment is what a captured intent looks like.
type Assessment struct {
	Path      string
	Found     bool
	Questions []Question
	Open      int
}

// Ready reports whether the chain may proceed past intent.
func (a Assessment) Ready() bool { return a.Found && a.Open == 0 }

// headingRe matches the Open questions section in any reasonable casing.
// /capture-intent fixes the shape of its file, but Orion should not break if
// a human edits the heading, so matching is lenient about case and wording.
var headingRe = regexp.MustCompile(`(?i)^#{1,6}\s*open\s+questions?\s*$`)

// answeredRe spots a question that has been resolved in place. Three forms
// are accepted because people will use all three: a ticked checkbox, a
// strikethrough, or an inline "Answer:".
var answeredRe = regexp.MustCompile(`(?i)(^\s*[-*]?\s*\[x\]|~~|(^|\s)answer\s*:)`)

// bulletRe matches a list item, with or without a checkbox.
var bulletRe = regexp.MustCompile(`^\s*[-*+]\s+(.*)$`)

// Assess reads a captured intent and counts what is still open.
//
// A missing file is not an error: it means intent has not run yet, which the
// caller reports differently from "ran and left questions".
func Assess(path string) Assessment {
	a := Assessment{Path: path}
	f, err := os.Open(path)
	if err != nil {
		return a
	}
	defer f.Close()
	a.Found = true

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	inSection := false
	for sc.Scan() {
		line := sc.Text()

		if headingRe.MatchString(strings.TrimSpace(line)) {
			inSection = true
			continue
		}
		// Any other heading closes the section. Without this, everything
		// after "Open questions" to end of file counts as a question.
		if inSection && strings.HasPrefix(strings.TrimSpace(line), "#") {
			inSection = false
			continue
		}
		if !inSection {
			continue
		}

		m := bulletRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		text := strings.TrimSpace(m[1])
		if text == "" {
			continue
		}
		// "None", "n/a" and friends are how people say there are no open
		// questions. Counting those as unanswered would block the chain on
		// an intent that is actually complete.
		if isNone(text) {
			continue
		}
		q := Question{Text: text, Answered: answeredRe.MatchString(line)}
		if !q.Answered {
			a.Open++
		}
		a.Questions = append(a.Questions, q)
	}
	return a
}

func isNone(s string) bool {
	switch strings.ToLower(strings.Trim(s, " .*_`")) {
	case "none", "n/a", "na", "none.", "nothing", "no open questions", "-":
		return true
	}
	return false
}

// NeedsDiscovery judges whether an idea is thin enough to be worth talking
// about first.
//
// The judgement is deliberately crude, and that is the point. A precise
// classifier would need a model call, which costs a token floor to decide
// whether to spend a token floor. Length and specificity are weak signals,
// but they are free, and the escape hatch covers the misses in both
// directions.
func NeedsDiscovery(idea string) (bool, string) {
	words := strings.Fields(idea)

	// A one-line idea cannot carry audience, scope, constraints and success
	// criteria. It is not necessarily bad, it is just underspecified.
	if len(words) < 12 {
		return true, "the idea is one short line; who it is for, what is out of scope and what success means are all unstated"
	}

	// A long idea that already names constraints and non-goals has had the
	// conversation somewhere else. Asking again is friction for no gain.
	low := strings.ToLower(idea)
	signals := 0
	for _, s := range []string{
		"must", "should not", "must not", "out of scope", "constraint",
		"success", "acceptance", "instead of", "without", "only if",
		"users", "customers", "so that", "because",
	} {
		if strings.Contains(low, s) {
			signals++
		}
	}
	if len(words) >= 40 && signals >= 3 {
		return false, "the idea already states constraints and intent in detail"
	}
	if signals >= 2 {
		return false, "the idea names constraints or a rationale"
	}
	return true, "the idea does not state constraints, scope or what success looks like"
}

// GateMessage is what a blocked stage prints. It names the questions rather
// than pointing at a file, because a block that makes you go and look is a
// block people learn to route around.
func (a Assessment) GateMessage(id string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "discovery gate: %d unanswered question(s) in %s\n\n", a.Open, a.Path)
	for _, q := range a.Questions {
		if q.Answered {
			continue
		}
		fmt.Fprintf(&b, "  - %s\n", q.Text)
	}
	b.WriteString("\n  Designing from an unanswered question means inventing the answer,\n")
	b.WriteString("  and every later stage inherits the invention.\n\n")
	fmt.Fprintf(&b, "  Answer them:   orion answer %s\n", id)
	fmt.Fprintf(&b, "  Or edit:       %s\n", a.Path)
	b.WriteString("  Mark an answer with [x], ~~strikethrough~~, or \"Answer: ...\".\n")
	return b.String()
}
