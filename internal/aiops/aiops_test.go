package aiops

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/orion-sdlc/orion/internal/events"
	"github.com/orion-sdlc/orion/internal/supervisor"
	"github.com/orion-sdlc/orion/internal/tracker"
)

func at(sec int) time.Time {
	return time.Date(2026, 8, 30, 10, 0, sec, 0, time.UTC)
}

func ruleIDs(f []Finding) []string {
	out := make([]string, 0, len(f))
	for _, x := range f {
		out = append(out, x.Rule)
	}
	return out
}

func has(f []Finding, rule string) bool {
	for _, x := range f {
		if x.Rule == rule {
			return true
		}
	}
	return false
}

// Each rule has to match the message the emitting code ACTUALLY writes, so
// every fixture below is the literal from that emitter, cited by file. A rule
// that matches a message nobody writes reports nothing and looks clean while
// doing it -- which is the failure mode this pass exists to prevent, applied
// to the pass itself.
func TestEveryRuleFiresOnTheMessageOrionWrites(t *testing.T) {
	cases := []struct {
		rule string
		from string // where the message below is written
		evs  []events.Event
	}{{
		rule: "qa-no-verdict",
		from: "internal/work/qa.go qaNoVerdict",
		evs: []events.Event{{
			At: at(1), Kind: events.KindEscalate, Actor: events.ActorQA,
			Msg: "QA never reported a verdict: its closing message named neither QA-CLEAN " +
				"nor any finding, and it did not write one when asked again; no fix round " +
				"was dispatched, because nothing was described to fix",
		}},
	}, {
		rule: "fix-loop-exhausted",
		from: "internal/collect/fixloop.go giveUp",
		evs: []events.Event{{
			At: at(1), Kind: events.KindFailed, Actor: events.ActorOrion,
			Msg: "stopped fixing: 3 fix attempts were spent without a green build",
		}},
	}, {
		rule: "no-commit-blocked",
		from: "internal/work/work.go",
		evs: []events.Event{{
			At: at(1), Kind: events.KindBlocked, Actor: events.ActorImplementer,
			Msg: "ran cleanly but produced no commits; treating the closing message as a question",
		}},
	}, {
		rule: "ask-unanswered",
		from: "internal/work/work.go -- an ask with neither an answer nor a refusal (OR-201)",
		evs: []events.Event{
			{At: at(1), Kind: events.KindAsk, Actor: events.ActorImplementer, Msg: "which issuer?"},
			{At: at(2), Kind: events.KindNote, Actor: events.ActorOrion, Msg: "something else happened"},
		},
	}, {
		rule: "run-failed-terminal",
		from: "internal/work/work.go -- exit N with nothing after it",
		evs: []events.Event{{
			At: at(1), Kind: events.KindRunEnd, Actor: events.ActorImplementer, Msg: "exit 143",
		}},
	}}

	for _, c := range cases {
		t.Run(c.rule, func(t *testing.T) {
			got := Scan(c.evs)
			if !has(got, c.rule) {
				t.Fatalf("the %s rule did not fire on the message %s writes.\n"+
					"got %v -- a rule that matches nothing reports a clean run for a broken one",
					c.rule, c.from, ruleIDs(got))
			}
		})
	}
}

// The rate-limit rule is pinned end to end rather than against a copied
// string: the message is BUILT here by the code that writes it, so rewording
// Describe breaks this test instead of silently switching the rule off.
//
// OR-162 is the reason. An unrecognised status reported as an exhausted limit
// is a false statement about the account, and the whole point of noticing it
// is that nobody was watching when it happened.
func TestRateLimitRuleMatchesWhatDescribeActuallyWrites(t *testing.T) {
	lim := supervisor.RateLimit{
		Status: supervisor.LimitUnrecognised,
		Type:   "seven_day",
		Raw:    "some_status_from_the_future",
	}
	msg := lim.Describe(at(0))

	got := Scan([]events.Event{{
		At: at(1), Kind: events.KindBudget, Actor: events.ActorOrion, Msg: msg,
	}})
	if !has(got, "rate-limit-unrecognised") {
		t.Fatalf("the rate-limit rule did not match what Describe wrote:\n  %q\ngot %v",
			msg, ruleIDs(got))
	}
}

