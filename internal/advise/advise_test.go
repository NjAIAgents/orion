package advise

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/orion-sdlc/orion/internal/decide"
)

func runner(reply string, err error) (Runner, *[]string) {
	var prompts []string
	return func(dir, model, prompt string) (string, error) {
		prompts = append(prompts, model+"\x00"+prompt)
		return reply, err
	}, &prompts
}

func TestDerivedAnswerCarriesItsGrounding(t *testing.T) {
	run, prompts := runner(`{"verdict":"derived",
	  "decision":"By issuer.",
	  "grounding":"spec.md section 4: the segmentation key is the issuing bank",
	  "reason":""}`, nil)

	a, err := Ask(run, t.TempDir(), RoleArchitect, "MCC or issuer?", []string{"spec.md"})
	if err != nil {
		t.Fatal(err)
	}
	if !a.Answered() {
		t.Fatalf("answer = %+v", a)
	}
	if a.Role != RoleArchitect || a.Model != ModelAdvisor {
		t.Errorf("role/model = %q/%q", a.Role, a.Model)
	}
	if !strings.Contains(a.Grounding, "spec.md") {
		t.Errorf("grounding = %q", a.Grounding)
	}
	// The advisor must be asked with the right model.
	if !strings.HasPrefix((*prompts)[0], ModelAdvisor+"\x00") {
		t.Errorf("advisor ran on the wrong model: %q", (*prompts)[0][:20])
	}
}

// The core rule: a decision with no citation is indistinguishable from an
// invention, and an invention that arrives wrapped in an advisor's authority
// is worse than no answer -- nobody downstream questions it.
func TestADecisionWithoutGroundingIsDowngradedToARefusal(t *testing.T) {
	run, _ := runner(`{"verdict":"derived","decision":"By issuer.","grounding":"","reason":""}`, nil)

	a, err := Ask(run, t.TempDir(), RoleArchitect, "q", []string{"spec.md"})
	if err != nil {
		t.Fatal(err)
	}
	if a.Answered() {
		t.Fatal("an ungrounded decision was accepted")
	}
	if a.Verdict != VerdictRefused {
		t.Errorf("verdict = %q", a.Verdict)
	}
	if a.Decision != "" {
		t.Errorf("the ungrounded decision survived: %q", a.Decision)
	}
	if !strings.Contains(a.Reason, "inventing") {
		t.Errorf("the reason should say why: %q", a.Reason)
	}
}

// An unparseable reply must become a refusal, never a guessed decision:
// acting on a half-parsed answer means acting on something nobody said.
func TestAnUnparseableReplyBecomesARefusal(t *testing.T) {
	for _, reply := range []string{
		"I think probably by issuer, but it depends.",
		"", "```\nnot json\n```",
	} {
		run, _ := runner(reply, nil)
		a, _ := Ask(run, t.TempDir(), RoleArchitect, "q", []string{"spec.md"})
		if a.Answered() {
			t.Errorf("reply %q was accepted as an answer: %+v", reply, a)
		}
		if a.Verdict != VerdictRefused {
			t.Errorf("reply %q gave verdict %q", reply, a.Verdict)
		}
	}
}

// Models wrap JSON in prose or a fence; discarding a good answer over
// formatting would be its own kind of wrong.
func TestJSONIsFoundInsideProseOrAFence(t *testing.T) {
	for _, reply := range []string{
		"Here is my verdict:\n```json\n{\"verdict\":\"derived\",\"decision\":\"By issuer.\",\"grounding\":\"spec.md 4\"}\n```\nHope that helps.",
		"{\"verdict\":\"derived\",\"decision\":\"By issuer.\",\"grounding\":\"spec.md 4\"}",
	} {
		run, _ := runner(reply, nil)
		a, _ := Ask(run, t.TempDir(), RoleArchitect, "q", nil)
		if !a.Answered() {
			t.Errorf("valid JSON inside %q was missed: %+v", reply[:20], a)
		}
	}
}

func TestRefusalAndEscalationSurvive(t *testing.T) {
	run, _ := runner(`{"verdict":"refused","reason":"spec.md does not mention fees"}`, nil)
	a, _ := Ask(run, t.TempDir(), RoleArchitect, "q", nil)
	if a.Answered() || a.Verdict != VerdictRefused {
		t.Errorf("a = %+v", a)
	}
	if !strings.Contains(a.Reason, "fees") {
		t.Errorf("the reason was lost: %q", a.Reason)
	}

	run2, _ := runner(`{"verdict":"escalate","reason":"this is a product decision"}`, nil)
	b, _ := Ask(run2, t.TempDir(), RoleArchitect, "q", nil)
	if b.Verdict != VerdictEscalate {
		t.Errorf("b = %+v", b)
	}
}

