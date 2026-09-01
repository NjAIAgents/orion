package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// OR-255. scripts/release.sh had three independent guards keeping a beta out
// of the Homebrew tap, and .github/workflows/release.yml had none of them: a
// free-text tag input, no beta concept anywhere in the file, and an
// unconditional push of the rendered formula to the tap. Dispatching it with
// v0.9.0-beta.1 would have handed a prerelease to every stable user's next
// `brew upgrade`.
//
// Static checks, like TestTheTapAndBucketArePublishedOnlyOnTheProductionChannel
// next door and for the same reason: running the publish half for real needs a
// stub forge, a stub git remote and a full cross-compile. What these pin is the
// edit that would put the hole back.

func repoFile(t *testing.T, parts ...string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(append([]string{"..", ".."}, parts...)...))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestTheReleaseWorkflowPublishesPackagesOnlyOnTheProductionChannel(t *testing.T) {
	wf := repoFile(t, ".github", "workflows", "release.yml")

	if !strings.Contains(wf, "if: needs.build.outputs.channel == 'production'") {
		t.Error("publish-packages has no production-channel condition, so a dispatched " +
			"beta tag would push a prerelease formula to the Homebrew tap and the Scoop " +
			"bucket. `brew upgrade` would hand it to stable users, and semver never " +
			"offers them a way back up.")
	}

	// The condition is worthless if the job it guards is not the one that
	// writes to the tap.
	guard := strings.Index(wf, "if: needs.build.outputs.channel == 'production'")
	tap := strings.Index(wf, "homebrew-tap.git")
	if guard < 0 || tap < 0 || guard > tap {
		t.Errorf("the channel guard does not precede the tap push (guard at %d, tap at %d)", guard, tap)
	}

	if !strings.Contains(wf, "tag-channel.sh") {
		t.Error("the workflow does not derive a channel from the tag; a workflow_dispatch " +
			"input is free text, so without this nothing in the file knows what a beta is")
	}
	if !strings.Contains(wf, "--prerelease") {
		t.Error("a beta release is not marked prerelease here, so it becomes `latest` and " +
			"every naive download script resolves to it")
	}
}

// The tag reaches the shell through the environment, never spliced into the
// script body by ${{ }}. It is text a dispatcher types.
func TestTheDispatchedTagIsNotInterpolatedIntoAShellScript(t *testing.T) {
	wf := repoFile(t, ".github", "workflows", "release.yml")
	for i, line := range strings.Split(wf, "\n") {
		if !strings.Contains(line, "inputs.tag") {
			continue
		}
		// A comment is prose about the rule, not a use of it -- including the
		// comment that explains why this rule exists.
		if strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		// An env: binding is the safe form. A bare use inside a run: block is
		// not, and this file used to have one.
		if strings.Contains(line, "TAG:") || strings.Contains(line, "description") ||
			strings.Contains(line, "required") {
			continue
		}
		t.Errorf("line %d uses inputs.tag outside an env binding: %s", i+1, strings.TrimSpace(line))
	}
}

// One definition of the shapes, exercised. release.sh is TOLD its channel and
// checks the tag agrees; the workflow is given only a tag and must derive one.
// Different questions, same shapes -- and the classifier is where they meet.
func TestTagChannelClassifiesEveryShapeAndRefusesTheRest(t *testing.T) {
	script := filepath.Join("..", "..", "scripts", "tag-channel.sh")
	if _, err := os.Stat(script); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		tag  string
		want string // "" means it must refuse
	}{
		{"v1.2.3", "production"},
		{"v0.8.9", "production"},
		{"v10.20.30", "production"},
		{"v1.2.3-beta.4", "beta"},
		{"v0.9.0-beta.1", "beta"},
		{"v1.2.3-rc.1", ""},   // a prerelease that is not a beta
		{"v1.2.3-alpha", ""},  // ditto
		{"1.2.3", ""},         // no v
		{"v1.2", ""},          // not three parts
		{"main", ""},          // not a tag at all
		{"", ""},              // nothing
		{"v1.2.3-beta", ""},   // no beta number, would not sort
		{"v1.2.3-beta.x", ""}, // not a number
	} {
		out, err := exec.Command("sh", script, tc.tag).Output()
		got := strings.TrimSpace(string(out))
		switch {
		case tc.want == "" && err == nil:
			t.Errorf("tag-channel.sh %q returned %q; a tag that names no channel must "+
				"refuse rather than be guessed at", tc.tag, got)
		case tc.want != "" && err != nil:
			t.Errorf("tag-channel.sh %q refused a valid %s tag: %v", tc.tag, tc.want, err)
		case tc.want != "" && got != tc.want:
			t.Errorf("tag-channel.sh %q = %q, want %q", tc.tag, got, tc.want)
		}
	}
}

// release.sh must keep asking the classifier rather than growing its own copy
// of the shapes back.
func TestReleaseScriptUsesTheSharedClassifier(t *testing.T) {
	sh := repoFile(t, "scripts", "release.sh")
	if !strings.Contains(sh, "tag-channel.sh") {
		t.Error("release.sh no longer uses scripts/tag-channel.sh, so the release " +
			"workflow and the release script now define `is this a beta` separately; " +
			"they drift, and the drift ships a prerelease to the tap")
	}
	// Its two wordings are the reason the comparison stayed here rather than
	// moving into the classifier. Losing them turns a channel mistake into a
	// tag-shape complaint.
	if !strings.Contains(sh, "Pass --beta to cut it as one") {
		t.Error("the production-channel refusal no longer tells the caller which flag " +
			"they meant to pass")
	}
}
