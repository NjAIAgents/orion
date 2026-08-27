package main

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/orion-sdlc/orion/internal/lessons"
)

// The failure OR-99 is about was invisible precisely because it was quiet:
// `orion lessons list` returned cleanly and returned nothing, every time, for
// the whole life of the store. An empty answer and a subsystem nobody is
// writing to must never print the same thing.
func TestAnUnwrittenStoreDoesNotLookLikeAnEmptyOne(t *testing.T) {
	var quiet, broken bytes.Buffer

	printLessons(&broken, nil, lessons.Health{}, nil)
	printLessons(&quiet, nil, lessons.Health{Sightings: 4, LastObserved: time.Now()}, nil)

	if !strings.Contains(broken.String(), "OBSERVED") {
		t.Errorf("a store nothing writes to reported as merely empty:\n%s", broken.String())
	}
	if strings.Contains(quiet.String(), "OBSERVED") {
		t.Errorf("a working store was reported as broken:\n%s", quiet.String())
	}
	if !strings.Contains(quiet.String(), "4 event(s) observed") {
		t.Errorf("a working store must say it is being written to:\n%s", quiet.String())
	}
}

func TestPendingProposalsAreAdvertised(t *testing.T) {
	var buf bytes.Buffer
	printLessons(&buf, nil, lessons.Health{Sightings: 2, Pending: 1, LastObserved: time.Now()}, nil)
	if !strings.Contains(buf.String(), "orion lessons pending") {
		t.Errorf("a proposal waiting for a decision was never mentioned:\n%s", buf.String())
	}
}

// A recorded lesson has to be judgeable later, which means saying when it was
// learned -- not only what it says.
func TestTheListNamesTheDateAndTheProject(t *testing.T) {
	var buf bytes.Buffer
	seen := time.Date(2026, 8, 27, 9, 0, 0, 0, time.Local)
	printLessons(&buf, []lessons.Record{{
		Lesson:   lessons.Lesson{Text: "rebase merges do not preserve ancestry", Scope: lessons.ScopeProject},
		Hits:     2,
		Projects: []string{"orion"},
		LastSeen: seen,
	}}, lessons.Health{Sightings: 2, LastObserved: seen}, nil)

	out := buf.String()
	for _, want := range []string{"2026-08-27", "orion", "rebase merges do not preserve ancestry"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}
