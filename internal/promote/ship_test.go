package promote

import (
	"strings"
	"testing"
)

// clean is a set of inputs that must produce no refusal at all. Every test
// below starts from it and breaks exactly one thing, so a refusal that fires
// is attributable to the thing that was broken.
func clean() ShipInputs {
	return ShipInputs{
		Channel:       Production,
		Version:       "v0.9.0",
		OnBranch:      "develop",
		WorkBranch:    "develop",
		ReleaseBranch: "main",
		Delta:         []string{"abc1234 feat(OR-1): a thing"},
		HeadSHA:       "deadbeefcafe",
		BuildSHA:      "deadbeefcafe",
		BuildState:    "success",
	}
}

func TestACleanProductionPreflightRefusesNothing(t *testing.T) {
	if got := ShipRefusals(clean()); len(got) > 0 {
		t.Fatalf("a releasable state must produce no refusal, got: %s", strings.Join(got, "; "))
	}
}

func TestACleanBetaPreflightRefusesNothing(t *testing.T) {
	in := clean()
	in.Channel, in.Version = Beta, "v0.9.0-beta.1"
	if got := ShipRefusals(in); len(got) > 0 {
		t.Fatalf("a releasable beta must produce no refusal, got: %s", strings.Join(got, "; "))
	}
}

// The three named guardrails, each on its own, each naming WHICH.
//
// A refusal that says only "preflight failed" sends the operator to read the
// script. These are the three states a release is most often attempted from,
// so each has to be its own sentence.
func TestEachGuardrailRefusesAndNamesItself(t *testing.T) {
	cases := []struct {
		name   string
		break_ func(*ShipInputs)
		want   []string
	}{
		{"dirty tree", func(in *ShipInputs) { in.Dirty = true },
			[]string{"dirty"}},
		{"empty delta", func(in *ShipInputs) { in.Delta = nil },
			[]string{"nothing to ship", "develop", "main"}},
		{"red CI", func(in *ShipInputs) { in.BuildState = "failure" },
			[]string{"failure", "develop"}},
		{"no CI at all", func(in *ShipInputs) { in.BuildState = "" },
			[]string{"reported nothing"}},
		{"green build for another commit", func(in *ShipInputs) { in.BuildSHA = "0123456789ab" },
			[]string{"ran on", "would ship"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			in := clean()
			c.break_(&in)
			got := ShipRefusals(in)
			if len(got) != 1 {
				t.Fatalf("want exactly the one refusal for %s, got %d: %s",
					c.name, len(got), strings.Join(got, "; "))
			}
			for _, want := range c.want {
				if !strings.Contains(got[0], want) {
					t.Errorf("the refusal must name %q: %s", want, got[0])
				}
			}
		})
	}
}

// The refusal that stops a beta reaching a production user.
//
// This is the one worth failing loudly for: `brew upgrade` resolves the tap,
// the tap serves whatever production last published, and a prerelease tagged
// on that channel is served to everyone. It is also not self-correcting,
// because v0.9.0-beta.1 sorts BELOW v0.9.0 under semver.
func TestAPrereleaseTagIsRefusedOnTheProductionChannel(t *testing.T) {
	in := clean()
	in.Version = "v0.9.0-beta.1"
	got := ShipRefusals(in)
	if len(got) != 1 {
		t.Fatalf("want one refusal, got %d: %s", len(got), strings.Join(got, "; "))
	}
	for _, want := range []string{"v0.9.0-beta.1", "vX.Y.Z", "tap"} {
		if !strings.Contains(got[0], want) {
			t.Errorf("the refusal must name %q so the reader knows the cost: %s", want, got[0])
		}
	}
}

func TestAPlainTagIsRefusedOnTheBetaChannel(t *testing.T) {
	in := clean()
	in.Channel = Beta
	got := ShipRefusals(in)
	if len(got) != 1 || !strings.Contains(got[0], "vX.Y.Z-beta.N") {
		t.Fatalf("a beta must be refused a production-shaped tag: %s", strings.Join(got, "; "))
	}
}

