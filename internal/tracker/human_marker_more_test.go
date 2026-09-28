package tracker

import (
	"sync"
	"testing"
	"time"
)

// OR-558. HumanOnly reads the DESCRIPTION only. A tag can only ever have been
// written there by decompose -- Labels are never applied to a HUMAN-marked
// task precisely so it stays unclaimed, and Jira keeps no separate "comment"
// or "attachment" field on the Issue this package sees at all. So a marker
// sitting anywhere else a person might type it -- a label, a comment, a link,
// an attachment name -- must not make an ordinary sub-task read as one.
func TestHumanMarkerOutsideTheDescriptionIsNotSeen(t *testing.T) {
	cases := []struct {
		name  string
		issue Issue
	}{
		{"marker text living in a label", Issue{
			Description: "Add the endpoint.\n",
			Labels:      []string{"HUMAN"},
		}},
		{"marker text living in the summary", Issue{
			Summary:     "HUMAN: register the OIDC client",
			Description: "Add the endpoint.\n",
		}},
		{"marker text living in the URL", Issue{
			Description: "Add the endpoint.\n",
			URL:         "https://jira.example.com/browse/OR-1?comment=HUMAN",
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if HumanOnly(tc.issue) {
				t.Errorf("a marker outside the description was read as HUMAN-marked: %+v", tc.issue)
			}
		})
	}
}

// A description a tracker has truncated (Jira does this on some fields past
// a length limit) either still carries the whole marker line, in which case
// it must still match, or cuts the marker mid-word, in which case it must
// not -- there is no marker to find in "HUM" alone.
func TestHumanMarkerOnATruncatedDescription(t *testing.T) {
	whole := "Run quickstart scenario 1 end to end.\n\nHUMAN: a person picks it up."
	if !HumanOnly(Issue{Description: whole}) {
		t.Error("a marker line left intact by truncation was not recognised")
	}
	cutMidWord := "Run quickstart scenario 1 end to end.\n\nHUM"
	if HumanOnly(Issue{Description: cutMidWord}) {
		t.Error("truncation left only \"HUM\", which is not the marker, but it matched")
	}
}

// A description that used to carry the marker and had it edited out (a
// person rewording the ticket, or decompose regenerating it without the
// [HUMAN] tag) must read as ordinary from then on -- HumanOnly has no memory
// of what a description used to say, only what it says now.
func TestHumanMarkerRemovedFromADescriptionNoLongerMatches(t *testing.T) {
	before := Issue{Description: "Run quickstart scenario 1.\n\nHUMAN: a person picks it up.\n"}
	if !HumanOnly(before) {
		t.Fatal("setup: expected the original description to match")
	}
	after := Issue{Description: "Run quickstart scenario 1.\n"}
	if HumanOnly(after) {
		t.Error("a description with the marker line stripped out still read as HUMAN-marked")
	}
}

// A marker line reworded away from decompose's exact wording, but still
// starting the line with the bare word HUMAN, must still match -- the
// contract tasks_test.go pins is that decompose's own wording matches, not
// that ONLY decompose's own wording matches. A rewritten HUMAN line is still
// a human's line.
func TestHumanMarkerRewordedButStillLineInitialStillMatches(t *testing.T) {
	reworded := Issue{Description: "Run quickstart scenario 1.\n\n" +
		"HUMAN — needs a person with prod console access, not an agent.\n"}
	if !HumanOnly(reworded) {
		t.Error("a reworded but still line-initial HUMAN marker was not recognised")
	}
}

// A pathological description built to make a naive engine backtrack
// catastrophically must still resolve in well under a second: the marker
// pattern is a plain anchored `^\s*HUMAN\b` with no nested quantifiers, so it
// has nothing to backtrack on regardless of input shape.
func TestHumanMarkerDetectionDoesNotBlowUpOnAdversarialInput(t *testing.T) {
	adversarial := ""
	for i := 0; i < 20000; i++ {
		adversarial += " "
	}
	adversarial += "HUMANX"

	done := make(chan bool, 1)
	go func() { done <- HumanOnly(Issue{Description: adversarial}) }()
	select {
	case got := <-done:
		if got {
			t.Error("HUMANX is not the HUMAN marker (word boundary), but it matched")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("HumanOnly did not return within 2s on adversarial input -- possible ReDoS")
	}
}

// Invalid UTF-8 in a description (a mangled export, a bad encoding round
// trip) must not panic the matcher -- regexp.MatchString operates on bytes
// and never throws, but a change swapping in a different engine or adding
// pre-processing must not introduce one either.
func TestHumanMarkerDetectionOnInvalidUTF8DoesNotPanic(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("HumanOnly panicked on invalid UTF-8: %v", r)
		}
	}()
	invalid := "HUMAN: \xff\xfe broken encoding here.\n"
	HumanOnly(Issue{Description: invalid})
}

// HumanOnly does no mutation and touches no shared state -- it is a pure
// read of the Issue it is given -- so concurrent callers on distinct issues,
// and on the SAME issue, must never race or disagree with each other.
func TestHumanOnlyIsSafeUnderConcurrentUse(t *testing.T) {
	human := Issue{Description: "HUMAN: a person picks it up.\n"}
	ordinary := Issue{Description: "Add the endpoint.\n"}

	var wg sync.WaitGroup
	errs := make(chan string, 200)
	for i := 0; i < 100; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			if !HumanOnly(human) {
				errs <- "concurrent call on the human issue returned false"
			}
		}()
		go func() {
			defer wg.Done()
			if HumanOnly(ordinary) {
				errs <- "concurrent call on the ordinary issue returned true"
			}
		}()
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		t.Error(e)
	}
}
