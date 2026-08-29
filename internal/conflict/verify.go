package conflict

import (
	"fmt"
	"path/filepath"
	"strings"
)

// Markers git-grep looks for. Anchored at the start of a line, because these
// sequences appear legitimately inside a diff fixture or a test asserting on
// them, and an unanchored match would flag this package's own tests.
var markerPatterns = []string{"^<<<<<<< ", "^=======$", "^>>>>>>> "}

// Verify checks a resolved tree against the two sides it came from.
//
//	dir     a git worktree with the resolution already committed or staged
//	base    the merge base
//	ours    the commit for one side
//	theirs  the commit for the other
//
// It never writes, never pushes and never decides. It answers one question --
// is there evidence a side was dropped -- and the caller chooses what to do.
func Verify(dir, base, ours, theirs string) (Report, error) {
	var r Report

	if err := findMarkers(dir, &r); err != nil {
		return r, err
	}
	if err := findLitter(dir, &r); err != nil {
		return r, err
	}
	if err := findDroppedSides(dir, base, ours, theirs, &r); err != nil {
		return r, err
	}
	return r, nil
}

// findMarkers catches the resolution that was never finished. Cheap, and the
// one failure that is unambiguous: a marker in the tree is never correct.
func findMarkers(dir string, r *Report) error {
	for _, pat := range markerPatterns {
		out, err := git(dir, "grep", "-n", "-E", pat, "--", ".")
		// git grep exits 1 for "no matches", which is the good case here and
		// not an error. Anything else is left to the next check rather than
		// failing the whole verification on one grep.
		if err != nil && out == "" {
			continue
		}
		for _, line := range lines(out) {
			file := line
			if i := strings.Index(line, ":"); i > 0 {
				file = line[:i]
			}
			r.Markers = append(r.Markers, Finding{
				File: file,
				Why:  "an unresolved conflict marker is still in the tree",
			})
		}
	}
	return nil
}

// findLitter catches .orig and .rej files, which mean a merge tool wrote its
// working state into the repository and nobody cleared it.
func findLitter(dir string, r *Report) error {
	out, err := git(dir, "status", "--porcelain", "--untracked-files=all")
	if err != nil {
		return fmt.Errorf("reading the state of %s: %s", dir, out)
	}
	for _, line := range lines(out) {
		if len(line) < 4 {
			continue
		}
		path := strings.TrimSpace(line[2:])
		switch filepath.Ext(path) {
		case ".orig", ".rej":
			r.Litter = append(r.Litter, Finding{
				File: path,
				Why:  "merge-tool leftovers were committed or left in the worktree",
			})
		}
	}
	return nil
}

// findDroppedSides is the check the ticket exists for.
//
// For every file BOTH sides changed, compare the resolved contents against
// each side. Matching one exactly means the other side's edit is not present.
// Sometimes that is right -- one change superseded the other -- but it cannot
// be distinguished from a loss by inspection of the result alone, so it is
// reported with the specific claim to check.
//
// A file only ONE side touched is not interesting: there was no conflict to
// resolve, and matching that side is simply the change being carried through.
func findDroppedSides(dir, base, ours, theirs string, r *Report) error {
	ourChanged, err := changedFiles(dir, base, ours)
	if err != nil {
		return err
	}
	theirChanged, err := changedFiles(dir, base, theirs)
	if err != nil {
		return err
	}

	touchedByThem := make(map[string]bool, len(theirChanged))
	for _, f := range theirChanged {
		touchedByThem[f] = true
	}

	for _, f := range ourChanged {
		if !touchedByThem[f] {
			continue
		}
		got, ok := blob(dir, "HEAD", f)
		if !ok {
			// Deleted in the resolution while both sides were editing it.
			// That is a bigger claim than a dropped hunk, so it is always
			// worth a person's attention.
			r.MatchedOneSide = append(r.MatchedOneSide, Finding{
				File: f,
				Why:  "both sides changed this file and the resolution deletes it",
			})
			continue
		}
		oursBlob, okOurs := blob(dir, ours, f)
		theirsBlob, okTheirs := blob(dir, theirs, f)

		if okOurs && got == oursBlob {
			r.MatchedOneSide = append(r.MatchedOneSide, Finding{
				File: f,
				Why: fmt.Sprintf("both sides changed this file, and the result is byte-identical "+
					"to %s -- so %s's edit is not in it", short(ours), short(theirs)),
			})
			continue
		}
		if okTheirs && got == theirsBlob {
			r.MatchedOneSide = append(r.MatchedOneSide, Finding{
				File: f,
				Why: fmt.Sprintf("both sides changed this file, and the result is byte-identical "+
					"to %s -- so %s's edit is not in it", short(theirs), short(ours)),
			})
		}
	}
	return nil
}

func short(ref string) string {
	if len(ref) > 8 && !strings.ContainsAny(ref, "/-") {
		return ref[:8]
	}
	return ref
}