// The load-bearing test. Orion degrades ON PURPOSE in these places, so a pass
// that files tickets for any of them is worse than no pass: after the third
// such ticket nobody reads the fourth.
func TestDesignedDegradationIsNotAFinding(t *testing.T) {
	cases := []struct {
		name string
		why  string
		evs  []events.Event
	}{{
		name: "a no-change ending",
		why: "internal/work/noop.go: a run that found the work already present is a " +
			"RESULT, not a failure, and is recorded as a note",
		evs: []events.Event{
			{At: at(1), Kind: events.KindRunEnd, Actor: events.ActorImplementer, Msg: "exit 0"},
			{At: at(2), Kind: events.KindNote, Actor: events.ActorOrion,
				Msg: "no change: the change is already on the trunk"},
		},
	}, {
		name: "a lock timeout that proceeded unlocked",
		why:  "internal/procsafe: the caller is meant to proceed and say so",
		evs: []events.Event{{
			At: at(1), Kind: events.KindNote, Actor: events.ActorOrion,
			Msg: "orion: recorded usage without the lock (orion: lock timeout; update ran unserialized)",
		}},
	}, {
		name: "an advisor refusing to invent an answer",
		why:  "internal/events: a refusal is a valid close for an ask",
		evs: []events.Event{
			{At: at(1), Kind: events.KindAsk, Actor: events.ActorImplementer, Msg: "which issuer?"},
			{At: at(2), Kind: events.KindRefuse, Actor: events.ActorArchitect,
				Msg: "the spec does not say, and I will not guess"},
		},
	}, {
		name: "a failure the fix loop recovered from",
		why:  "the loop going red and then green is the loop doing its job",
		evs: []events.Event{
			{At: at(1), Kind: events.KindRunEnd, Actor: events.ActorImplementer, Msg: "exit 1"},
			{At: at(2), Kind: events.KindCI, Actor: events.ActorOrion, Msg: "fix attempt 1 of 3: tests failed"},
			{At: at(3), Kind: events.KindPush, Actor: events.ActorImplementer, Msg: "pushed orion/or-1"},
			{At: at(4), Kind: events.KindMerge, Actor: events.ActorCI, Msg: "merged"},
		},
	}, {
		name: "QA findings a fix round cleared",
		why:  "QA reports, it does not block; a cleared round is the system working",
		evs: []events.Event{
			{At: at(1), Kind: events.KindQA, Actor: events.ActorQA, Msg: "2 findings"},
			{At: at(2), Kind: events.KindQA, Actor: events.ActorQA, Msg: "clean after 1 fix round"},
			{At: at(3), Kind: events.KindCommit, Actor: events.ActorImplementer, Msg: "2 commit(s)"},
		},
	}, {
		name: "an absent optional tool",
		why:  "detected, never required: a missing gh is a supported configuration",
		evs: []events.Event{{
			At: at(1), Kind: events.KindNote, Actor: events.ActorOrion,
			Msg: "gh is not installed, so no pull request was opened",
		}},
	}, {
		name: "an ordinary successful run",
		why:  "nothing went wrong at all",
		evs: []events.Event{
			{At: at(1), Kind: events.KindClaimed, Actor: events.ActorOrion, Msg: "claimed OR-1"},
			{At: at(2), Kind: events.KindRunEnd, Actor: events.ActorImplementer, Msg: "exit 0"},
			{At: at(3), Kind: events.KindCommit, Actor: events.ActorImplementer, Msg: "3 commit(s)"},
			{At: at(4), Kind: events.KindPR, Actor: events.ActorOrion, Msg: "opened https://x/1"},
		},
	}}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Scan(c.evs); len(got) != 0 {
				t.Fatalf("filed %v for behaviour that is working correctly (%s).\n"+
					"A pass that tickets designed degradation is worse than no pass",
					ruleIDs(got), c.why)
			}
		})
	}
}

