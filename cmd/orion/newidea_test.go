package main

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	"github.com/orion-sdlc/orion/internal/tracker"
)

// fakeIdeas is the two-method slice of the tracker the idea path needs.
type fakeIdeas struct {
	issues map[string]tracker.Issue

	searchErr  error
	commentErr error
	comments   map[string]string
}

func (f *fakeIdeas) Search(jql string, max int) ([]tracker.Issue, error) {
	if f.searchErr != nil {
		return nil, f.searchErr
	}
	for k, iss := range f.issues {
		if strings.Contains(jql, k) {
			return []tracker.Issue{iss}, nil
		}
	}
	return nil, nil
}

func (f *fakeIdeas) Comment(key, text string) error {
	if f.commentErr != nil {
		return f.commentErr
	}
	if f.comments == nil {
		f.comments = map[string]string{}
	}
	f.comments[key] = text
	return nil
}

// The template Jira ships with a fresh discovery project, copied VERBATIM
// from the live PRIOR project rather than written from memory. Two details
// only the real text has, both of which broke the first implementation:
// the headings come back with no "#" markers (Jira flattens ADF), and the
// apostrophe is the curly one.
const unfilledTemplate = "Objective\n" +
	"What outcome are we trying to achieve? What does success look like?\n" +
	"Problem\n" +
	"Define customer problems, why they\u2019re urgent and important.\n" +
	"Solution\n" +
	"Outline the proposed solution and validation status\n" +
	"Risks\n" +
	"List key risks and mitigation strategies\n" +
	"Supporting documents\n" +
	"Embed docs, design file or PDF"

const writtenIdea = `## Objective

Cut the time from a merged ticket to a released binary from days to minutes.

## Problem

Releases are cut by hand, so they happen when somebody remembers.

## Solution

Promote on a schedule behind one approval.`

// OR-349. An idea that IS written up answers the interview before it is
// asked, so the command must not ask.
func TestAWrittenIdeaSkipsTheInterview(t *testing.T) {
	ideas := &fakeIdeas{issues: map[string]tracker.Issue{
		"PRIOR-3": {Key: "PRIOR-3", Summary: "Faster releases", Description: writtenIdea},
	}}
	tr := workingTracker()
	var out bytes.Buffer

	// EMPTY stdin. If the command interviews, it reads blank answers and the
	// description will not carry the idea's own words -- which is the
	// assertion below.
	err := newRun(tr, newOptions{
		Idea: "PRIOR-3", Site: "https://x.atlassian.net",
		In: strings.NewReader(""), Out: &out, Ideas: ideas,
		Confirm: func(string) bool { return true },
	})
	if err != nil {
		t.Fatalf("newRun: %v\n%s", err, out.String())
	}
	if tr.creates != 1 {
		t.Fatalf("created %d project(s), want 1:\n%s", tr.creates, out.String())
	}
	if !strings.Contains(tr.createdDesc, "Cut the time from a merged ticket") {
		t.Errorf("the project description does not carry the idea's own words:\n%s", tr.createdDesc)
	}
	if !strings.Contains(tr.createdDesc, "PRIOR-3") {
		t.Errorf("the description does not say where it came from:\n%s", tr.createdDesc)
	}
	if tr.createdName != "Faster releases" {
		t.Errorf("name = %q, want the idea's summary", tr.createdName)
	}
	if strings.Contains(out.String(), "Five questions") {
		t.Errorf("it interviewed anyway:\n%s", out.String())
	}
}

// The load-bearing case. Every idea in a fresh discovery project carries the
// unfilled template, and planning from boilerplate produces a confident,
// empty plan -- the exact failure the interview exists to prevent.
func TestAnUnfilledTemplateIsNotAnIdea(t *testing.T) {
	idea := tracker.Issue{Key: "PRIOR-5", Summary: "Explore VR", Description: unfilledTemplate}
	ok, why := writtenDown(idea)
	if ok {
		t.Fatal("the unfilled template was accepted as a written-up idea; a stage would " +
			"design against \"Define customer problems, why they're urgent\"")
	}
	if !strings.Contains(why, "template") {
		t.Errorf("the reason does not name the template: %q", why)
	}
}

func TestHeadingsWithNothingUnderThemAreNotAnIdea(t *testing.T) {
	// Both forms: flattened as Jira returns it, since that is what arrives.
	idea := tracker.Issue{Key: "PRIOR-6", Description: "Objective\n\nProblem\n\nRisks\n"}
	if ok, why := writtenDown(idea); ok {
		t.Error("headings alone were accepted as an idea")
	} else if !strings.Contains(why, "nothing written under them") {
		t.Errorf("reason = %q", why)
	}
}

func TestAnIdeaWithRealProseIsAccepted(t *testing.T) {
	idea := tracker.Issue{Key: "PRIOR-7", Description: writtenIdea}
	if ok, why := writtenDown(idea); !ok {
		t.Errorf("a written-up idea was rejected: %s", why)
	}
}

// A short idea in two real sentences is worth more than five headings of
// placeholder, so the test is prose rather than length.
func TestAShortButRealIdeaIsAccepted(t *testing.T) {
	idea := tracker.Issue{Key: "PRIOR-8", Description: "Ship a CLI that reads a plan and runs it."}
	if ok, why := writtenDown(idea); !ok {
		t.Errorf("a short but real idea was rejected: %s", why)
	}
}

