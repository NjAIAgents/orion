package fanout

// The same question Validate asks, one axis over.
//
// Validate answers "may these Go packages be WRITTEN at once", on the real
// import graph, for one ticket's implementation fan. This answers "may these
// TICKETS be worked at once", on the scope each one declared at planning time.
// Different inputs, identical contract: the safe outcome is serial, every
// rejection produces one, and Reason is the sentence a reader is shown.
//
// IT LIVES HERE RATHER THAN IN THE QUEUE because there must be exactly one
// implementation of "can these be worked at once" (OR-260). Two would drift,
// and the drift shows up as a branch ejected at assembly time for a reason
// nobody can reconstruct -- which is the failure this whole axis exists to
// make rarer.
//
// WHAT A DECLARED SCOPE IS NOT. It is a prediction made before the work is
// done, so it is wrong sometimes, and an agent that finds the real fix in a
// fourth file is doing its job. Nothing here is a substitute for the
// assembly-time check that ejects a branch which will not merge: this reduces
// how often that fires and must never be trusted instead of it.
//
// UNKNOWN IS NOT CONFLICT. A ticket that declares nothing is held back by
// nothing -- the same rule OR-243's queue manager and OR-95's dependency links
// both keep. A gate that treated silence as collision would stop the queue on
// the tickets nobody has got round to describing yet, which is most of them.

import "strings"

// Scope is the ground a ticket declared it expects to touch: packages,
// directories or files, as planning wrote them down.
type Scope struct {
	// Key names the ticket, for the sentence a collision produces.
	Key string
	// Paths are the declared entries, in the order they were declared. Any
	// spelling is accepted -- ./internal/a, internal/a/, internal/a -- and
	// normalised here rather than at every call site.
	Paths []string
}

// Overlap returns the ground two scopes share, narrowest form first-seen
// order, or nothing when they are independent.
//
// A path overlaps another when they are EQUAL or when one CONTAINS the other.
// internal/watch and internal/watch/watch.go are the same ground written at
// two grains; a check that compared the two as strings would call them
// independent and then eject the branch at assembly time, which is exactly the
// discovery this moves earlier.
//
// The shared entry reported is the NARROWER of the pair, because that is the
// ground both tickets actually named. Saying "internal/watch" when one ticket
// only ever claimed one file in it overstates the collision to whoever has to
// judge it.
func Overlap(a, b Scope) []string {
	as, bs := cleanPaths(a.Paths), cleanPaths(b.Paths)
	var out []string
	seen := map[string]bool{}
	for _, p := range as {
		for _, q := range bs {
			shared := ""
			switch {
			case contains(p, q):
				shared = q
			case contains(q, p):
				shared = p
			default:
				continue
			}
			if seen[shared] {
				continue
			}
			seen[shared] = true
			out = append(out, shared)
		}
	}
	return out
}

// Independent decides whether a candidate ticket may be worked alongside the
// ones already spoken for -- admitted this pass, or still in flight from an
// earlier one.
//
// The FIRST collision decides, and the admitted set is walked in order, so the
// reason a reader gets is the same every time for the same input. A
// non-serial verdict leaves Packages empty: that field is Validate's list of
// resolved import paths and means nothing on this axis.
func Independent(candidate Scope, spokenFor []Scope) Verdict {
	if len(cleanPaths(candidate.Paths)) == 0 {
		// Not a refusal and not an admission on the merits: there is simply no
		// prediction to judge, and inventing one would be the gate deciding
		// what planning declined to say.
		return Verdict{}
	}
	for _, other := range spokenFor {
		shared := Overlap(candidate, other)
		if len(shared) == 0 {
			continue
		}
		return serial("%s is already spoken for by %s; two tickets that declare the same "+
			"ground collide when their branches meet, and that is knowable now rather than "+
			"at assembly time with the agent run and the tokens already spent",
			strings.Join(shared, ", "), other.Key)
	}
	return Verdict{}
}

// contains reports whether parent is the same ground as child, or encloses it.
func contains(parent, child string) bool {
	return parent == child || strings.HasPrefix(child, parent+"/")
}

// cleanPaths normalises and de-duplicates, dropping anything that is not a
// prediction.
//
// A scope of "." or "/" -- the whole repository -- is dropped rather than
// treated as colliding with everything. Nobody predicts that in good faith:
// it is what a description says when it has not thought about scope, and
// reading it as a universal collision would hold the queue on a sentence.
func cleanPaths(in []string) []string {
	out := make([]string, 0, len(in))
	seen := map[string]bool{}
	for _, p := range in {
		p = cleanPath(p)
		if p == "" || seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, p)
	}
	return out
}

func cleanPath(p string) string {
	p = strings.ReplaceAll(strings.TrimSpace(p), `\`, "/")
	p = strings.TrimSpace(strings.Trim(p, "`\"'"))
	p = strings.TrimPrefix(p, "./")
	p = strings.TrimSuffix(p, "/")
	if p == "." || p == "/" || p == ".." {
		return ""
	}
	return p
}

// Declared reports whether this scope says anything a decision can be made on.
func (s Scope) Declared() bool { return len(cleanPaths(s.Paths)) > 0 }