// One defect firing four times is one ticket, not four. A backlog that gets
// four drafts for four symptoms of the same thing is the noise this design is
// arranged to avoid.
func TestRepeatedHitsCollapseIntoOneFinding(t *testing.T) {
	var evs []events.Event
	for i := 1; i <= 4; i++ {
		evs = append(evs, events.Event{
			At: at(i), Kind: events.KindBlocked, Actor: events.ActorImplementer,
			Msg: "ran cleanly but produced no commits; treating the closing message as a question",
		})
	}
	got := Scan(evs)
	if len(got) != 1 {
		t.Fatalf("got %d findings, want 1 carrying all four hits: %v", len(got), ruleIDs(got))
	}
	if n := len(got[0].Evidence); n != 4 {
		t.Errorf("evidence lines = %d, want 4 -- a finding whose evidence is missing "+
			"cannot be checked", n)
	}
	if !strings.Contains(got[0].Title, "4 times") {
		t.Errorf("title = %q, want it to say how many times it fired", got[0].Title)
	}
}

// The cost gate. The agent is the only part of this pass that spends money,
// and a subagent started to stare at a log the rules already explain is the
// whole cost with none of the value.
func TestAgentIsNotStartedWhenRulesExplainEverything(t *testing.T) {
	evs := []events.Event{
		{At: at(1), Kind: events.KindFailed, Actor: events.ActorOrion,
			Msg: "stopped fixing: 3 fix attempts were spent without a green build"},
		{At: at(2), Kind: events.KindCommit, Actor: events.ActorImplementer, Msg: "1 commit(s)"},
	}
	found := Scan(evs)
	if len(found) == 0 {
		t.Fatal("the fixture should fire a rule, or this test proves nothing")
	}
	if left := Concerning(evs, found); len(left) != 0 {
		t.Errorf("Concerning = %d event(s), want 0: every concerning event here is "+
			"already claimed by a rule, so there is nothing to pay an agent to judge", len(left))
	}
}

// ...and it IS started for something no rule recognises, or the agent half of
// this design does nothing at all.
func TestUnrecognisedTroubleReachesTheAgent(t *testing.T) {
	evs := []events.Event{{
		At: at(1), Kind: events.KindBlocked, Actor: events.ActorOrion,
		Msg: "stopped fixing: blocked by policy: Edit(/etc/hosts) matches the protected rule \"system\"",
	}}
	found := Scan(evs)
	left := Concerning(evs, found)
	if len(left) != 1 {
		t.Fatalf("Concerning = %d, want the 1 unrecognised event; rules matched %v",
			len(left), ruleIDs(found))
	}
}

// A refusal is designed behaviour and no rule claims it, so if it counted as
// concerning every correctly-refused question would start a paid agent.
func TestARefusalDoesNotStartTheAgent(t *testing.T) {
	evs := []events.Event{
		{At: at(1), Kind: events.KindAsk, Actor: events.ActorImplementer, Msg: "which issuer?"},
		{At: at(2), Kind: events.KindRefuse, Actor: events.ActorArchitect, Msg: "the spec does not say"},
	}
	if left := Concerning(evs, Scan(evs)); len(left) != 0 {
		t.Errorf("Concerning = %d, want 0: a refusal is an advisor working correctly, "+
			"and paying to judge one every run is a nightly bill for nothing", len(left))
	}
}

// A triage that re-files the same defect every night is worse than none.
func TestAnAlreadyFiledFindingIsNotProposedAgain(t *testing.T) {
	f := Finding{Rule: "qa-no-verdict", Title: "QA finished without reporting a verdict"}
	_, body := Draft("OR-1", f)

	// Filed by a person who reworded the title, which is the point of a
	// draft: the marker is what survives, and matching on the title would
	// miss this and propose it again tomorrow.
	open := []tracker.Issue{{
		Key: "OR-99", Summary: "QA sometimes returns no verdict at all",
		Description: body,
	}}

	fresh, tracked := Dedupe([]Finding{f}, open)
	if len(fresh) != 0 {
		t.Errorf("proposed %v again although OR-99 already tracks it", ruleIDs(fresh))
	}
	if len(tracked) != 1 {
		t.Fatalf("tracked = %v, want the one finding", ruleIDs(tracked))
	}
}

