package discovery

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func write(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "intent.md")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestMissingIntentIsNotReady(t *testing.T) {
	a := Assess(filepath.Join(t.TempDir(), "nope.md"))
	if a.Found {
		t.Error("a missing file must not report Found")
	}
	if a.Ready() {
		t.Error("no intent at all cannot be ready to design from")
	}
}

func TestCountsOnlyUnanswered(t *testing.T) {
	a := Assess(write(t, `# Intent
Something.

## Open questions
- [ ] Do loss adjusters need access?
- [x] Which auth do we use? Answer: the existing gateway JWT.
- ~~Should it paginate?~~ decided: yes
- Answer: rate limit is 50rps, confirmed with platform
- What is the retention period?
`))
	if !a.Found {
		t.Fatal("file should be found")
	}
	if a.Open != 2 {
		t.Errorf("Open = %d, want 2 (the unticked one and the bare question)", a.Open)
	}
	if a.Ready() {
		t.Error("open questions must block")
	}
}

// "None" is how people say there is nothing outstanding. Counting it as a
// question would block a chain whose intent is actually complete.
func TestNonePlaceholdersDoNotBlock(t *testing.T) {
	for _, body := range []string{
		"## Open questions\n- None\n",
		"## Open questions\n- n/a\n",
		"## Open questions\n- Nothing\n",
		"## Open questions\n",
	} {
		a := Assess(write(t, "# Intent\n\n"+body))
		if a.Open != 0 {
			t.Errorf("%q: Open = %d, want 0", body, a.Open)
		}
		if !a.Ready() {
			t.Errorf("%q should be ready", body)
		}
	}
}

// Without closing the section at the next heading, every bullet in the rest
// of the document counts as an open question.
func TestSectionEndsAtTheNextHeading(t *testing.T) {
	a := Assess(write(t, `# Intent

## Open questions
- Do adjusters need access?

## Constraints
- No new PII in the session
- Existing authentication only
- Must not change the public API
`))
	if a.Open != 1 {
		t.Errorf("Open = %d, want 1: bullets after the next heading are not questions", a.Open)
	}
}

func TestHeadingMatchingIsLenient(t *testing.T) {
	for _, h := range []string{"## Open questions", "### OPEN QUESTION", "# Open Questions"} {
		a := Assess(write(t, "# Intent\n\n"+h+"\n- something unresolved\n"))
		if a.Open != 1 {
			t.Errorf("heading %q: Open = %d, want 1", h, a.Open)
		}
	}
}

func TestNeedsDiscovery(t *testing.T) {
	tests := []struct {
		idea string
		want bool
	}{
		{"fix the typo in the footer", true},
		{"customers should see claim status in the portal", true},
		// Long and specific, with constraints and a rationale: the
		// conversation already happened somewhere else.
		{"Customers should see claim status, next step and expected date in the portal " +
			"so that they stop phoning the contact center. Must not introduce new PII " +
			"into the portal session and must use the existing authentication. Out of " +
			"scope: third-party loss adjusters.", false},
		{"add rate limiting to the status endpoint because the claims-core API " +
			"limits us to 50rps and we must not exceed it", false},
	}
	for _, tc := range tests {
		got, reason := NeedsDiscovery(tc.idea)
		if got != tc.want {
			t.Errorf("NeedsDiscovery(%.40q...) = %v (%s), want %v", tc.idea, got, reason, tc.want)
		}
		if reason == "" {
			t.Error("a decision must come with its reason")
		}
	}
}

