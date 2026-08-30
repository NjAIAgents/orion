package discovery

import (
	"bufio"
	"fmt"
	"io"
	"strings"
	"unicode"
)

// The interview is the one synchronous, Socratic exchange in Orion.
//
// Everything Assess exists for is a consequence of the opposite situation:
// intent, spec and plan run through `claude -p`, nobody is present, and an
// ambiguous premise can therefore only be caught after the fact by blocking
// on the artifact. `orion new` is the exception -- a human typed the idea and
// is still at the terminal -- so the ambiguity is removed by asking rather
// than by gating, which is the cheap way round and the only place it is
// available.
//
// What it asks is not arbitrary. It is exactly what NeedsDiscovery reports
// missing from a thin idea (who it is for, what is out of scope, what success
// means) plus the problem being solved, so the interview answers the same
// question the heuristic raised rather than a different one.
//
// A blank answer is NOT dropped. It is carried into the description as an
// open question, where Assess's existing gate finds it later. An omission
// that vanished here would be an invention made downstream.

// section is one question and the heading its answer lands under in the
// project description.
type section struct {
	Heading string
	Prompt  string
}

// sections is the interview, in order. Four questions, because a fifth costs
// more goodwill than it buys: a person who abandons the exchange halfway
// leaves less behind than one who was asked less and answered it all.
var sections = []section{
	{"Who it is for", "Who is this for? Name the people who will use it."},
	{"The problem", "What problem does it solve, and why now?"},
	{"Out of scope", "What is explicitly NOT part of this?"},
	{"Success looks like", "How will you know it worked?"},
}

// Intake is the finished front half: a project with a finalised name and an
// elaborated description, which is what the tracker project is created with.
type Intake struct {
	Idea string
	Name string
	// Answers is parallel to sections. An entry that is empty, or missing
	// because the exchange ended early, is an unanswered question.
	Answers []string
}

// answer returns the i-th answer, tolerating a short or absent slice: an
// interview cut off by EOF has fewer answers than there are questions, and
// that is a normal ending rather than a corrupt Intake.
func (k Intake) answer(i int) string {
	if i >= len(k.Answers) {
		return ""
	}
	return strings.TrimSpace(k.Answers[i])
}

// OpenQuestions lists the prompts left unanswered, in the order they were
// asked.
func (k Intake) OpenQuestions() []string {
	var out []string
	for i, s := range sections {
		if k.answer(i) == "" {
			out = append(out, s.Prompt)
		}
	}
	return out
}

// Description renders the elaborated project description.
//
// The shape is deliberately the one Assess already parses. This text becomes
// the tracker project's description and is the only artifact carrying the
// idea across into the non-interactive stages, so anything left open has to
// be legible to the gate that will read it there -- not merely legible to a
// person.
func (k Intake) Description() string {
	var b strings.Builder
	b.WriteString(strings.TrimSpace(k.Idea))
	b.WriteString("\n")
	for i, s := range sections {
		if a := k.answer(i); a != "" {
			fmt.Fprintf(&b, "\n## %s\n\n%s\n", s.Heading, a)
		}
	}
	b.WriteString("\n## Open questions\n\n")
	open := k.OpenQuestions()
	if len(open) == 0 {
		b.WriteString("- None\n")
		return b.String()
	}
	for _, q := range open {
		fmt.Fprintf(&b, "- %s\n", q)
	}
	return b.String()
}

// NameFromSlug turns a canonical slug back into a human project name.
//
// The slug is the input rather than the raw idea on purpose: per decision
// 0009 one canonical slug names the Jira project, the workspace and the git
// repo, so the name offered here is the same string those will carry, not a
// second derivation from the idea that happens to look similar.
func NameFromSlug(slug string) string {
	name := strings.TrimSpace(strings.ReplaceAll(slug, "-", " "))
	if name == "" {
		return ""
	}
	r := []rune(name)
	r[0] = unicode.ToUpper(r[0])
	return string(r)
}

// Interview conducts the exchange and returns the named, elaborated project.
//
// elaborate says whether to ask the four questions. The name is asked for
// either way: a detailed idea still arrives without a name anyone agreed to,
// and the name is the thing three separate systems are about to be called.
//
// EOF is not an error. Input can end early -- a piped run, a Ctrl-D, a
// person who has said enough -- and what was gathered up to that point is
// worth more than a refusal to return any of it.
func Interview(in io.Reader, out io.Writer, idea, proposedName string, elaborate bool) Intake {
	k := Intake{Idea: idea, Name: proposedName}
	r := bufio.NewReader(in)

	fmt.Fprintln(out, "Everything after this runs non-interactively, so this is the last")
	fmt.Fprintln(out, "point at which a question can be asked rather than guessed at.")
	fmt.Fprintln(out, "Enter alone leaves an answer open; it is recorded as an open question")
	fmt.Fprintln(out, "rather than silently dropped.")
	fmt.Fprintln(out)

	if elaborate {
		for _, s := range sections {
			fmt.Fprintf(out, "%s\n> ", s.Prompt)
			line, err := r.ReadString('\n')
			k.Answers = append(k.Answers, strings.TrimSpace(line))
			fmt.Fprintln(out)
			if err != nil {
				return k
			}
		}
	}

	fmt.Fprintf(out, "Project name -- this names the Jira project, the workspace and the repo.\n")
	fmt.Fprintf(out, "Enter keeps %q.\n> ", proposedName)
	line, err := r.ReadString('\n')
	if name := strings.TrimSpace(line); name != "" {
		k.Name = name
	}
	fmt.Fprintln(out)
	_ = err // EOF here just means the proposed name stands
	return k
}
