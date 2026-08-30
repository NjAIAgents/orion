package main

import (
	"testing"

	"github.com/orion-sdlc/orion/internal/tracker"
)

// An open milestone with everything finished is exactly what this command
// exists for: before OR-209 closing it meant a hand-written PUT to the REST
// API with the operator's own credentials.
func TestCloseAnOpenCompleteMilestone(t *testing.T) {
	got := decideClose(tracker.Version{Name: "v0.8.1"}, nil, false)

	if got.Action != "close" {
		t.Errorf("a complete open milestone resolved to %q, want close", got.Action)
	}
}

// Idempotent like `release create`. A second run must report the fact and
// change nothing, or the command cannot be re-run after a partial failure.
func TestClosingAnAlreadyReleasedVersionIsNotAnError(t *testing.T) {
	v := tracker.Version{Name: "v0.8.1", Released: true, ReleaseDate: "2026-08-29"}

	if got := decideClose(v, nil, false); got.Action != "already" {
		t.Errorf("re-closing a released version resolved to %q, want already", got.Action)
	}
	// Even with unfinished tickets: the version is already closed, so the
	// refusal would report a state this command neither caused nor can fix,
	// and re-running would stop being a no-op.
	if got := decideClose(v, []string{"OR-1"}, false); got.Action != "already" {
		t.Errorf("a released version with unfinished tickets resolved to %q, want already", got.Action)
	}
}

// The failure this guards is a milestone that says work shipped when it did
// not -- which is silent afterwards, because the version reads as complete.
func TestUnfinishedTicketsRefuseTheCloseAndAreNamed(t *testing.T) {
	got := decideClose(tracker.Version{Name: "v0.8.1"}, []string{"OR-207", "OR-209"}, false)

	if got.Action != "refuse" {
		t.Fatalf("an incomplete milestone resolved to %q, want refuse", got.Action)
	}
	if len(got.NotDone) != 2 || got.NotDone[0] != "OR-207" {
		t.Errorf("the refusal does not carry the unfinished tickets, so it cannot name "+
			"them: %v", got.NotDone)
	}
}

// --force exists because the operator may know something the tracker does
// not; the point is that it must be typed.
func TestForceClosesAnIncompleteMilestone(t *testing.T) {
	got := decideClose(tracker.Version{Name: "v0.8.1"}, []string{"OR-207"}, true)

	if got.Action != "close" {
		t.Errorf("--force resolved to %q, want close", got.Action)
	}
	if len(got.NotDone) != 1 {
		t.Errorf("a forced close dropped the unfinished tickets, so nothing reports what "+
			"was overridden: %v", got.NotDone)
	}
}

func TestReleaseDatePrefersTheFlagThenTheTagThenToday(t *testing.T) {
	cases := []struct {
		name, flag, tag, want string
	}{
		{"flag wins over the tag", "2026-08-29", "2026-08-30", "2026-08-29"},
		{"tag date when no flag", "", "2026-08-29", "2026-08-29"},
		{"today when neither", "", "", ""},
		{"today when git answered with something else", "", "fatal: no such ref", ""},
	}
	for _, c := range cases {
		got, err := releaseDate(c.flag, c.tag)
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		if got != c.want {
			t.Errorf("%s: releaseDate(%q, %q) = %q, want %q", c.name, c.flag, c.tag, got, c.want)
		}
	}
}

// A malformed date must not reach Jira: it would either 400 halfway through a
// release or, worse, be accepted as some other day.
func TestReleaseDateRejectsAMalformedFlag(t *testing.T) {
	if _, err := releaseDate("29-08-2026", ""); err == nil {
		t.Error("a non-ISO --date was accepted")
	}
}