// headingRe is deliberately lenient about casing and a trailing "s", but it
// is not free-form: a heading that says something else -- a typo, a rewording,
// a different plural -- never enters inSection, so every bullet under it is
// silently skipped rather than counted. This is the failure mode the whole
// gate exists to prevent, applied to the gate's own heading: a capture full
// of unanswered questions reports Open == 0 and Ready() == true.
func TestHeadingTypoOrRewordingParsesAsZeroQuestions(t *testing.T) {
	for _, tc := range []struct {
		name    string
		heading string
	}{
		{"typo", "## Opne questions"},
		{"reworded", "## Outstanding items"},
		{"different noun", "## Unresolved issues"},
		{"missing word", "## Questions"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := Assess(write(t, "# Intent\n\n"+tc.heading+"\n- Do adjusters need access?\n- What is the retention period?\n"))
			if a.Open != 0 {
				t.Errorf("heading %q: Open = %d, want 0 (a misworded heading must not be recognized)", tc.heading, a.Open)
			}
			if !a.Ready() {
				t.Errorf("heading %q: Ready() = false, want true -- a misworded heading must fail silently (pass), not loudly (block)", tc.heading)
			}
		})
	}
}

// The count under "## Open questions" must match the bullets actually
// there -- not bullets before the heading, not bullets after a later one.
func TestOpenCountsOnlyBulletsUnderTheExactHeading(t *testing.T) {
	a := Assess(write(t, `# Intent
- Not a question, this is above the heading.

## Open questions
- First open question.
- Second open question.
- Third open question.
`))
	if !a.Found {
		t.Fatal("file should be found")
	}
	if a.Open != 3 {
		t.Errorf("Open = %d, want 3", a.Open)
	}
	if len(a.Questions) != 3 {
		t.Errorf("Questions = %d, want 3", len(a.Questions))
	}
}

// Four ways a person settles a question in place, each pulled directly from
// what the intent prompt tells the agent to write (see intentShape and
// TestIntentPromptWritesWhatTheDiscoveryGateParses in the supervisor
// package). Every one of them must clear the gate, or a capture that
// answered everything still blocks the chain.
func TestEachSettlementMethodClearsTheQuestion(t *testing.T) {
	for _, tc := range []struct {
		name   string
		bullet string
	}{
		{"checkbox", "- [x] Do adjusters need access?"},
		{"strikethrough", "- ~~Do adjusters need access?~~"},
		{"inline answer", "- Do adjusters need access? Answer: yes, via the existing gateway."},
		{"none placeholder", "- None"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := Assess(write(t, "# Intent\n\n## Open questions\n"+tc.bullet+"\n"))
			if a.Open != 0 {
				t.Errorf("%s: Open = %d, want 0 (settled question still counted as open)", tc.name, a.Open)
			}
		})
	}
}

// Any single unanswered question, mixed in among settled ones, must block --
// the gate does not average or round down.
func TestBlocksLaterStagesWhileAnyQuestionRemainsUnanswered(t *testing.T) {
	a := Assess(write(t, `# Intent

## Open questions
- [x] Settled with a checkbox.
- ~~Settled with strikethrough.~~
- Settled inline. Answer: yes.
- Still unanswered: which retention period applies?
`))
	if a.Open != 1 {
		t.Errorf("Open = %d, want 1", a.Open)
	}
	if a.Ready() {
		t.Error("one unanswered question among several settled ones must still block")
	}
}

// The gate passes once every question is settled by any mix of the accepted
// methods, and separately when the section just says "- None".
func TestReadyWhenAllQuestionsSettledOrNone(t *testing.T) {
	settled := Assess(write(t, `# Intent

## Open questions
- [x] Settled with a checkbox.
- ~~Settled with strikethrough.~~
- Settled inline. Answer: yes.
`))
	if !settled.Ready() {
		t.Errorf("all questions settled but Ready() = false (Open = %d)", settled.Open)
	}

	none := Assess(write(t, "# Intent\n\n## Open questions\n- None\n"))
	if !none.Ready() {
		t.Errorf("section reads \"- None\" but Ready() = false (Open = %d)", none.Open)
	}
}

