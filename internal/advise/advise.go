// Package advise answers a question the implementer could not resolve.
//
// The loop this automates, described by the person who was doing it by hand:
// Claude Code works a ticket, hits an ambiguity, stops and asks; the question
// is carried to the model that designed the project; that model says which
// option to take; the answer is carried back. Orion does the carrying.
//
// The answerer is not the supervisor. A process controller has no knowledge
// the implementer lacks. The answerer is a second agent that holds the
// DESIGN -- and the design is not a chat history, it is intent.md, spec.md
// and plan.md, committed in the repository. That is what makes this
// automatable at all: the context the human was supplying from a ChatGPT
// conversation is already on disk.
//
// Two roles, because two different documents decide two different questions:
//
//	architect  reads spec.md and plan.md   "how should this be built"
//	pm         reads intent.md              "what are we building, and why"
//
// One rule binds both: DERIVE FROM THE ARTIFACT, CITE IT, OR REFUSE.
//
// Refusal is a success. If an advisor invents an answer, the invention now
// carries a citation-shaped wrapper and reads as authoritative -- two agents
// in sequence launder a guess into something a reviewer will not question.
// The honest outcome when the artifact is silent is that the artifact is
// INCOMPLETE, which is a human's decision and then an amendment to the
// document, so the next ticket does not re-ask.
package advise

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Role is who is being asked.
type Role string

const (
	RoleArchitect Role = "architect"
	RolePM        Role = "pm"
	RoleHuman     Role = "human"
)

// Verdict is what came back.
type Verdict string

const (
	// Derived: the answer is IN the artifacts and the advisor found it.
	VerdictDerived Verdict = "derived"
	// Refused: the artifacts are silent. A human decides, and the artifact
	// should then be amended.
	VerdictRefused Verdict = "refused"
	// Escalate: the question is not this advisor's to answer.
	VerdictEscalate Verdict = "escalate"
)

// Answer is an advisor's response.
type Answer struct {
	Role      Role    `json:"role"`
	Verdict   Verdict `json:"verdict"`
	Decision  string  `json:"decision"`
	Grounding string  `json:"grounding"` // the clause it was derived from
	Reason    string  `json:"reason"`    // why it refused or escalated
	Model     string  `json:"model"`
}

// Answered reports whether the implementer can continue on this.
func (a Answer) Answered() bool {
	return a.Verdict == VerdictDerived && strings.TrimSpace(a.Decision) != ""
}

// Models. Configurable, but these defaults are reasoned:
//
// The advisors are Sonnet because their task is constrained -- read three
// documents, decide, cite the clause, or refuse. That is careful reading
// under a citation requirement, not open-ended design; the design already
// happened and is on disk. Latency decides it: the implementer sits idle
// while the advisor thinks, burning the wall clock the supervisor is about
// to kill it on, so a slower advisor inflates the cost of the primary run.
//
// The router is Haiku because classification into a closed set is its shape,
// and because misrouting is self-correcting: send a product question to the
// architect and it refuses for lack of grounding, so the supervisor forwards
// it to the PM. One wasted cheap call.
//
// Escalating to a stronger model when an advisor REFUSES would be exactly
// backwards. Refusal means the artifact is silent; a stronger model is not
// more likely to find something that is not there, only more likely to
// produce a confident answer anyway.
const (
	ModelAdvisor = "sonnet"
	ModelRouter  = "haiku"
)

// Runner executes one read-only agent turn and returns its final message.
// Injected so the whole package is testable without spending anything.
type Runner func(dir, model, prompt string) (string, error)

// Ask puts a question to a role and parses the verdict.
func Ask(run Runner, dir string, role Role, question string, artifacts []string) (Answer, error) {
	prompt := promptFor(role, question, artifacts)
	out, err := run(dir, ModelAdvisor, prompt)
	if err != nil {
		return Answer{Role: role}, err
	}
	a := parse(out)
	a.Role = role
	a.Model = ModelAdvisor
	return a, nil
}

// Route decides which advisor a question belongs to.
//
// Falls back to the architect on any doubt, because the architect's refusal
// is cheap and informative, whereas a product question answered by the wrong
// advisor is only wrong if that advisor answers it -- and it is instructed
// not to.
func Route(run Runner, dir, question string) Role {
	out, err := run(dir, ModelRouter, routePrompt(question))
	if err != nil {
		return RoleArchitect
	}
	switch {
	case strings.Contains(strings.ToLower(out), "product"):
		return RolePM
	default:
		return RoleArchitect
	}
}

// Artifacts lists the design documents a role is allowed to reason from.
//
// Deliberately narrow. Giving the architect intent.md as well would let it
// answer a product question by reading the goals and inferring what someone
// probably wanted, which is the laundering this package exists to prevent.
func Artifacts(dir string, role Role) []string {
	var want []string
	switch role {
	case RoleArchitect:
		want = []string{"spec.md", "plan.md", "docs/decisions"}
	case RolePM:
		want = []string{"intent.md", "docs/decisions"}
	}
	var out []string
	for _, rel := range want {
		if _, err := os.Stat(filepath.Join(dir, rel)); err == nil {
			out = append(out, rel)
		}
	}
	return out
}