// The dedupe must not silence a DIFFERENT defect just because some ticket
// carries a marker. A pass that swallows real findings is the OR-167 failure
// in a new place.
func TestAnUnrelatedOpenTicketDoesNotSilenceAFinding(t *testing.T) {
	_, filed := Draft("OR-1", Finding{Rule: "fix-loop-exhausted", Title: "x"})
	open := []tracker.Issue{
		{Key: "OR-98", Summary: "unrelated work", Description: "nothing to do with this"},
		{Key: "OR-99", Summary: "the fix loop", Description: filed},
	}

	fresh, _ := Dedupe([]Finding{{Rule: "qa-no-verdict", Title: "y"}}, open)
	if len(fresh) != 1 {
		t.Errorf("fresh = %v, want the qa-no-verdict finding still proposed", ruleIDs(fresh))
	}
}

// PROPOSE, DO NOT FILE, enforced by the type rather than by a comment.
//
// This test is the enforcement. If somebody adds Comment, Create or
// TransitionTo to Open so this package can "just file the obvious ones", this
// fails and says why -- which is the only moment anyone would think to argue
// about it.
func TestOpenCannotFile(t *testing.T) {
	iface := reflect.TypeOf((*Open)(nil)).Elem()
	if n := iface.NumMethod(); n != 1 {
		var names []string
		for i := 0; i < n; i++ {
			names = append(names, iface.Method(i).Name)
		}
		t.Fatalf("Open has %d methods (%v), want exactly Search.\n"+
			"This pass proposes tickets and a person creates them. A write method here "+
			"is how an autonomous filer gets built by accident", n, names)
	}
	if got := iface.Method(0).Name; got != "Search" {
		t.Errorf("Open's only method is %q, want Search -- read-only is the point", got)
	}
}

// The marker has to survive the round trip, or the dedupe silently stops
// working the day the draft format changes.
func TestDraftCarriesTheSignatureItWillBeDedupedOn(t *testing.T) {
	f := Finding{Rule: "rate-limit-unrecognised", Title: "t", Why: "w", Evidence: []string{"e"}}
	_, body := Draft("OR-1", f)

	if sigs := markersIn(body); len(sigs) != 1 || sigs[0] != f.Rule {
		t.Fatalf("markers in the draft = %v, want exactly [%s]", sigs, f.Rule)
	}
	for _, want := range []string{f.Why, "e", "proposed, not created"} {
		if !strings.Contains(body, want) {
			t.Errorf("draft is missing %q; a reader cannot judge it without that", want)
		}
	}
}

// An incomplete dedupe that looks complete is how the same defect gets filed
// twice: the reader takes a re-proposal for a new one.
func TestReportSaysWhenTheDedupeWasPartial(t *testing.T) {
	r := Report{
		Key: "OR-1", Scanned: 12,
		Fresh:      []Finding{{Rule: "qa-no-verdict", Title: "t", Why: "w"}},
		DedupeNote: "search failed (no jira credentials); already-filed proposals may repeat here",
	}
	if got := r.Text(); !strings.Contains(got, "already-filed proposals may repeat") {
		t.Errorf("the report does not say the dedupe was partial:\n%s", got)
	}
}

// An empty report has to be legible as "clean", not as "the pass broke".
func TestACleanRunReportsNothingWorthFiling(t *testing.T) {
	got := Report{Key: "OR-1", Scanned: 30}.Text()
	if !strings.Contains(got, "nothing worth filing") {
		t.Errorf("a clean report should say so plainly:\n%s", got)
	}
	if !strings.Contains(got, "30 events read") {
		t.Errorf("the report should say how much it read, so an empty result can be "+
			"told from an empty log:\n%s", got)
	}
}

// OpenIssues must report the cap rather than truncating in silence: a dedupe
// that quietly gave up reads exactly like one that found nothing.
func TestOpenIssuesReportsItsCap(t *testing.T) {
	full := make([]tracker.Issue, MaxOpenScanned)
	_, truncated, err := OpenIssues(stubOpen{issues: full}, "OR")
	if err != nil {
		t.Fatal(err)
	}
	if !truncated {
		t.Error("a full page was not reported as truncated, so the report would claim " +
			"a complete dedupe it did not do")
	}

	_, truncated, err = OpenIssues(stubOpen{issues: full[:3]}, "OR")
	if err != nil || truncated {
		t.Errorf("a short page reported truncated=%v err=%v, want false/nil", truncated, err)
	}
}

type stubOpen struct{ issues []tracker.Issue }

func (s stubOpen) Search(string, int) ([]tracker.Issue, error) { return s.issues, nil }
