package work

import (
	"errors"
	"strings"
	"testing"
)

const fbTitle = "FCIA-7: Add the thing"
const fbBody = "Implements FCIA-7.\n\nhttps://jira/FCIA-7\n\n3 commit(s)."

func TestAGoodDescriptionReplacesTheTitleAndKeepsTheTrailer(t *testing.T) {
	run := func(string, string, string, string) (string, error) {
		return `{"title":"Recompute segment impact rather than apportioning it",
		         "body":"## Summary\nThe old split apportioned totals."}`, nil
	}
	title, body, ok := describePR(run, "/repo", "FCIA-7", fbTitle, fbBody)

	if !ok {
		t.Fatal("a valid description was rejected")
	}
	if title != "Recompute segment impact rather than apportioning it" {
		t.Errorf("title = %q", title)
	}
	if !strings.Contains(body, "The old split apportioned totals") {
		t.Error("the generated body is missing")
	}
	// The skill knows nothing about the ticket or the run. Those links are
	// what let anyone reading this in six months find out why it exists.
	if !strings.Contains(body, "https://jira/FCIA-7") {
		t.Error("Orion's trailer was dropped, taking the ticket link with it")
	}
}

// Every failure must fall back, never propagate. The branch is pushed and the
// work is done; refusing to open a pull request over a cosmetic failure would
// strand finished work.
func TestEveryFailureFallsBackToOrionsOwnDescription(t *testing.T) {
	for name, run := range map[string]Describer{
		"nil runner": nil,
		"run failed": func(string, string, string, string) (string, error) {
			return "", errors.New("breaker tripped")
		},
		"not json": func(string, string, string, string) (string, error) {
			return "I could not run the pr-describe skill.", nil
		},
		"empty title": func(string, string, string, string) (string, error) {
			return `{"title":"","body":"something"}`, nil
		},
		"empty body": func(string, string, string, string) (string, error) {
			return `{"title":"something","body":""}`, nil
		},
		"garbage": func(string, string, string, string) (string, error) {
			return `{{{`, nil
		},
	} {
		title, body, ok := describePR(run, "/repo", "FCIA-7", fbTitle, fbBody)
		if ok {
			t.Errorf("%s: reported success", name)
		}
		if title != fbTitle || body != fbBody {
			t.Errorf("%s: fallback was not used exactly (%q)", name, title)
		}
	}
}

// A model that explains itself around the JSON is the common case, not an
// error. Refusing that would make the fallback the normal path.
func TestJSONWrappedInProseOrAFenceIsStillRead(t *testing.T) {
	for _, out := range []string{
		"Here you go:\n```json\n{\"title\":\"T\",\"body\":\"B\"}\n```\nHope that helps.",
		"{\"title\":\"T\",\"body\":\"B\"}",
		"\n\n  {\"title\":\"T\",\"body\":\"B\"}  \n",
	} {
		title, _, ok := parseDescription(out)
		if !ok || title != "T" {
			t.Errorf("did not parse: %q", out)
		}
	}
}

// gh pr create --title takes one line. A multi-line title breaks the command
// that opens the pull request, which turns a description problem into a
// pull request that does not exist.
func TestAMultiLineTitleIsReducedToOneLine(t *testing.T) {
	title, _, ok := parseDescription("{\"title\":\"First line\\nsecond line\",\"body\":\"B\"}")
	if !ok {
		t.Fatal("not parsed")
	}
	if strings.Contains(title, "\n") || title != "First line" {
		t.Errorf("title = %q, want a single line", title)
	}
}

func TestAnOverlongBodyIsTruncatedRatherThanSent(t *testing.T) {
	long := strings.Repeat("x", maxPRBody+500)
	_, body, ok := parseDescription(`{"title":"T","body":"` + long + `"}`)
	if !ok {
		t.Fatal("not parsed")
	}
	if len(body) > maxPRBody+50 {
		t.Errorf("body is %d bytes; it must be bounded", len(body))
	}
	if !strings.Contains(body, "truncated") {
		t.Error("truncation must be visible, or a reader trusts a cut-off sentence")
	}
}

// Captured from a real pr-describe run against a real branch, prose and all.
// The skill led with a line about its own reasoning before the JSON -- so the
// tolerant parse is the NORMAL path here, not a defensive extra. Written
// against what it actually emits rather than what it ought to.
const realOutput = `Have enough context. Drafting directly, no agent spawn needed for single-commit branch.

{"title": "feat(impact): segment-level impact, recomputed not apportioned (FCIA-6)", "body": "## Summary\nAdds segment-level impact analysis to ImpactReport -- each segment's metrics are recomputed over its own rows, never apportioned from the population total.\n\n## Changes\n- src/fcia/impact.py: extracts a shared aggregate()"}`

func TestTheParserHandlesWhatTheSkillActuallyEmits(t *testing.T) {
	title, body, ok := parseDescription(realOutput)
	if !ok {
		t.Fatal("a real pr-describe reply was not parsed")
	}
	if !strings.HasPrefix(title, "feat(impact): segment-level impact") {
		t.Errorf("title = %q", title)
	}
	if strings.Contains(title, "Have enough context") {
		t.Error("the model's preamble leaked into the title")
	}
	if !strings.Contains(body, "## Summary") {
		t.Errorf("body lost its structure: %q", body)
	}
}