// answeredRe matches "[x]" case-insensitively, but a marker that is
// malformed in some other way -- a stray space breaking the three
// characters apart -- is not the same typo as case, and must still leave
// the question open rather than silently settle it.
func TestTypoedSettlementMarkerStaysOpen(t *testing.T) {
	for _, tc := range []struct {
		name   string
		bullet string
	}{
		{"space inside brackets", "- [x ] Do adjusters need access?"},
		{"space before x", "- [ x] Do adjusters need access?"},
		{"wrong bracket", "- (x) Do adjusters need access?"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := Assess(write(t, "# Intent\n\n## Open questions\n"+tc.bullet+"\n"))
			if a.Open != 1 {
				t.Errorf("bullet %q: Open = %d, want 1 (a malformed settlement marker must not clear the question)", tc.bullet, a.Open)
			}
		})
	}
}

// A bullet with nothing after the dash is not a question anyone forgot to
// answer -- it is empty. It must not inflate the open count or appear in
// Questions, or a stray blank list item silently blocks the chain forever.
func TestEmptyBulletUnderOpenQuestionsIsIgnored(t *testing.T) {
	a := Assess(write(t, "# Intent\n\n## Open questions\n-\n- Do adjusters need access?\n- \n"))
	if a.Open != 1 {
		t.Errorf("Open = %d, want 1 (empty bullets must not be counted)", a.Open)
	}
	if len(a.Questions) != 1 {
		t.Errorf("Questions = %d, want 1", len(a.Questions))
	}
}

// Nothing in Assess looks for a "Success measures" heading at all, so
// whatever shape that section is in -- absent bullets, a bare paragraph, a
// numbered list bulletRe does not match -- must not panic or otherwise
// break the parse of the Open questions section that follows it.
func TestMalformedSuccessMeasuresDoesNotBreakParsing(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
	}{
		{"no bullets, just prose", "# Intent\n\n## Success measures\nWe will know it worked.\n\n## Open questions\n- Do adjusters need access?\n"},
		{"numbered list instead of bullets", "# Intent\n\n## Success measures\n1. Median latency drops.\n2. Error rate drops.\n\n## Open questions\n- Do adjusters need access?\n"},
		{"heading with nothing under it", "# Intent\n\n## Success measures\n\n## Open questions\n- Do adjusters need access?\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := Assess(write(t, tc.body))
			if !a.Found {
				t.Fatal("file should be found")
			}
			if a.Open != 1 {
				t.Errorf("Open = %d, want 1 (a malformed success measures section must not affect the Open questions count)", a.Open)
			}
		})
	}
}

// A block that makes you go and find the questions is a block people learn
// to route around.
func TestGateMessageNamesTheQuestionsAndTheWayOut(t *testing.T) {
	a := Assess(write(t, "# Intent\n\n## Open questions\n- Do adjusters need access?\n"))
	m := a.GateMessage("claim-status-abc123")

	if !strings.Contains(m, "Do adjusters need access?") {
		t.Error("the message must name the question, not just point at a file")
	}
	if !strings.Contains(m, "orion answer claim-status-abc123") {
		t.Error("the message must give the exact command to resolve it")
	}
	if !strings.Contains(m, "[x]") {
		t.Error("the message must say how to mark something answered")
	}
}

// spec-kit's [NEEDS CLARIFICATION] marker is an open question in a different
// spelling. AssessSpec counts both forms; a marker inside fenced code is an
// example, not a question.
func TestAssessSpecCountsMarkersAndOpenBullets(t *testing.T) {
	a := AssessSpec(write(t, "# Spec\n\n"+
		"- **FR-003**: retain data for [NEEDS CLARIFICATION: retention period not specified]\n"+
		"- **FR-004**: auth via [NEEDS CLARIFICATION: SSO or password?]\n\n"+
		"```\n- **FR-006**: [NEEDS CLARIFICATION: this is the template's example]\n```\n\n"+
		"## Open questions\n- Which region ships first?\n- [x] Do adjusters need access?\n"))
	if !a.Found {
		t.Fatal("file should be found")
	}
	if a.Open != 3 {
		t.Errorf("Open = %d, want 3 (two markers and one unanswered bullet; the fenced one is an example)", a.Open)
	}
	if a.Ready() {
		t.Error("a spec with markers must block")
	}
	joined := ""
	for _, q := range a.Questions {
		joined += q.Text + "|"
	}
	for _, want := range []string{"retention period", "SSO or password", "Which region"} {
		if !strings.Contains(joined, want) {
			t.Errorf("questions %q do not carry %q", joined, want)
		}
	}
}

