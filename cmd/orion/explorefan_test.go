package main

import (
	"os"
	"sort"
	"strings"
	"testing"

	"github.com/orion-sdlc/orion/internal/events"
	"github.com/orion-sdlc/orion/internal/supervisor"
)

// OR-229: the implementer's exploration phase asks its questions ALL AT ONCE,
// through supervisor.Fan, instead of one round trip at a time. What these
// tests hold is the part a regression would silently undo -- that N questions
// really do go out as N subagents, that each answer comes back attached to the
// question it answers, and that one child failing does not take the siblings
// with it.

// claudePerQuestion puts a `claude` on PATH that answers according to WHICH
// question it was given, so a test can prove answers are matched to questions
// rather than merely counted. The prompt reaches the script through argv.
//
// A question whose marker is not listed exits 1, which is how a test asks for
// one child of a fan-out to fail.
//
// TWO PROPERTIES THIS FIXTURE HAS TO HAVE, and the first version had neither.
// It is the reason OR-229 was reverted on 2026-08-31 as a "correctness fault"
// that was in fact a fault in this function:
//
//	DETERMINISTIC ORDER  the arms were emitted by ranging a map, so their order
//	                     was Go's randomised map order. Sorted now, so a run
//	                     that passes passes for a reason.
//	DISJOINT MARKERS     the arms are matched against the whole argv, and argv
//	                     carries ExplorePrompt -- whose READ ONLY section
//	                     contains the word "worktree". A marker of "worktree"
//	                     therefore matched EVERY child's prompt, so whichever
//	                     arm the random order happened to emit first answered
//	                     all three questions. That is precisely the reported
//	                     symptom ("answer 0 cites workspace.go, want events.go")
//	                     and it reproduced under -count=2 because a second run
//	                     is a second draw from the map order.
//
// Markers are asserted disjoint from the prompt template below rather than
// merely chosen carefully, because "chosen carefully" is what was done last
// time.
func claudePerQuestion(t *testing.T, answers map[string]string) {
	t.Helper()
	markers := make([]string, 0, len(answers))
	for m := range answers {
		markers = append(markers, m)
	}
	sort.Strings(markers)

	// A marker that appears in the prompt template matches every child, which
	// is a silently wrong test rather than a failing one. Caught here, where
	// the message can say what to do about it.
	for _, m := range markers {
		if markerCollidesWithPrompt(m) {
			t.Fatalf("the marker %q appears in ExplorePrompt itself, so it would match every "+
				"child's argv and every question would get the same answer; pick a marker "+
				"that only the question contains", m)
		}
	}

	var b strings.Builder
	b.WriteString("#!/bin/sh\nfor a in \"$@\"; do\n  case \"$a\" in\n")
	for _, marker := range markers {
		// The marker is quoted inside the pattern: an unquoted space in a
		// `case` pattern is a syntax error, and a marker of one word is not
		// enough to tell three questions apart.
		//
		// printf rather than echo, because some shells' echo expands the \n
		// inside the JSON string into a real newline -- which is invalid JSON,
		// so the supervisor reads a run that emitted no result at all and the
		// test fails for a reason that has nothing to do with the code.
		b.WriteString("    *\"" + marker + "\"*) printf '%s\\n' '{\"type\":\"result\"," +
			"\"session_id\":\"s\",\"result\":\"" + answers[marker] +
			"\",\"total_cost_usd\":0.01,\"is_error\":false}'; exit 0;;\n")
	}
	b.WriteString("  esac\ndone\necho 'no answer for that question' >&2\nexit 1\n")

	writeFakeBin(t, "claude", b.String())
}

// markerCollidesWithPrompt reports a marker that every child would match.
//
// The prompt is sent on argv, so every word in the TEMPLATE is present in
// every child's arguments. A fixture keyed off argv can only tell children
// apart by text the template does not contain.
func markerCollidesWithPrompt(marker string) bool {
	// A question no real caller would ask, so what remains is the template.
	return strings.Contains(supervisor.ExplorePrompt("\x00"), marker)
}

