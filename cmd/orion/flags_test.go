package main

import (
	"strings"
	"testing"
)

// Flag parsing had no test, and it shipped a bug that a single real
// invocation found immediately:
//
//	orion watch fcia --dry-run --max-jobs 1
//	  scope     FCIA, 1
//
// The `1` belonging to --max-jobs was read as a second PROJECT. Orion then
// searched a project called "1", found nothing, and reported it exactly as
// it would report an empty queue.
//
// The shape of the bug is general: any flag that consumes the next argument
// leaves that argument looking like a positional to a naive filter.
func TestAFlagsValueIsNotMistakenForAPositional(t *testing.T) {
	valued := []string{"--interval", "--max-jobs", "--max-minutes", "--max-turns"}

	for _, tc := range []struct {
		name string
		args []string
		want []string
	}{
		{"the bug, verbatim",
			[]string{"fcia", "--dry-run", "--max-jobs", "1"}, []string{"fcia"}},
		{"value before the positional",
			[]string{"--max-jobs", "2", "fcia"}, []string{"fcia"}},
		{"equals form keeps its own value",
			[]string{"--max-jobs=2", "fcia"}, []string{"fcia"}},
		{"several valued flags",
			[]string{"--interval", "60", "fcia", "--max-turns", "90"}, []string{"fcia"}},
		{"boolean flags consume nothing",
			[]string{"--dry-run", "fcia", "--once"}, []string{"fcia"}},
		{"two real projects still both arrive",
			[]string{"fcia", "orion", "--max-jobs", "1"}, []string{"fcia", "orion"}},
		{"no positionals at all",
			[]string{"--max-jobs", "1"}, nil},
	} {
		got := positional(tc.args, valued...)
		if strings.Join(got, ",") != strings.Join(tc.want, ",") {
			t.Errorf("%s: positional(%v) = %v, want %v", tc.name, tc.args, got, tc.want)
		}
	}
}

// A boolean flag must not be listed as valued, or the positional after it is
// swallowed. Worth pinning because the failure is silent: the project simply
// disappears from the scope and the watcher watches everything instead.
func TestABooleanFlagDoesNotSwallowTheNextArgument(t *testing.T) {
	got := positional([]string{"--dry-run", "fcia"}, "--max-jobs")
	if len(got) != 1 || got[0] != "fcia" {
		t.Fatalf("positional = %v, want [fcia]", got)
	}
}
