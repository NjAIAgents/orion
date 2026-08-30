package discovery

import (
	"bytes"
	"strings"
	"testing"
)

// The point of the interview is that nothing said, and nothing left unsaid,
// disappears between the terminal and the tracker project's description.
// These tests fail if either half of that stops being true.

func TestInterviewRecordsEveryAnswerUnderItsHeading(t *testing.T) {
	in := strings.NewReader(strings.Join([]string{
		"claims handlers in the contact centre",
		"they read status off three systems",
		"payments, and anything outside the portal",
		"a handler answers without leaving the portal",
		"", // keep the proposed name
	}, "\n") + "\n")

	k := Interview(in, &bytes.Buffer{}, "claim status in the portal", "Claim status portal", true)

	if k.Name != "Claim status portal" {
		t.Fatalf("empty name input should keep the proposal, got %q", k.Name)
	}
	desc := k.Description()
	for _, want := range []string{
		"claim status in the portal",
		"## Who it is for\n\nclaims handlers in the contact centre",
		"## The problem\n\nthey read status off three systems",
		"## Out of scope\n\npayments, and anything outside the portal",
		"## Success looks like\n\na handler answers without leaving the portal",
		"## Open questions\n\n- None",
	} {
		if !strings.Contains(desc, want) {
			t.Errorf("description is missing %q\n---\n%s", want, desc)
		}
	}
}

func TestInterviewTakesTheTypedNameOverTheProposal(t *testing.T) {
	in := strings.NewReader("a\nb\nc\nd\nPortal Status Service\n")
	k := Interview(in, &bytes.Buffer{}, "idea", "Idea", true)
	if k.Name != "Portal Status Service" {
		t.Fatalf("name = %q, want the typed one", k.Name)
	}
}

// A blank answer must survive as an open question. Dropping it would let the
// non-interactive stages invent the answer, which is the whole failure the
// discovery gate exists to prevent.
func TestBlankAnswersBecomeOpenQuestionsTheGateCanSee(t *testing.T) {
	in := strings.NewReader("handlers\n\n\nfewer calls\n\n")
	k := Interview(in, &bytes.Buffer{}, "idea", "Idea", true)

	open := k.OpenQuestions()
	if len(open) != 2 {
		t.Fatalf("open questions = %v, want the two blank ones", open)
	}
	if open[0] != sections[1].Prompt || open[1] != sections[2].Prompt {
		t.Fatalf("open questions are not the ones left blank: %v", open)
	}

	// The gate reads the rendered description, not the struct. If Assess
	// cannot see these, nothing downstream blocks on them.
	a := assessString(t, k.Description())
	if a.Open != 2 {
		t.Fatalf("Assess found %d open question(s) in the description, want 2", a.Open)
	}
}

func TestFullyAnsweredDescriptionPassesTheGate(t *testing.T) {
	in := strings.NewReader("w\nx\ny\nz\n\n")
	k := Interview(in, &bytes.Buffer{}, "idea", "Idea", true)
	a := assessString(t, k.Description())
	if !a.Ready() {
		t.Fatalf("a fully answered intake should be Ready, got open=%d", a.Open)
	}
}

// Input can end before the questions do. What was gathered still has to come
// back, and the rest has to read as open rather than as answered-with-empty.
func TestEarlyEOFKeepsWhatWasAnsweredAndOpensTheRest(t *testing.T) {
	k := Interview(strings.NewReader("handlers"), &bytes.Buffer{}, "idea", "Idea", true)

	if got := k.answer(0); got != "handlers" {
		t.Fatalf("first answer = %q, want it kept despite the EOF", got)
	}
	if k.Name != "Idea" {
		t.Fatalf("name = %q, want the proposal to stand after EOF", k.Name)
	}
	if len(k.OpenQuestions()) != 3 {
		t.Fatalf("open questions = %v, want the three never asked", k.OpenQuestions())
	}
}

// --skip-discovery and an idea that already states its constraints both land
// here: no elaboration, but the name is still agreed rather than assumed.
func TestWithoutElaborationOnlyTheNameIsAsked(t *testing.T) {
	out := &bytes.Buffer{}
	k := Interview(strings.NewReader("Claims Portal\n"), out, "idea", "Idea", false)

	if k.Name != "Claims Portal" {
		t.Fatalf("name = %q, want the typed one", k.Name)
	}
	if strings.Contains(out.String(), sections[0].Prompt) {
		t.Error("the elaboration questions were asked despite elaborate=false")
	}
	if len(k.OpenQuestions()) != len(sections) {
		t.Errorf("unelaborated intake should leave every question open, got %v", k.OpenQuestions())
	}
}

// A whitespace-only answer is not "something was typed" -- it must be treated
// exactly like an empty one: open, not a hidden invisible answer the gate
// can't see.
func TestWhitespaceOnlyAnswerIsTreatedAsBlank(t *testing.T) {
	in := strings.NewReader("handlers\n   \t  \nout of scope\nsuccess\n\n")
	k := Interview(in, &bytes.Buffer{}, "idea", "Idea", true)

	open := k.OpenQuestions()
	if len(open) != 1 || open[0] != sections[1].Prompt {
		t.Fatalf("open questions = %v, want only %q", open, sections[1].Prompt)
	}
	if strings.Contains(k.Description(), "\t") {
		t.Fatalf("whitespace answer leaked into the description:\n%s", k.Description())
	}
}

func TestNameFromSlug(t *testing.T) {
	for slug, want := range map[string]string{
		"claim-status-portal": "Claim status portal",
		"payments":            "Payments",
		"":                    "",
	} {
		if got := NameFromSlug(slug); got != want {
			t.Errorf("NameFromSlug(%q) = %q, want %q", slug, got, want)
		}
	}
}

// assessString runs the real gate over a rendered description.
func assessString(t *testing.T, s string) Assessment {
	t.Helper()
	return Assess(write(t, s))
}