// Both channels are PREPARED from the integration branch, and the refusal has
// to name the channel as well as the branches: "you are on main" is not
// actionable until the reader knows which of the two things they asked for
// wanted which branch.
func TestBranchMismatchNamesTheChannelAndBothBranches(t *testing.T) {
	for _, c := range []struct {
		ch      Channel
		version string
	}{
		{Production, "v0.9.0"},
		{Beta, "v0.9.0-beta.1"},
	} {
		in := clean()
		in.Channel, in.Version, in.OnBranch = c.ch, c.version, "main"
		got := ShipRefusals(in)
		if len(got) != 1 {
			t.Fatalf("%s: want one refusal, got %d: %s", c.ch, len(got), strings.Join(got, "; "))
		}
		for _, want := range []string{string(c.ch), "develop", "main"} {
			if !strings.Contains(got[0], want) {
				t.Errorf("%s: the refusal must name %q: %s", c.ch, want, got[0])
			}
		}
	}
}

// Every refusal at once, not the first one. An operator who fixes a dirty
// tree, re-runs, is told CI is red, fixes that, re-runs and is told there is
// nothing to ship has paid three times for one look at the same state.
func TestEveryRefusalIsReportedTogether(t *testing.T) {
	in := clean()
	in.Dirty = true
	in.Delta = nil
	in.BuildState = "failure"
	in.OnBranch = "some/feature"
	if got := ShipRefusals(in); len(got) != 4 {
		t.Fatalf("want all 4 refusals in one pass, got %d: %s", len(got), strings.Join(got, "; "))
	}
}

func TestAnUnknownChannelIsRefusedRatherThanTreatedAsProduction(t *testing.T) {
	in := clean()
	in.Channel = "nightly"
	got := ShipRefusals(in)
	if len(got) == 0 || !strings.Contains(got[0], "not a release channel") {
		t.Fatalf("an unrecognised channel must be refused, not defaulted: %s",
			strings.Join(got, "; "))
	}
}

// CutFrom is where the two channels differ in what they publish FROM, and it
// is the value scripts/release.sh enforces on the other side.
func TestCutFromNamesTheChannelsOwnBranch(t *testing.T) {
	if got := CutFrom(Production, "develop", "main"); got != "main" {
		t.Errorf("production is cut from the release branch, got %q", got)
	}
	if got := CutFrom(Beta, "develop", "main"); got != "develop" {
		t.Errorf("a beta is cut from the integration branch, got %q", got)
	}
}

func TestNextBetaCountsUpPastTheExistingOnes(t *testing.T) {
	tags := []string{"v0.8.0", "v0.9.0-beta.1", "v0.9.0-beta.2", "v0.10.0-beta.7", "junk"}
	if got := NextBeta("v0.9.0", tags); got != "v0.9.0-beta.3" {
		t.Errorf("want v0.9.0-beta.3, got %q", got)
	}
	// A base nobody has cut a beta of starts at 1, even in a repository full
	// of betas for OTHER versions.
	if got := NextBeta("v1.0.0", tags); got != "v1.0.0-beta.1" {
		t.Errorf("want v1.0.0-beta.1, got %q", got)
	}
	// An explicit beta is honoured, so a specific prerelease can be re-cut by
	// naming it in full rather than being silently bumped past.
	if got := NextBeta("v0.9.0-beta.2", tags); got != "v0.9.0-beta.2" {
		t.Errorf("an explicit beta must not be renumbered, got %q", got)
	}
	// beta.10 must beat beta.9: string ordering would not, and the tenth
	// prerelease of a version is not a hypothetical.
	if got := NextBeta("v2.0.0", []string{"v2.0.0-beta.9", "v2.0.0-beta.10"}); got != "v2.0.0-beta.11" {
		t.Errorf("want v2.0.0-beta.11, got %q", got)
	}
}

func TestPrereleaseRecognisesTheSuffix(t *testing.T) {
	if Prerelease("v1.2.3") {
		t.Error("a plain version is not a prerelease")
	}
	if !Prerelease("v1.2.3-beta.1") || !Prerelease("v1.2.3-rc.1") {
		t.Error("any hyphenated tag sorts below its base and is a prerelease")
	}
}