// The trap that cost OR-229 a revert, pinned so the next person meets it as a
// named fact rather than as a test that fails one run in three.
//
// "worktree" is the obvious word for a question about worktrees, and it is in
// the template's READ ONLY section -- so a fixture keyed on it answered every
// question with whichever arm the randomised map order happened to emit first.
// That was reported as a pairing fault in supervisor.Fan. It was not: Fan
// writes results[i] with i captured by value, and exploreAll reads out[i] from
// the same index. The production code was correct both times.
func TestAMarkerFromThePromptTemplateIsRefused(t *testing.T) {
	if !strings.Contains(supervisor.ExplorePrompt("\x00"), "worktree") {
		t.Skip("the template no longer contains the word this test documents")
	}
	if !markerCollidesWithPrompt("worktree") {
		t.Error("a marker that appears in the prompt template was accepted; every child would " +
			"match it, and every question would come back with the same answer -- the exact " +
			"symptom that was misdiagnosed as a pairing bug in the production code")
	}
	if markerCollidesWithPrompt("a marker no template contains") {
		t.Error("a marker absent from the template was refused, so no fixture can be written")
	}
}

// The central claim of the ticket: several questions asked in one call are
// several subagents, and every answer comes back attached to the question it
// answers. Order is what makes that safe -- results arrive in whatever order
// the children finish, and a caller that trusted arrival order would print
// each answer under somebody else's question.
func TestExploreAllAnswersEveryQuestionInTheOrderItWasAsked(t *testing.T) {
	claudePerQuestion(t, map[string]string{
		"the event log":    "appended by events.Log\\nPATHS: internal/events/events.go",
		"the children cap": "limits.max_concurrent_children\\nPATHS: internal/config/config.go",
		"a checkout added": "workspace.AddWorktree\\nPATHS: internal/workspace/workspace.go",
	})
	ws := triageWS(t)

	questions := []string{
		"where is the event log written?",
		"what holds the children cap?",
		"where is a checkout added?",
	}
	got := exploreAll(ws, "OR-229", questions)

	if len(got) != len(questions) {
		t.Fatalf("got %d answers for %d questions", len(got), len(questions))
	}
	want := []string{"events.go", "config.go", "workspace.go"}
	for i := range got {
		if got[i].Err != nil {
			t.Fatalf("question %d (%q) came back with nothing: %v", i, got[i].Question, got[i].Err)
		}
		if got[i].Question != questions[i] {
			t.Errorf("answer %d is filed under %q, want %q -- answers arriving out of order "+
				"must not be printed under the wrong question", i, got[i].Question, questions[i])
		}
		if !strings.Contains(strings.Join(got[i].Answer.Paths, ","), want[i]) {
			t.Errorf("answer %d cites %v, want the file %q the question was about -- the answer "+
				"was matched to the wrong question", i, got[i].Answer.Paths, want[i])
		}
	}
}

// Fan's failure policy, seen from its first real caller: a fan-out where one
// child's failure discards four completed answers is worse than asking one
// question at a time, which is the thing this replaced.
func TestExploreAllKeepsTheAnswersWhenOneQuestionFails(t *testing.T) {
	claudePerQuestion(t, map[string]string{
		"the event log": "appended by events.Log\\nPATHS: internal/events/events.go",
	})
	ws := triageWS(t)

	got := exploreAll(ws, "OR-229", []string{
		"where is the event log written?",
		"a question nothing will answer",
	})

	if got[0].Err != nil {
		t.Errorf("the answered question was discarded by its sibling's failure: %v", got[0].Err)
	}
	if !strings.Contains(got[0].Answer.Answer, "events.Log") {
		t.Errorf("the surviving answer is empty: %+v", got[0])
	}
	if got[1].Err == nil {
		t.Error("the failed question reported no error, so the caller would print an empty " +
			"answer as though the subagent had said nothing was there")
	}
}

