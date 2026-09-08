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
	"errors"
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
	// Line is where the question sits in the file, 1-based: the bullet's
	// first line, or the line carrying the marker. It is what Answer
	// rewrites, so a caller answering several must re-assess between
	// writes -- a bullet answer inserts a line and shifts the rest.
	Line int
	// ID is the identifier the text opens with (OQ-12, Q3), if any: what
	// ties a bullet under Open questions to the marker in the body that
	// asks the same thing, so the two are one question and one answer.
	ID string
	// Markers are the lines carrying a marker for this same question,
	// when the question is a bullet. Answered together with it.
	Markers []int
}

// idRe is the identifier a question may open with. Letters, an optional
// dash, digits: OQ-12, Q3, NC-7.
var idRe = regexp.MustCompile(`^([A-Za-z]{1,4}-?\d+)\b`)

func questionID(text string) string {
	if m := idRe.FindStringSubmatch(strings.TrimSpace(text)); m != nil {
		return strings.ToUpper(m[1])
	}
	return ""
}

// Assessment is what a captured intent looks like.
type Assessment struct {
	Path      string
	Found     bool
	Questions []Question
	Open      int

	markerLines map[int]bool
	bulletLines map[int]bool
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

// markerRe matches spec-kit's own way of saying "undecided":
// [NEEDS CLARIFICATION: the question]. The same statement as an open bullet,
// in a different spelling, and it used to walk straight through this gate.
var markerRe = regexp.MustCompile(`(?i)\[NEEDS CLARIFICATION:?\s*([^\]]*)\]`)

// Assess reads a captured intent and counts what is still open.
//
// A missing file is not an error: it means intent has not run yet, which the
// caller reports differently from "ran and left questions".
func Assess(path string) Assessment { return assess(path, false) }

// AssessSpec reads a specification and counts what is still open: the
// bullets under Open questions, as Assess does, plus every
// [NEEDS CLARIFICATION: ...] marker anywhere in the document outside fenced
// code. A marker is answered only by removing it -- it sits in the sentence
// it qualifies, and the answer belongs in that sentence.
func AssessSpec(path string) Assessment { return assess(path, true) }

func assess(path string, markers bool) Assessment {
	a := Assessment{Path: path, markerLines: map[int]bool{}, bulletLines: map[int]bool{}}
	f, err := os.Open(path)
	if err != nil {
		return a
	}
	defer f.Close()
	a.Found = true

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	inSection := false
	inFence := false
	n := 0
	for sc.Scan() {
		line := sc.Text()
		n++

		if markers {
			if strings.HasPrefix(strings.TrimSpace(line), "```") {
				inFence = !inFence
				continue
			}
			if !inFence {
				for _, m := range markerRe.FindAllStringSubmatch(line, -1) {
					text := strings.TrimSpace(m[1])
					// A bare [NEEDS CLARIFICATION] with nothing after the
					// label is prose ABOUT markers -- a checklist line, an
					// instruction -- not a question. spec-kit's own
					// convention always carries the question inside.
					if text == "" {
						continue
					}
					a.Questions = append(a.Questions, Question{Text: text, Line: n, ID: questionID(text)})
					a.markerLines[n] = true
					a.Open++
				}
			}
		}

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
		// A checkbox bullet: the box is state, not question text.
		if strings.HasPrefix(text, "[ ]") {
			text = strings.TrimSpace(text[3:])
		} else if len(text) > 3 && strings.HasPrefix(strings.ToLower(text), "[x]") {
			text = strings.TrimSpace(text[3:])
		}
		q := Question{Text: text, Answered: answeredRe.MatchString(line), Line: n, ID: questionID(text)}
		a.bulletLines[n] = true
		if !q.Answered {
			a.Open++
		}
		a.Questions = append(a.Questions, q)
	}
	if markers {
		a.merge()
	}
	return a
}

// merge folds a marker and a bullet that ask the same question -- same
// identifier, or the same text -- into one: the bullet, which is where a
// person answers, carrying the marker's line so the answer reaches both.
// A marker with no bullet stays a question of its own, answered only by
// removal. Open is recounted from what survives.
func (a *Assessment) merge() {
	var bullets []*Question
	byKey := map[string]*Question{}
	for i := range a.Questions {
		q := &a.Questions[i]
		if len(q.Markers) == 0 && q.Line > 0 && !isMarkerQuestion(a, q) {
			bullets = append(bullets, q)
			byKey[keyOf(*q)] = q
		}
	}
	if len(bullets) == 0 {
		return
	}
	var kept []Question
	for _, q := range a.Questions {
		if isMarkerQuestion(a, &q) {
			if b, ok := byKey[keyOf(q)]; ok {
				b.Markers = append(b.Markers, q.Line)
				continue
			}
		}
		kept = append(kept, q)
	}
	// The pointers above addressed a.Questions; rebuild from bullets'
	// updated copies.
	out := make([]Question, 0, len(kept))
	for _, q := range kept {
		if b, ok := byKey[keyOf(q)]; ok && b.Line == q.Line {
			out = append(out, *b)
		} else {
			out = append(out, q)
		}
	}
	a.Questions = out
	a.Open = 0
	for _, q := range a.Questions {
		if !q.Answered {
			a.Open++
		}
	}
}

// isMarkerQuestion tells a marker-born question from a bullet-born one by
// where it sits: bullets live under the Open questions heading, markers
// anywhere else. Recorded during the scan rather than inferred.
func isMarkerQuestion(a *Assessment, q *Question) bool {
	return a.markerLines[q.Line] && !a.bulletLines[q.Line]
}

func keyOf(q Question) string {
	if q.ID != "" {
		return "id:" + q.ID
	}
	return "text:" + strings.ToLower(strings.TrimSpace(q.Text))
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

// Answer writes one answer into the file, in the form the parser reads back
// as answered: a bullet gets `[x]` and an indented `Answer: ...` line after
// its continuation lines; a [NEEDS CLARIFICATION: ...] marker is replaced by
// the decision, in the sentence it qualified. The file is the record every
// later stage reads, so the answer goes there and nowhere else.
func Answer(path string, q Question, text string) error {
	text = strings.TrimSpace(text)
	if text == "" {
		return errors.New("an empty answer answers nothing")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	lines := strings.Split(string(b), "\n")
	if q.Line < 1 || q.Line > len(lines) {
		return fmt.Errorf("line %d is not in %s (re-read the file: an earlier answer may have moved it)", q.Line, path)
	}
	i := q.Line - 1
	line := lines[i]

	// A marker on this line whose text is the question: replace that span.
	for _, m := range markerRe.FindAllStringSubmatchIndex(line, -1) {
		if strings.TrimSpace(line[m[2]:m[3]]) == q.Text {
			lines[i] = line[:m[0]] + text + line[m[1]:]
			return os.WriteFile(path, []byte(strings.Join(lines, "\n")), 0o644)
		}
	}

	// Otherwise a bullet under Open questions. Its body markers first: a
	// same-line edit shifts nothing, and the Answer line inserted below
	// would move every line after the bullet.
	for _, ml := range q.Markers {
		if ml < 1 || ml > len(lines) {
			continue
		}
		l := lines[ml-1]
		for _, m := range markerRe.FindAllStringSubmatchIndex(l, -1) {
			if questionID(l[m[2]:m[3]]) == q.ID && q.ID != "" || strings.TrimSpace(l[m[2]:m[3]]) == q.Text {
				l = l[:m[0]] + text + l[m[1]:]
				break
			}
		}
		lines[ml-1] = l
	}
	m := bulletRe.FindStringSubmatch(line)
	body := strings.TrimSpace(m1(m))
	body = strings.TrimSpace(strings.TrimPrefix(body, "[ ]"))
	if m == nil || body != q.Text && !strings.HasPrefix(body, q.Text) {
		return fmt.Errorf("line %d of %s is no longer the question %q", q.Line, path, q.Text)
	}
	switch {
	case strings.Contains(line, "[ ]"):
		lines[i] = strings.Replace(line, "[ ]", "[x]", 1)
	case !strings.HasPrefix(strings.ToLower(strings.TrimSpace(m[1])), "[x]"):
		k := strings.IndexAny(line, "-*+")
		lines[i] = line[:k+1] + " [x]" + line[k+1:]
	}
	// Past the bullet's own continuation lines: indented, non-empty, not
	// a bullet themselves.
	j := i + 1
	for j < len(lines) {
		l := lines[j]
		if strings.TrimSpace(l) == "" || bulletRe.MatchString(l) || !(strings.HasPrefix(l, " ") || strings.HasPrefix(l, "\t")) {
			break
		}
		j++
	}
	indent := line[:len(line)-len(strings.TrimLeft(line, " \t"))] + "  "
	lines = append(lines[:j], append([]string{indent + "Answer: " + text}, lines[j:]...)...)
	return os.WriteFile(path, []byte(strings.Join(lines, "\n")), 0o644)
}

func m1(m []string) string {
	if len(m) < 2 {
		return ""
	}
	return m[1]
}