func TestAssessSpecWithNothingOpenIsReady(t *testing.T) {
	a := AssessSpec(write(t, "# Spec\n\n- **FR-001**: System MUST do the thing.\n\n## Open questions\n- None\n"))
	if !a.Ready() {
		t.Errorf("a spec with no markers and no open bullets must be ready: %+v", a)
	}
}

// Assess, the intent reader, keeps ignoring markers: an intent does not use
// them, and a stray mention in prose must not block.
func TestAssessDoesNotCountMarkers(t *testing.T) {
	a := Assess(write(t, "# Intent\n\nspec-kit marks gaps as [NEEDS CLARIFICATION: x].\n\n## Open questions\n- None\n"))
	if !a.Ready() {
		t.Errorf("the intent reader counted a marker: %+v", a)
	}
}

// Answer writes what Assess reads back: a bullet gets [x] and an Answer line
// after its continuation lines; the rest of the file is untouched.
func TestAnswerRoundTripsThroughAssess(t *testing.T) {
	p := write(t, "# Intent\n\n## Open questions\n\n- Which region ships first?\n- How many accounts, and\n  under which payer?\n- [x] Already settled.\n\n---\nfooter\n")
	a := Assess(p)
	if a.Open != 2 {
		t.Fatalf("Open = %d, want 2", a.Open)
	}
	// The multi-line one first, so the shift is exercised.
	if err := Answer(p, a.Questions[1], "Twelve, one payer."); err != nil {
		t.Fatal(err)
	}
	a = Assess(p)
	if err := Answer(p, a.Questions[0], "eu-west-1"); err != nil {
		t.Fatal(err)
	}
	a = Assess(p)
	if a.Open != 0 {
		t.Errorf("Open = %d after answering both: %+v", a.Open, a.Questions)
	}
	b, _ := os.ReadFile(p)
	got := string(b)
	for _, want := range []string{
		"- [x] Which region ships first?\n  Answer: eu-west-1\n",
		"- [x] How many accounts, and\n  under which payer?\n  Answer: Twelve, one payer.\n",
		"- [x] Already settled.\n\n---\nfooter\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("file lacks %q:\n%s", want, got)
		}
	}
}

// A marker is answered by replacing it with the decision, in place.
func TestAnswerReplacesAMarkerInPlace(t *testing.T) {
	p := write(t, "# Spec\n\n- **FR-003**: retain data for [NEEDS CLARIFICATION: retention period not specified] after close.\n\n## Open questions\n- retention period not specified\n")
	s := AssessSpec(p)
	// Same text in the body and under Open questions: one question.
	if s.Open != 1 {
		t.Fatalf("Open = %d, want 1 (the marker and the bullet ask the same thing)", s.Open)
	}
	if err := Answer(p, s.Questions[0], "13 months"); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(p)
	if !strings.Contains(string(b), "retain data for 13 months after close.") {
		t.Errorf("marker not replaced:\n%s", b)
	}
	if got := AssessSpec(p).Open; got != 0 {
		t.Errorf("Open = %d after answering, want 0 (bullet ticked, marker replaced)", got)
	}
}

func TestAnswerRefusesAnEmptyAnswerAndAMovedLine(t *testing.T) {
	p := write(t, "## Open questions\n- One?\n")
	q := Assess(p).Questions[0]
	if err := Answer(p, q, "  "); err == nil {
		t.Error("an empty answer was written")
	}
	q.Line = 9
	if err := Answer(p, q, "x"); err == nil {
		t.Error("a line past the end was accepted")
	}
}

