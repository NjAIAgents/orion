package main

import (
	"bytes"
	"strings"
	"testing"
)

// The four outcomes, and why they are four and not a count. A ticket already
// on ANOTHER version is being MOVED, and a bare "9 updated" would hide which
// nine tickets left which milestone.
func TestPlanFixVersionSeparatesAddMoveAlreadyAndMissing(t *testing.T) {
	keys := []string{"OR-100", "OR-105", "OR-133", "OR-999"}
	current := map[string][]string{
		"OR-100": nil,        // no milestone at all -> add
		"OR-105": {"v0.8.2"}, // a different milestone -> move
		"OR-133": {"v0.8.3"}, // already there -> untouched
		// OR-999 is absent from the map: no such ticket.
	}

	p := planFixVersion("v0.8.3", keys, current)

	if strings.Join(p.Add, ",") != "OR-100" {
		t.Errorf("Add = %v, want [OR-100]", p.Add)
	}
	if len(p.Move) != 1 || p.Move[0].Key != "OR-105" ||
		strings.Join(p.Move[0].From, ",") != "v0.8.2" {
		t.Errorf("Move = %+v, want OR-105 leaving v0.8.2", p.Move)
	}
	if strings.Join(p.Already, ",") != "OR-133" {
		t.Errorf("Already = %v, want [OR-133]", p.Already)
	}
	if strings.Join(p.Missing, ",") != "OR-999" {
		t.Errorf("Missing = %v, want [OR-999]", p.Missing)
	}
	if p.writes() != 2 {
		t.Errorf("writes() = %d, want 2 (the add and the move only)", p.writes())
	}
}

// A ticket on no milestone is NOT the same answer as a ticket that does not
// exist, and collapsing them would report a real ticket as a typo -- or worse,
// silently skip writing to it.
func TestPlanFixVersionTellsNoMilestoneFromNoTicket(t *testing.T) {
	p := planFixVersion("v0.8.3", []string{"OR-1", "OR-2"},
		map[string][]string{"OR-1": nil})

	if strings.Join(p.Add, ",") != "OR-1" {
		t.Errorf("a real ticket carrying no milestone was not queued as an add: %+v", p)
	}
	if strings.Join(p.Missing, ",") != "OR-2" {
		t.Errorf("a key with no ticket behind it was not reported missing: %+v", p)
	}
}

// The property that makes this callable from a retry: re-running writes
// nothing. Every key already on the target milestone means zero writes.
func TestPlanFixVersionIsANoOpOnARerun(t *testing.T) {
	p := planFixVersion("v0.8.3", []string{"OR-100", "OR-105"},
		map[string][]string{
			"OR-100": {"v0.8.3"},
			"OR-105": {"v0.8.3"},
		})

	if p.writes() != 0 {
		t.Errorf("a re-run would write %d ticket(s); it must change nothing: %+v", p.writes(), p)
	}
	if len(p.Already) != 2 {
		t.Errorf("Already = %v, want both keys reported as already there", p.Already)
	}
}

// A ticket carrying the target AND another milestone is already there. Writing
// would strip the other one, which a re-run must not do behind the operator's
// back.
func TestPlanFixVersionLeavesATicketThatAlreadyCarriesTheTarget(t *testing.T) {
	p := planFixVersion("v0.8.3", []string{"OR-100"},
		map[string][]string{"OR-100": {"v0.8.2", "v0.8.3"}})

	if p.writes() != 0 || strings.Join(p.Already, ",") != "OR-100" {
		t.Errorf("a ticket already on the target was queued for a write: %+v", p)
	}
}

// --project is inferred from the keys, because the keys say it. The flag still
// scopes the lookup when given, and a flag that CONTRADICTS the keys is
// refused rather than silently winning.
func TestProjectForKeysInfersFromTheKeys(t *testing.T) {
	for _, tc := range []struct {
		name    string
		flag    string
		keys    []string
		want    string
		wantErr []string
	}{
		{"inferred with no flag", "", []string{"OR-1", "OR-2"}, "OR", nil},
		{"an agreeing flag", "OR", []string{"OR-1"}, "OR", nil},
		{"a lowercase agreeing flag", "or", []string{"OR-1"}, "OR", nil},
		{"a contradicting flag", "FCIA", []string{"OR-1"}, "",
			[]string{"FCIA", "OR", "does not match"}},
		{"keys spanning two projects", "", []string{"OR-1", "FCIA-2"}, "",
			[]string{"two projects", "OR", "FCIA"}},
		{"no keys", "", nil, "", []string{"no tickets"}},
	} {
		got, err := projectForKeys(tc.flag, tc.keys)
		if tc.wantErr == nil {
			if err != nil {
				t.Errorf("%s: projectForKeys(%q, %v) errored: %v", tc.name, tc.flag, tc.keys, err)
			} else if got != tc.want {
				t.Errorf("%s: projectForKeys(%q, %v) = %q, want %q",
					tc.name, tc.flag, tc.keys, got, tc.want)
			}
			continue
		}
		if err == nil {
			t.Errorf("%s: projectForKeys(%q, %v) = %q, want a refusal",
				tc.name, tc.flag, tc.keys, got)
			continue
		}
		for _, want := range tc.wantErr {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("%s: refusal %q does not say %q", tc.name, err, want)
			}
		}
	}
}

// The preview is what makes a range safe to type: it has to list the tickets
// the range expanded to, in all four states, before anything is written.
func TestPrintFixPlanListsEveryOutcome(t *testing.T) {
	var buf bytes.Buffer
	printFixPlan(&buf, "OR", "v0.8.3", fixPlan{
		Add:     []string{"OR-100"},
		Move:    []fixMove{{Key: "OR-105", From: []string{"v0.8.2"}}},
		Already: []string{"OR-133"},
		Missing: []string{"OR-999"},
	})

	out := buf.String()
	for _, want := range []string{
		"v0.8.3", "OR-100", "OR-105", "v0.8.2", "OR-133", "OR-999",
		"add", "move", "already", "no such ticket",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the preview never mentions %q, so it cannot be read before "+
				"the write:\n%s", want, out)
		}
	}
}
