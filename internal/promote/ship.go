package promote

// The two release CHANNELS, and the preflight that refuses to cut either one
// from the wrong place (OR-116).
//
// Verify (promote.go) answers "is this milestone safe to promote" from the
// tracker's point of view. This answers the narrower question the shipping
// command asks first: is this WORKING COPY in a state from which a release
// can be cut at all, and is the channel being asked for the channel this
// branch may publish to.
//
// THE TWO CHANNELS ARE NOT SYMMETRICAL, and the asymmetry is the point:
//
//	develop -> beta        a prerelease. No promotion, no tap, no bucket.
//	main    -> production  the full promotion, and the only thing an
//	                       installed `brew upgrade` may ever be handed.
//
// So a production tag carrying a prerelease suffix is refused here rather
// than discovered by a stable user receiving a beta -- which is a failure
// nobody can take back, because the tap has already served it.
//
// Every refusal is collected rather than returned one at a time. An operator
// who fixes a dirty tree, re-runs, and is then told CI is red has been made
// to pay twice for one look at the same state.

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Channel is what a release is published as.
type Channel string

const (
	// Production is cut from the release branch and updates the tap and the
	// bucket. The one an installed `brew upgrade` can reach.
	Production Channel = "production"
	// Beta is cut from the integration branch, marked prerelease on the
	// forge, and touches no installer.
	Beta Channel = "beta"
)

// ShipInputs is everything the preflight needs, gathered by the caller.
//
// Gathering is separate from deciding for the same reason Verify's is: the
// refusals are then assertable without a repository, a forge or a network,
// which is what makes it possible to test the ones that must never fire.
type ShipInputs struct {
	Channel Channel
	// Version is the tag that would be created.
	Version string

	// OnBranch is the branch checked out where the command was run. Both
	// channels are PREPARED from the integration branch: a beta is cut from
	// it directly, and a production release starts by promoting it.
	OnBranch      string
	WorkBranch    string
	ReleaseBranch string

	Dirty bool
	// Delta is the commit list this release would ship -- the release
	// branch's tip up to the integration branch's. Empty means there is
	// nothing to release, on either channel.
	Delta []string

	// HeadSHA is the integration branch's tip; BuildSHA is the commit CI
	// actually ran on and BuildState its conclusion, as the forge gave them.
	HeadSHA    string
	BuildSHA   string
	BuildState string
}

var (
	productionTag = regexp.MustCompile(`^v\d+\.\d+\.\d+$`)
	betaTag       = regexp.MustCompile(`^v\d+\.\d+\.\d+-beta\.\d+$`)
)

// CutFrom names the branch a channel's tag is cut FROM, which is not the
// branch the command is run from for a production release: the promotion
// moves the work onto the release branch first, and the tag follows it there.
func CutFrom(ch Channel, work, release string) string {
	if ch == Beta {
		return work
	}
	return release
}

// Prerelease reports whether a tag sorts below the plain version it is built
// from -- which under semver is any tag carrying a hyphen.
//
// Used to keep a beta out of the production channel. `v0.9.0-beta.1` is
// LOWER than `v0.9.0`, so a stable user who somehow received it would then
// be offered a downgrade-shaped upgrade forever.
func Prerelease(tag string) bool { return strings.Contains(tag, "-") }

// NextBeta turns a base version into the next unused beta tag for it.
//
// A base that already names a beta is returned untouched, so an operator can
// re-cut a specific prerelease by naming it in full. existing is the tag list
// as git reports it; anything that is not a beta of this base is ignored,
// which is why a repository full of production tags still starts at beta.1.
func NextBeta(base string, existing []string) string {
	if Prerelease(base) {
		return base
	}
	prefix := base + "-beta."
	highest := 0
	for _, t := range existing {
		n, err := strconv.Atoi(strings.TrimPrefix(strings.TrimSpace(t), prefix))
		if err != nil || !strings.HasPrefix(strings.TrimSpace(t), prefix) {
			continue
		}
		if n > highest {
			highest = n
		}
	}
	return fmt.Sprintf("%s%d", prefix, highest+1)
}

// ShipRefusals lists every reason this release must not be cut. Empty is a go.
func ShipRefusals(in ShipInputs) []string {
	var out []string

	switch in.Channel {
	case Production:
		if !productionTag.MatchString(in.Version) {
			out = append(out, fmt.Sprintf(
				"a production release must be tagged vX.Y.Z, and %q is not. "+
					"A prerelease tag on the production channel reaches the tap, and "+
					"`brew upgrade` would hand a beta to a stable user", in.Version))
		}
	case Beta:
		if !betaTag.MatchString(in.Version) {
			out = append(out, fmt.Sprintf(
				"a beta must be tagged vX.Y.Z-beta.N so it sorts below vX.Y.Z under "+
					"semver, and %q does not", in.Version))
		}
	default:
		out = append(out, fmt.Sprintf(
			"%q is not a release channel; it is %s or %s", in.Channel, Production, Beta))
	}

	// Both channels are prepared from the integration branch, and the
	// refusal names the channel as well as the branches -- "you are on
	// main" is not actionable until the reader knows which of the two
	// things they asked for wanted which branch.
	if in.OnBranch != in.WorkBranch {
		switch in.Channel {
		case Beta:
			out = append(out, fmt.Sprintf(
				"a %s is cut from the integration branch %s only; you are on %s",
				Beta, in.WorkBranch, in.OnBranch))
		default:
			out = append(out, fmt.Sprintf(
				"a %s release promotes %s onto %s, so it is prepared from %s; you are on %s",
				Production, in.WorkBranch, in.ReleaseBranch, in.WorkBranch, in.OnBranch))
		}
	}

	if in.Dirty {
		out = append(out, "the working tree is dirty; a release must be reproducible from a commit")
	}

	if len(in.Delta) == 0 {
		out = append(out, fmt.Sprintf(
			"%s has nothing %s lacks, so there is nothing to ship",
			in.WorkBranch, in.ReleaseBranch))
	}

	// Green ON THE EXACT COMMIT. The three failures are named separately
	// because they mean different things: no build at all, a build that
	// failed, and a green build that tested something other than the code
	// about to ship.
	switch {
	case in.BuildState == "":
		out = append(out, fmt.Sprintf("CI has reported nothing for %s on %s",
			short(in.HeadSHA), in.WorkBranch))
	case !strings.EqualFold(in.BuildState, "success"):
		out = append(out, fmt.Sprintf("CI on %s reported %s for %s",
			in.WorkBranch, in.BuildState, short(in.BuildSHA)))
	case in.BuildSHA != in.HeadSHA:
		out = append(out, fmt.Sprintf(
			"the green build on %s ran on %s, and this would ship %s",
			in.WorkBranch, short(in.BuildSHA), short(in.HeadSHA)))
	}

	return out
}
