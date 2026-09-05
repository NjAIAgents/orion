package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/orion-sdlc/orion/internal/collect"
	"github.com/orion-sdlc/orion/internal/promote"
)

// THE GUARDRAIL THAT CANNOT BE A FLAG (OR-116).
//
// An unattended loop must not be able to cut a public release. A boolean
// checked at the top of runReleaseShip would satisfy the ticket's words and
// nothing else: the next person to refactor the call sites has no way to know
// the flag was load-bearing, and the failure is silent and public.
//
// So the property asserted is STRUCTURAL: nothing on the watch path names the
// shipping entry point, the release dispatch, or the publishing script. If
// somebody wires shipping into the watcher, this fails at the wiring rather
// than at the release.
func TestWatchHasNoPathToShipping(t *testing.T) {
	// Every file the watcher's own code lives in. cmd/orion/watch.go is the
	// command; internal/watch is the loop it runs.
	var files []string
	files = append(files, "watch.go")
	entries, err := os.ReadDir(filepath.Join("..", "..", "internal", "watch"))
	if err != nil {
		t.Fatalf("internal/watch is unreadable: %v", err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".go") && !strings.HasSuffix(e.Name(), "_test.go") {
			files = append(files, filepath.Join("..", "..", "internal", "watch", e.Name()))
		}
	}

	// The names that reach a tag. runRelease is included deliberately: it is
	// the only caller of runReleaseShip, so a watcher that could reach the
	// dispatch could reach shipping by passing one more argument.
	forbidden := []string{"runReleaseShip", "runRelease", "releaseAction", "release.sh", "releaseScript"}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		for _, name := range forbidden {
			if strings.Contains(string(b), name) {
				t.Errorf("%s names %q. An unattended loop must have no path to "+
					"cutting a release; if this is deliberate, it is a design change, "+
					"not a test to update.", f, name)
			}
		}
	}
}

// `ship` is now wired, and the other two reserved verbs deliberately are not.
//
// Keeping publish and cut unwired is what leaves slack in OR-190's guard
// rail: a typo for either lands on usage rather than on a public tag.
func TestOnlyShipIsWiredOfTheReservedPublishingVerbs(t *testing.T) {
	if got := releaseAction([]string{"ship"}); got != "ship" {
		t.Errorf(`release ship resolved to %q, want "ship"`, got)
	}
	for _, verb := range []string{"publish", "cut"} {
		if got := releaseAction([]string{verb}); got != "" {
			t.Errorf("release %s resolved to %q; only one reserved verb was spent", verb, got)
		}
	}
}

// A flag is not an action. `orion release --beta` must still be usage and a
// non-zero exit, or the guard rail is one keystroke shallower than it reads.
func TestShipFlagsAloneDoNotNameAnAction(t *testing.T) {
	for _, args := range [][]string{{"--beta"}, {"--dry-run"}, {"--beta", "--dry-run"}} {
		if got := releaseAction(args); got != "" {
			t.Errorf("release %v resolved to %q; flags must not reach an action", args, got)
		}
	}
}

// The approval message is the last thing between a person and a public tag,
// so it has to make refusing possible: what channel, what version, and every
// commit that would ship.
func TestTheReleaseApprovalNamesTheVersionAndTheWholeShipList(t *testing.T) {
	in := promote.ShipInputs{
		Channel: promote.Production, Version: "v0.9.0",
		WorkBranch: "develop", ReleaseBranch: "main",
		Delta: []string{"aaa1111 feat(OR-1): one", "bbb2222 fix(OR-2): two"},
	}
	title, body := msgReleaseApproval(in, collect.PR{URL: "https://example.test/pr/1"},
		[]string{"<@U123>"})

	if !strings.Contains(title, "RELEASE") || !strings.Contains(title, "v0.9.0") {
		t.Errorf("the title must say this is a release and name the version: %s", title)
	}
	// Every commit, not a sample. This is the one message where "and 24 more"
	// hides the commit the reader was going to object to.
	for _, c := range in.Delta {
		if !strings.Contains(body, c) {
			t.Errorf("the ship list must be complete; %q is missing:\n%s", c, body)
		}
	}
	// What approving actually costs, in the message rather than in the docs.
	for _, want := range []string{"not a ticket merge", "Homebrew tap", "Scoop bucket", "<@U123>"} {
		if !strings.Contains(body, want) {
			t.Errorf("the request must name %q so it can be refused on its own terms:\n%s",
				want, body)
		}
	}
}

