package main

import "testing"

// `orion new` writes "From PRIOR-3" at the top of a project's description, so
// a stage can be told which idea belongs to this work.
func TestTheIdeaKeyIsReadFromTheProvenanceMarker(t *testing.T) {
	for _, tc := range []struct{ desc, want string }{
		{"From PRIOR-3\n\nthe rest", "PRIOR-3"},
		{"From PRIOR-3 (https://x/browse/PRIOR-3)\n\nbody", "PRIOR-3"},
		{"  From prior-12  \n\nbody", "PRIOR-12"},

		// Not a marker. A description mentioning a ticket in passing is not a
		// statement about provenance, and pointing a stage at it would fill
		// in the wrong idea.
		{"We are replacing the tool From PRIOR-3 onwards", ""},
		{"Nothing to do with ideas", ""},
		{"", ""},
		{"From nowhere in particular", ""},
		{"From ", ""},
		{"see PRIOR-3\nFrom PRIOR-4", ""}, // only the first line counts
	} {
		if got := ideaKeyFromDescription(tc.desc); got != tc.want {
			t.Errorf("ideaKeyFromDescription(%q) = %q, want %q", tc.desc, got, tc.want)
		}
	}
}