// A marker in the body and a bullet under Open questions that carry the
// same identifier are one question, answered once, in both places.
func TestAssessSpecMergesAMarkerAndItsBulletByID(t *testing.T) {
	p := write(t, "# Spec\n\n- **FR-002**: accounts in scope [NEEDS CLARIFICATION: OQ-02 — How many accounts? Stand-in: 12.]\n\n"+
		"Prose about `[NEEDS CLARIFICATION]` markers does not count.\n\n"+
		"## Open questions\n\n- [ ] OQ-02 — How many accounts? *Stand-in: 12.* (FR-002)\n- [ ] OQ-09 — Case-fold tags? *No stand-in.*\n")
	s := AssessSpec(p)
	if s.Open != 2 {
		t.Fatalf("Open = %d, want 2 (OQ-02 once, OQ-09 once, the prose marker never): %+v", s.Open, s.Questions)
	}
	var oq2 Question
	for _, q := range s.Questions {
		if q.ID == "OQ-02" {
			oq2 = q
		}
		if strings.HasPrefix(q.Text, "[ ]") {
			t.Errorf("the checkbox leaked into the question text: %q", q.Text)
		}
	}
	if oq2.Line == 0 || len(oq2.Markers) != 1 {
		t.Fatalf("OQ-02 should be the bullet carrying its marker's line: %+v", oq2)
	}
	if err := Answer(p, oq2, "12, one Organization"); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(p)
	got := string(b)
	if !strings.Contains(got, "accounts in scope 12, one Organization\n") {
		t.Errorf("the body marker was not replaced:\n%s", got)
	}
	if !strings.Contains(got, "- [x] OQ-02 — How many accounts? *Stand-in: 12.* (FR-002)\n  Answer: 12, one Organization\n") {
		t.Errorf("the bullet was not ticked and answered in place:\n%s", got)
	}
	if strings.Contains(got, "[x] [ ]") {
		t.Error("a second checkbox was inserted")
	}
	if got := AssessSpec(p).Open; got != 1 {
		t.Errorf("Open = %d after answering OQ-02, want 1", got)
	}
}

// A marker with no bullet is still a question, on its own.
func TestAMarkerWithoutABulletStandsAlone(t *testing.T) {
	p := write(t, "# Spec\n\nRetain for [NEEDS CLARIFICATION: how long?].\n\n## Open questions\n- None\n")
	if got := AssessSpec(p).Open; got != 1 {
		t.Errorf("Open = %d, want 1", got)
	}
}

// An answer that names no value is refused: it becomes the requirement's
// text and ticks the box, so every later stage reads it as settled. Real
// examples, from a real project.
func TestAnswerRefusesAnAnswerThatNamesNoValue(t *testing.T) {
	p := write(t, "## Open questions\n- Which cost basis is the bill?\n")
	q := Assess(p).Questions[0]

	for _, hedge := range []string{
		"same as cloudhealth", "whaever broadcom cloudhealth does",
		"again check broadcom cloudhealth", "not decided yet",
		"TBD", "unknown", "N/A", "same", "yes",
		"check what the finance team uses",
	} {
		if err := Answer(p, q, hedge); err == nil {
			t.Errorf("%q was accepted as an answer", hedge)
		}
	}
	// Still open: nothing was written.
	if got := Assess(p).Open; got != 1 {
		t.Errorf("Open = %d after refusals, want 1", got)
	}

	// And real answers are not refused, including ones that mention another
	// system or open with a word a hedge also uses.
	for _, real := range []string{
		"Unblended cost per linked account per month, as Cost Explorer reports it, including tax and credits.",
		"Twelve, all under one AWS Organization.",
		"eu-west-1 only.",
		"Yes: read-only access on the management account, granted on request.",
		"Check-in happens weekly, and the figure is the month-end total.",
	} {
		p2 := write(t, "## Open questions\n- A question?\n")
		if err := Answer(p2, Assess(p2).Questions[0], real); err != nil {
			t.Errorf("a real answer was refused: %q\n  %v", real, err)
		}
	}
}