// An empty approver list must not read as "anyone". ReadDecision already
// refuses that case; the message has to say so, or the reader waits for a tap
// that can never work.
func TestTheReleaseApprovalSaysWhenNobodyCanApprove(t *testing.T) {
	_, body := msgReleaseApproval(promote.ShipInputs{Version: "v0.9.0"}, collect.PR{}, nil)
	if !strings.Contains(body, "no approval can succeed") {
		t.Errorf("an empty allowlist must be stated, not implied:\n%s", body)
	}
}

// The description Orion writes itself, used whenever the describer cannot
// run. It has to carry the delta: a promotion pull request whose body says
// only "promote develop" is one a reviewer cannot review.
func TestThePromotionFallbackBodyCarriesTheWholeDelta(t *testing.T) {
	in := promote.ShipInputs{
		Version: "v0.9.0", WorkBranch: "develop", ReleaseBranch: "main",
		Delta: []string{"aaa1111 feat(OR-1): one", "bbb2222 fix(OR-2): two"},
	}
	title, body := promotionFallback(in)
	for _, want := range []string{"v0.9.0", "develop", "main"} {
		if !strings.Contains(title, want) {
			t.Errorf("the title must name %q: %s", want, title)
		}
	}
	for _, c := range in.Delta {
		if !strings.Contains(body, c) {
			t.Errorf("the body must list %q:\n%s", c, body)
		}
	}
}

// shipList is what the whole preflight is about, so it has to be the real
// range against a real repository rather than a parse of a string.
func TestShipListIsTheCommitsTheReleaseBranchLacks(t *testing.T) {
	repo := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t",
			"GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	commit := func(msg string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(repo, msg+".txt"), []byte(msg), 0o644); err != nil {
			t.Fatal(err)
		}
		git("add", ".")
		git("commit", "-q", "-m", msg)
	}
	git("init", "-q", "-b", "main")
	commit("base")
	git("checkout", "-q", "-b", "develop")
	commit("one")
	commit("two")
	// shipList reads origin/*, so the remote is this repository itself --
	// which is what the real one reads after its fetch.
	git("remote", "add", "origin", repo)
	git("fetch", "-q", "origin")

	got := shipList(repo, "main", "develop")
	if len(got) != 2 {
		t.Fatalf("want the 2 commits develop has and main does not, got %d: %v", len(got), got)
	}
	// Newest first, as git reports it, and the subject is carried so the
	// printed ship list is readable rather than a column of hashes.
	if !strings.HasSuffix(got[0], " two") || !strings.HasSuffix(got[1], " one") {
		t.Errorf("want the subjects, newest first: %v", got)
	}
	// The empty delta the preflight refuses on: nothing to promote once the
	// two branches agree.
	if got := shipList(repo, "develop", "develop"); len(got) != 0 {
		t.Errorf("a branch has nothing the same branch lacks, got %v", got)
	}
}