func routePrompt(question string) string {
	return strings.Join([]string{
		"Classify this question as TECHNICAL or PRODUCT. Answer with one word.",
		"",
		"TECHNICAL: how something should be built, structured, named, or",
		"sequenced. Decidable from an architecture specification.",
		"",
		"PRODUCT: what the software should do for whom, what is in or out of",
		"scope, what a business rule ought to be. Decidable only from a",
		"statement of intent, or by a person.",
		"",
		"The question:",
		question,
	}, "\n")
}

// promptFor builds the advisor's instruction.
//
// The shape of the answer is fixed JSON so the supervisor can act on it
// without guessing. Free prose would mean pattern-matching a model's
// phrasing to decide whether it actually answered, and "it depends, but
// probably by issuer" would read as a decision.
func promptFor(role Role, question string, artifacts []string) string {
	var who, scope string
	switch role {
	case RoleArchitect:
		who = "the architect of this project"
		scope = "how it should be BUILT: structure, naming, sequencing, interfaces"
	case RolePM:
		who = "the product manager for this project"
		scope = "what it should DO: scope, non-goals, business rules, who it serves"
	}

	lines := []string{
		"You are " + who + ". An engineer implementing a ticket has stopped to",
		"ask you something. Decide it, or say you cannot.",
		"",
		"THE QUESTION",
		question,
		"",
		"WHAT YOU MAY REASON FROM",
	}
	if len(artifacts) == 0 {
		lines = append(lines,
			"Nothing. No design document exists in this repository, so you cannot",
			"derive anything. Refuse.")
	} else {
		lines = append(lines, "Only these files, which are the agreed design:",
			"  "+strings.Join(artifacts, "\n  "),
			"They cover "+scope+".")
	}

	lines = append(lines,
		"",
		"THE RULE",
		"Derive the answer from those documents and cite the clause, or refuse.",
		"",
		"Refusing is a correct outcome, not a failure. If the documents are",
		"silent, saying so is the honest answer: it means the design is",
		"incomplete, a person has to decide, and the document should then be",
		"amended so the next ticket does not ask again. If you answer anyway,",
		"your guess arrives wrapped in your authority and nobody downstream",
		"will question it.",
		"",
		"Do not read the implementation to work out what was intended. Code",
		"tells you what someone did, not what was agreed, and inferring intent",
		"from it is how a bug becomes a specification.",
		"",
		"ANSWER WITH ONE JSON OBJECT AND NOTHING ELSE",
		`{"verdict":"derived|refused|escalate",`,
		` "decision":"the answer, one or two sentences, empty unless derived",`,
		` "grounding":"the file and clause it comes from, empty unless derived",`,
		` "reason":"why you refused or escalated, empty if derived"}`,
		"",
		"Use escalate when the question is real but belongs to the other role:",
		"a product decision if you are the architect, a technical one if you",
		"are the product manager.",
	)
	return strings.Join(lines, "\n")
}

// parse extracts the verdict from the advisor's reply.
//
// Tolerant of a model that wraps JSON in prose or a code fence, because that
// happens and the alternative is discarding a good answer over formatting.
// But an unparseable reply becomes a REFUSAL, never a decision: the failure
// mode of guessing at a half-parsed answer is acting on something nobody
// said.
func parse(out string) Answer {
	raw := strings.TrimSpace(out)
	if i := strings.Index(raw, "{"); i >= 0 {
		if j := strings.LastIndex(raw, "}"); j > i {
			raw = raw[i : j+1]
		}
	}
	var a Answer
	if err := json.Unmarshal([]byte(raw), &a); err != nil {
		return Answer{
			Verdict: VerdictRefused,
			Reason: fmt.Sprintf(
				"the advisor's reply could not be parsed as a verdict, so it is treated "+
					"as a refusal rather than guessed at:\n%s", truncate(out, 400)),
		}
	}
	switch a.Verdict {
	case VerdictDerived, VerdictRefused, VerdictEscalate:
	default:
		a.Verdict = VerdictRefused
		if a.Reason == "" {
			a.Reason = "the advisor returned an unrecognised verdict"
		}
	}
	// A "derived" answer with no grounding is exactly the laundered guess this
	// package exists to stop. Downgrade it rather than trust it.
	if a.Verdict == VerdictDerived && strings.TrimSpace(a.Grounding) == "" {
		a.Verdict = VerdictRefused
		a.Reason = "the advisor decided without citing where the decision came from, " +
			"which is indistinguishable from inventing it"
		a.Decision = ""
	}
	return a
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
