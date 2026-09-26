package discovery

// OR-481: an "Answer:" on a bullet's continuation line answers it. The gate
// used to read only the first line, so a hand-written answer under an unticked
// box stayed open while the refusal quoted that same answer back.

import (
	"os"
	"path/filepath"
	"testing"
)

func assessOpenQuestions(t *testing.T, body string) Assessment {
	t.Helper()
	p := filepath.Join(t.TempDir(), "spec.md")
	if err := os.WriteFile(p, []byte("# Spec\n\n## Open questions\n\n"+body), 0o644); err != nil {
		t.Fatal(err)
	}
	return AssessSpec(p)
}

func TestContinuationAnswerCountsAsAnswered(t *testing.T) {
	for name, body := range map[string]string{
		"unticked, Answer on the next line": "- [ ] **Q-19** Which history, measured how?\n  Answer: after an ITSM source is connected\n",
		"Answer after a wrapped question":   "- [ ] **Q-19** Which history,\n  over what window, measured how?\n  Answer: 12 months\n",
		"struck through on a continuation":  "- [ ] **Q-2** Which floor?\n  ~~ERROR~~ settled in the plan\n",
		"lower-case answer":                 "- [ ] **Q-3** Which tracker?\n  answer: Jira\n",
	} {
		if a := assessOpenQuestions(t, body); a.Open != 0 {
			t.Errorf("%s: %d open, want 0: %+v", name, a.Open, a.Questions)
		}
	}
}

// Question prose that merely mentions an answer is not an answer.
func TestMidSentenceAnswerOnAContinuationIsStillOpen(t *testing.T) {
	body := "- [ ] **Q-7** Retention is unsettled because\n  the answer: depends on the store, and nobody chose one.\n"
	if a := assessOpenQuestions(t, body); a.Open != 1 {
		t.Errorf("a continuation that only mentions the answer counted as answered: open=%d", a.Open)
	}
}

// The forms that already worked keep working.
func TestFirstLineFormsStillWork(t *testing.T) {
	for name, body := range map[string]string{
		"ticked":             "- [x] **Q-1** Which floor?\n",
		"inline answer":      "- **Q-1** Which floor? Answer: ERROR\n",
		"struck through":     "- ~~**Q-1** Which floor?~~\n",
		"orion answer shape": "- [x] **Q-1** Which floor?\n  Answer: ERROR\n",
	} {
		if a := assessOpenQuestions(t, body); a.Open != 0 {
			t.Errorf("%s: %d open, want 0", name, a.Open)
		}
	}
	if a := assessOpenQuestions(t, "- [ ] **Q-1** Which floor?\n  Nobody has decided.\n"); a.Open != 1 {
		t.Errorf("an unanswered question counted as answered: open=%d", a.Open)
	}
}