// A batch that failed entirely and a batch that failed partly are different
// results, and only the first means "go and read it yourself".
func TestExploreAllFailedOnlyReportsATotalFailure(t *testing.T) {
	partial := []exploredQuestion{{Question: "a"}, {Question: "b", Err: os.ErrNotExist}}
	if _, all := exploreAllFailed(partial); all {
		t.Error("one failure among several was reported as a total failure; the answers that " +
			"did arrive would be thrown away")
	}
	total := []exploredQuestion{{Question: "a", Err: os.ErrNotExist}, {Question: "b", Err: os.ErrClosed}}
	err, all := exploreAllFailed(total)
	if !all || err == nil {
		t.Errorf("a batch where nothing came back must report the failure and its reason, got %v/%v",
			err, all)
	}
}

// Answers arrive in a batch, so an unlabelled one belongs to whichever
// question the reader guesses. A single answer keeps the shape it always had:
// the caller of one explore is reading prose, and a header above one answer is
// noise on a prefix that is re-sent every turn.
func TestPrintExploreAllLabelsAnswersOnlyWhenThereAreSeveral(t *testing.T) {
	var many strings.Builder
	printExploreAll(&many, []exploredQuestion{
		{Question: "where is the rate limiter?", Answer: exploreAnswer{
			Answer: "a token bucket", Paths: []string{"internal/supervisor/ratelimit.go"}}},
		{Question: "where is the event log?", Err: os.ErrNotExist},
	})
	got := many.String()
	if !strings.Contains(got, "where is the rate limiter?") ||
		!strings.Contains(got, "where is the event log?") {
		t.Errorf("answers are not labelled with their questions: %q", got)
	}
	if !strings.Contains(got, "Read it yourself") {
		t.Errorf("the unanswered question does not send the caller to read it: %q", got)
	}
	if !strings.Contains(got, "unaffected") {
		t.Errorf("one failure must not read as the whole batch failing: %q", got)
	}

	var one strings.Builder
	printExploreAll(&one, []exploredQuestion{{Question: "where is the rate limiter?",
		Answer: exploreAnswer{Answer: "a token bucket", Paths: []string{"x.go"}}}})
	if strings.Contains(one.String(), "Q: ") {
		t.Errorf("a single answer gained a header it never had: %q", one.String())
	}
}

// An answer arriving is the same event whether its subagent ran alone or
// alongside four others, so nothing in the per-answer notes says a fan-out
// happened. This is what makes the concurrency queryable afterwards -- and
// what an acceptance criterion asking for concurrent explores in events.jsonl
// is read off.
func TestLogExploreDispatchRecordsTheFanOut(t *testing.T) {
	ws := triageWS(t)

	logExploreDispatch(ws, "OR-229", []string{"q one", "q two", "q three"})

	evs, err := events.Read(events.Path(ws.Dir))
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, e := range evs {
		if e.Actor != events.ActorExplore || !strings.Contains(e.Msg, "dispatched") {
			continue
		}
		found = true
		if !strings.Contains(e.Msg, "3") {
			t.Errorf("the dispatch event does not say how many went out: %q", e.Msg)
		}
		if e.Key != "OR-229" {
			t.Errorf("dispatch event key = %q, want the ticket it was spent on", e.Key)
		}
		qs, _ := e.Detail["questions"].([]any)
		if len(qs) != 3 {
			t.Errorf("dispatch detail questions = %v, want the three that were asked -- "+
				"a count with no questions cannot be checked against the answers", e.Detail["questions"])
		}
		if e.Detail["max_concurrent"] == nil {
			t.Error("the dispatch event does not record the cap it ran under, so a log cannot " +
				"say whether three questions ran three-at-once or two-then-one")
		}
	}
	if !found {
		t.Error("nothing recorded the fan-out; afterwards there is no way to tell a batch of " +
			"three questions from three separate explores")
	}
}

// One question is not a fan-out, and a "dispatched 1 concurrently" line for
// every single explore buries the batches it exists to make findable.
func TestLogExploreDispatchStaysQuietForOneQuestion(t *testing.T) {
	ws := triageWS(t)

	logExploreDispatch(ws, "OR-229", []string{"just the one"})

	evs, _ := events.Read(events.Path(ws.Dir))
	for _, e := range evs {
		if strings.Contains(e.Msg, "dispatched") {
			t.Errorf("a single question logged a fan-out: %q", e.Msg)
		}
	}
}