// runReleaseSh runs the script with an empty PATH, so it dies at the first
// `command -v gh` and can never reach the network, the tag, or -- the one
// that would be genuinely bad -- its own `go test ./...` gate from inside a
// test run. Everything asserted here happens before that point.
func runReleaseSh(t *testing.T, args ...string) (string, error) {
	t.Helper()
	if runtime.GOOS == "windows" {
		// The harness starves the script of gh by appending a second PATH
		// entry, and Windows environment blocks are case-insensitive with
		// duplicate keys resolved unpredictably -- msys bash may see either
		// value, so the starvation is not reliable there. The script is
		// POSIX bash and its logic is fully exercised on the other two legs
		// (OR-344).
		t.Skip("PATH starvation via duplicate env entries is unreliable on Windows")
	}
	script := filepath.Join("..", "..", "scripts", "release.sh")
	cmd := exec.Command("bash", append([]string{script}, args...)...)
	cmd.Env = append(os.Environ(), "PATH=/nonexistent")
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// scripts/release.sh owns the other half of the channel rule, and these
// refusals fire before it needs gh, a network or a build.
func TestTheReleaseScriptRefusesAChannelTagMismatch(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want []string
	}{
		{
			// The expensive direction: a prerelease on the channel that
			// updates the tap. `brew upgrade` would serve it.
			name: "a beta tag on the production channel",
			args: []string{"v0.9.0-beta.1"},
			want: []string{"prerelease tag", "production channel", "--beta"},
		},
		{
			name: "a production tag on the beta channel",
			args: []string{"v0.9.0", "--beta"},
			want: []string{"beta tag must look like", "semver"},
		},
		{
			name: "an unknown option is not silently ignored",
			args: []string{"v0.9.0", "--betaa"},
			want: []string{"unknown option"},
		},
		{
			name: "no version at all",
			args: []string{"--beta"},
			want: []string{"usage"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out, err := runReleaseSh(t, c.args...)
			if err == nil {
				t.Fatalf("the script must refuse and exit non-zero:\n%s", out)
			}
			// Refused on the CHANNEL rule, not merely because the sandbox has
			// no gh. Without this the whole table would pass against a script
			// that had lost its tag check entirely.
			if strings.Contains(out, "Preflight") {
				t.Fatalf("%v got past the tag/channel check:\n%s", c.args, out)
			}
			for _, want := range c.want {
				if !strings.Contains(out, want) {
					t.Errorf("the refusal must name %q:\n%s", want, out)
				}
			}
		})
	}
}

// A well-formed tag on the right channel gets PAST the shape check, so the
// table above is testing the shape rule rather than a script that refuses
// everything. It then dies for want of gh, which is where this stops caring.
//
// The preflight banner also has to name the channel and the branch it is
// about to use: that pairing is the thing this ticket added, and printing it
// before anything irreversible is how an operator catches a wrong --beta.
func TestTheReleaseScriptAcceptsAWellFormedTagOnEitherChannel(t *testing.T) {
	for _, c := range []struct {
		args   []string
		branch string
	}{
		{[]string{"v0.9.0"}, "main"},
		{[]string{"v0.9.0-beta.1", "--beta"}, "develop"},
	} {
		out, _ := runReleaseSh(t, c.args...)
		if strings.Contains(out, "must look like") || strings.Contains(out, "prerelease tag") {
			t.Errorf("%v was refused on its tag shape, which is the correct one:\n%s", c.args, out)
		}
		if !strings.Contains(out, "Preflight") {
			t.Errorf("%v never reached the preflight:\n%s", c.args, out)
		}
		if !strings.Contains(out, c.branch) {
			t.Errorf("%v never names %s, the branch this channel cuts from:\n%s",
				c.args, c.branch, out)
		}
	}
}

// The rule the whole beta channel exists to keep: the tap and the bucket are
// only ever written on the production channel.
//
// A static check, and deliberately narrow. Exercising the publish half for
// real would need a stub forge, a stub git remote and a full cross-compile;
// what this pins instead is the one edit that would break the rule -- moving
// either publish_manifest call out from behind the channel guard.
func TestTheTapAndBucketArePublishedOnlyOnTheProductionChannel(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "scripts", "release.sh"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(b), "\n")
	guarded := false
	for i, line := range lines {
		if !strings.HasPrefix(strings.TrimSpace(line), `publish_manifest "$`) {
			continue
		}
		guarded = false
		// Walk back to the nearest block opener. The call must sit in the
		// else of a beta test, i.e. on the production side.
		for j := i - 1; j >= 0 && j > i-12; j-- {
			if strings.Contains(lines[j], `if [ "$CHANNEL" = "beta" ]`) {
				t.Errorf("line %d publishes a manifest inside the beta branch: %s",
					i+1, strings.TrimSpace(line))
				return
			}
			if strings.TrimSpace(lines[j]) == "else" {
				guarded = true
				break
			}
		}
		if !guarded {
			t.Errorf("line %d publishes a manifest with no channel guard above it: %s\n"+
				"A beta reaching the tap means `brew upgrade` hands a prerelease to a "+
				"stable user, and semver never offers them a way back up.",
				i+1, strings.TrimSpace(line))
		}
	}
	if !guarded {
		t.Error("no guarded publish_manifest call found; has the publish step moved?")
	}
	if !strings.Contains(string(b), "--prerelease") {
		t.Error("the beta channel must mark the forge release as a prerelease, or " +
			"it becomes `latest` and every naive download script resolves to it")
	}
}
