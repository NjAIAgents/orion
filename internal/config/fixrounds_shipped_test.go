package config

import (
	"os"
	"strings"
	"testing"
)

// A knob nobody can find is a knob that does not exist, which is the whole
// complaint OR-226 opens with: qa.max_rounds had been settable for months, was
// in no template and in no doc, and so was universally believed not to be.
//
// These tests are about DISCOVERABILITY, which is why they assert on the
// shipped files rather than on behaviour. Behaviour is covered in qa_test.go;
// a correct default nobody can locate is the failure being guarded here.

// The template is what `orion init` writes and what doctor tells people to
// copy, so a ceiling absent from it is a ceiling nobody inherits a mention of.
// Each also needs its "_comment_", because the template's inline explanations
// are the only place the trade -- what a higher ceiling costs -- is stated at
// the point somebody is deciding.
func TestTheTemplateCarriesBothFixCeilingsWithTheirComment(t *testing.T) {
	body, err := os.ReadFile("../../templates/orion.json")
	if err != nil {
		t.Fatal(err)
	}
	src := string(body)

	for _, want := range []string{
		`"_comment_qa"`, `"max_rounds"`,
		`"_comment_ci"`, `"max_fix_attempts"`,
	} {
		if !strings.Contains(src, want) {
			t.Errorf("templates/orion.json does not mention %s", want)
		}
	}
	// The comment has to carry the COST, not merely name the setting. "Three
	// is the operator's call" only holds if the operator is told what three
	// buys and what it spends.
	for _, want := range []string{"TRADE", "orion config limits"} {
		if !strings.Contains(src, want) {
			t.Errorf("templates/orion.json states no %q; the comment explains the "+
				"trade and how to change it, or it is decoration", want)
		}
	}

	// And the numbers in it must be the numbers in force. A template that
	// states 2 while the code defaults to 3 is worse than one that says
	// nothing: every repository it creates is pinned to the old ceiling by a
	// value its author never chose.
	cfg := Load("../../templates")
	if cfg.Degraded {
		t.Fatalf("templates/orion.json: %s", cfg.DegradedReason)
	}
	if got := cfg.QA.Rounds(); got != FixRounds {
		t.Errorf("templates/orion.json pins qa.max_rounds at %d; the shipped ceiling is %d",
			got, FixRounds)
	}
	if got := cfg.CI.Attempts(); got != FixRounds {
		t.Errorf("templates/orion.json pins ci.max_fix_attempts at %d; the shipped ceiling is %d",
			got, FixRounds)
	}
}

// The limits table in USAGE.md is where somebody looks for "what bounds can I
// change". Two circuit breakers missing from it is how both stayed invisible.
func TestTheUsageLimitsTableNamesBothFixCeilings(t *testing.T) {
	body, err := os.ReadFile("../../docs/USAGE.md")
	if err != nil {
		t.Fatal(err)
	}
	src := string(body)

	for _, want := range []string{"`qa.max_rounds`", "`ci.max_fix_attempts`"} {
		if !strings.Contains(src, want) {
			t.Errorf("docs/USAGE.md does not list %s among the guardrails", want)
		}
	}
	// Named in the table AND settable from the documented command. OR-198 and
	// OR-131 both settled that a value people change gets a command rather
	// than a file to edit, so the doc that names the value names the command.
	if !strings.Contains(src, "orion config limits qa.max_rounds") ||
		!strings.Contains(src, "orion config limits ci.max_fix_attempts") {
		t.Error("docs/USAGE.md names the ceilings but not how to set them; " +
			"hand-editing orion.json is the path this is meant to replace")
	}
}