// OR-349's link-back decision: comment the key, never move the status.
func TestTheIdeaIsToldWhichProjectCameFromIt(t *testing.T) {
	ideas := &fakeIdeas{issues: map[string]tracker.Issue{
		"PRIOR-3": {Key: "PRIOR-3", Summary: "Faster releases", Description: writtenIdea},
	}}
	tr := workingTracker()
	var out bytes.Buffer

	if err := newRun(tr, newOptions{
		Idea: "PRIOR-3", Site: "https://x.atlassian.net",
		In: strings.NewReader(""), Out: &out, Ideas: ideas,
		Confirm: func(string) bool { return true },
	}); err != nil {
		t.Fatalf("newRun: %v", err)
	}

	got, ok := ideas.comments["PRIOR-3"]
	if !ok {
		t.Fatal("the idea was never told which project came from it")
	}
	if !strings.Contains(got, tr.createdKey) {
		t.Errorf("the comment does not name the new project: %q", got)
	}
	if !strings.Contains(strings.ToLower(got), "status is unchanged") {
		t.Errorf("the comment does not say the idea's own status was left alone: %q", got)
	}
}

// Best effort: the project exists by the time the comment is attempted, and
// losing the link is untidy where losing the run would not be.
func TestAFailedBackLinkDoesNotFailTheRun(t *testing.T) {
	ideas := &fakeIdeas{
		issues:     map[string]tracker.Issue{"PRIOR-3": {Key: "PRIOR-3", Summary: "X", Description: writtenIdea}},
		commentErr: fmt.Errorf("403 forbidden"),
	}
	tr := workingTracker()
	var out bytes.Buffer

	if err := newRun(tr, newOptions{
		Idea: "PRIOR-3", Site: "https://x.atlassian.net",
		In: strings.NewReader(""), Out: &out, Ideas: ideas,
		Confirm: func(string) bool { return true },
	}); err != nil {
		t.Fatalf("a failed comment failed the whole run: %v", err)
	}
	if tr.creates != 1 {
		t.Fatalf("the project was not created: %d", tr.creates)
	}
	if !strings.Contains(out.String(), "PRIOR-3") {
		t.Errorf("the warning does not name the idea it could not reach:\n%s", out.String())
	}
}

// A key that does not resolve must say so rather than interviewing about the
// literal string "PRIOR-99".
func TestAnUnknownKeyIsAnError(t *testing.T) {
	ideas := &fakeIdeas{issues: map[string]tracker.Issue{}}
	tr := workingTracker()
	var out bytes.Buffer

	err := newRun(tr, newOptions{
		Idea: "PRIOR-99", Site: "https://x.atlassian.net",
		In: strings.NewReader(""), Out: &out, Ideas: ideas,
		Confirm: func(string) bool { return true },
	})
	if err == nil {
		t.Fatal("an unknown key was accepted")
	}
	if !strings.Contains(err.Error(), "PRIOR-99") {
		t.Errorf("the error does not name the key: %v", err)
	}
	if tr.creates != 0 {
		t.Error("a project was created for an idea that does not exist")
	}
}

// Prose that merely MENTIONS a key is an idea, not a key. Treating it as one
// would fetch the wrong issue and silently plan from it.
func TestProseMentioningAKeyIsNotAKey(t *testing.T) {
	for _, s := range []string{
		"OR-42 needs a rewrite",
		"rewrite OR-42",
		"a faster release process",
	} {
		if looksLikeIdeaKey(s) {
			t.Errorf("%q was read as a tracker key", s)
		}
	}
	for _, s := range []string{"OR-42", "PRIOR-3", "or-42", "  PRIOR-100  "} {
		if !looksLikeIdeaKey(s) {
			t.Errorf("%q was not read as a tracker key", s)
		}
	}
}

// No idea on the command line is not a usage error: this command interviews,
// so the missing idea is simply its first question.
func TestAnAbsentIdeaIsAskedForRatherThanRefused(t *testing.T) {
	tr := workingTracker()
	var out bytes.Buffer

	// The idea, then the five elaboration answers, then the project name.
	in := answers("a portal where customers see claim status",
		"", "", "", "", "", "Claims Portal")

	if err := newRun(tr, newOptions{
		Idea: "", Site: "https://x.atlassian.net",
		In: strings.NewReader(in), Out: &out,
		Confirm: func(string) bool { return true },
	}); err != nil {
		t.Fatalf("an absent idea was refused instead of asked for: %v\n%s", err, out.String())
	}
	if tr.creates != 1 {
		t.Fatalf("created %d project(s), want 1:\n%s", tr.creates, out.String())
	}
	if !strings.Contains(out.String(), "What do you want built?") {
		t.Errorf("it never asked for the idea:\n%s", out.String())
	}
	if !strings.Contains(tr.createdDesc, "claim status") {
		t.Errorf("the typed idea did not reach the description:\n%s", tr.createdDesc)
	}
	// One heading, not two -- the ask and the echo share it.
	if n := strings.Count(out.String(), "The idea"); n != 1 {
		t.Errorf("the \"The idea\" heading appears %d times, want 1:\n%s", n, out.String())
	}
}

// Answering the first question with nothing is still nothing to plan from.
func TestAnEmptyAnswerToTheFirstQuestionStops(t *testing.T) {
	tr := workingTracker()
	var out bytes.Buffer

	err := newRun(tr, newOptions{
		Idea: "", Site: "https://x.atlassian.net",
		In: strings.NewReader("\n"), Out: &out,
		Confirm: func(string) bool { return true },
	})
	if err == nil {
		t.Fatal("an empty answer was accepted as an idea")
	}
	if tr.creates != 0 {
		t.Error("a project was created with no idea")
	}
}