// Misrouting must be safe: a product question sent to the architect gets
// refused for lack of grounding, so the cost is one cheap wasted call.
func TestRouteClassifiesAndFailsSafe(t *testing.T) {
	run, prompts := runner("PRODUCT", nil)
	if got := Route(run, t.TempDir(), "do we charge a fee on declines?"); got != RolePM {
		t.Errorf("got %q", got)
	}
	if !strings.HasPrefix((*prompts)[0], ModelRouter+"\x00") {
		t.Errorf("the router ran on the wrong model")
	}

	run2, _ := runner("TECHNICAL", nil)
	if got := Route(run2, t.TempDir(), "which package should this live in?"); got != RoleArchitect {
		t.Errorf("got %q", got)
	}

	// A router that fails must not stop the loop. The architect refuses
	// cheaply if it was the wrong choice.
	run3, _ := runner("", errors.New("model unavailable"))
	if got := Route(run3, t.TempDir(), "anything"); got != RoleArchitect {
		t.Errorf("a failed route gave %q, want the safe default", got)
	}
}

// Each role sees only the documents that decide its kind of question.
// Handing the architect intent.md would let it answer a product question by
// inferring what someone probably wanted -- the laundering this prevents.
func TestArtifactsAreScopedToTheRole(t *testing.T) {
	dir := t.TempDir()
	for _, f := range []string{"intent.md", "spec.md", "plan.md"} {
		if err := os.WriteFile(filepath.Join(dir, f), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	arch := strings.Join(Artifacts(dir, RoleArchitect), " ")
	if strings.Contains(arch, "intent.md") {
		t.Errorf("the architect can see intent.md: %q", arch)
	}
	if !strings.Contains(arch, "spec.md") || !strings.Contains(arch, "plan.md") {
		t.Errorf("architect artifacts = %q", arch)
	}

	pm := strings.Join(Artifacts(dir, RolePM), " ")
	if !strings.Contains(pm, "intent.md") {
		t.Errorf("pm artifacts = %q", pm)
	}
	if strings.Contains(pm, "spec.md") {
		t.Errorf("the product manager can see spec.md: %q", pm)
	}
}

// A confirmed recommendation is a decision and binds whoever reads it next.
// An unconfirmed one is a proposal, and an advisor handed it would derive
// from it and cite the file -- which is how something nobody agreed to
// acquires a citation and stops being questionable (OR-153).
func TestConfirmedRecommendationsAreInScopeAndPendingOnesAreNot(t *testing.T) {
	dir := t.TempDir()
	for _, d := range []string{decide.PendingDir, decide.ConfirmedDir} {
		if err := os.MkdirAll(filepath.Join(dir, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, role := range []Role{RoleArchitect, RolePM, RoleDBA} {
		got := strings.Join(Artifacts(dir, role), " ")
		if !strings.Contains(got, decide.ConfirmedDir) {
			t.Errorf("%s cannot see confirmed decisions: %q", role, got)
		}
		if strings.Contains(got, decide.PendingDir) {
			t.Errorf("%s can reason from an UNCONFIRMED recommendation: %q", role, got)
		}
	}
}

func TestArtifactsOnlyListsWhatExists(t *testing.T) {
	dir := t.TempDir()
	if got := Artifacts(dir, RoleArchitect); len(got) != 0 {
		t.Errorf("named files that do not exist: %v", got)
	}
}

// With no design documents at all there is nothing to derive from, and the
// prompt must say so rather than inviting the advisor to improvise.
func TestPromptTellsAnUngroundedAdvisorToRefuse(t *testing.T) {
	p := promptFor(RoleArchitect, "anything?", nil)
	if !strings.Contains(p, "Refuse") {
		t.Errorf("a prompt with no artifacts should instruct refusal:\n%s", p)
	}

	full := promptFor(RoleArchitect, "anything?", []string{"spec.md"})
	for _, want := range []string{"cite the clause", "Refusing is a correct outcome", "spec.md"} {
		if !strings.Contains(full, want) {
			t.Errorf("prompt is missing %q", want)
		}
	}
	// Reading the code to infer intent is how a bug becomes a specification.
	if !strings.Contains(full, "not what was agreed") {
		t.Errorf("the prompt should forbid inferring intent from the implementation")
	}
}
