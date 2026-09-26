package main

// OR-485: when protection fails only because a private repository needs a paid
// plan, the remote step offers to make it public -- asked, default no, and
// acted on only for a yes.

import (
	"bytes"
	"strings"
	"testing"

	"github.com/orion-sdlc/orion/internal/provision"
	"github.com/orion-sdlc/orion/internal/workspace"
)

const paid = "NOT APPLIED: branch protection needs a paid plan for private repositories"

// stubRemote returns a result whose protection failed with the given reason,
// and records whether makePublicFn ran.
func stubRemote(t *testing.T, reason string) *bool {
	t.Helper()
	oRemote, oPublic, oSettings := remoteFn, makePublicFn, ensureRepoSettingsFn
	remoteFn = func(opts provision.Options) (*provision.Result, error) {
		return &provision.Result{RemoteURL: "https://github.com/test/" + opts.Name + ".git",
			Protection: map[string]string{opts.DefaultBranch: reason, opts.WorkBranch: reason}}, nil
	}
	madePublic := false
	makePublicFn = func(opts provision.Options, res *provision.Result) error {
		madePublic = true
		res.Protection = map[string]string{opts.DefaultBranch: "applied", opts.WorkBranch: "applied"}
		res.Warnings = nil
		return nil
	}
	ensureRepoSettingsFn = func(string) {}
	t.Cleanup(func() { remoteFn, makePublicFn, ensureRepoSettingsFn = oRemote, oPublic, oSettings })
	return &madePublic
}

func remoteWS(t *testing.T) *workspace.Workspace {
	t.Helper()
	return chainWS(t)
}

func TestYesMakesItPublicAndTheStepCompletes(t *testing.T) {
	madePublic := stubRemote(t, paid)
	var asked string
	var out bytes.Buffer
	err := remoteStep(&stepIO{Out: &out, Confirm: func(q string) bool { asked = q; return true }}, remoteWS(t))
	if err != nil {
		t.Fatalf("step should complete once protection applies: %v", err)
	}
	if !*madePublic {
		t.Fatal("a yes did not make the repository public")
	}
	for _, want := range []string{"PUBLIC", "full history", "intent, spec, plan"} {
		if !strings.Contains(asked, want) {
			t.Errorf("the question does not say what going public exposes (%q):\n%s", want, asked)
		}
	}
}

// A no keeps the repository private with both branches and no protection --
// and that is a completed step with a plain warning, not "remote did not
// finish": the operator chose it, and the plan cannot do otherwise.
func TestNoKeepsItPrivateUnprotectedAndCompletes(t *testing.T) {
	madePublic := stubRemote(t, paid)
	var out bytes.Buffer
	w := remoteWS(t)
	err := remoteStep(&stepIO{Out: &out, Confirm: func(string) bool { return false }}, w)
	if *madePublic {
		t.Fatal("visibility changed without a yes")
	}
	if err != nil {
		t.Fatalf("declining to go public stopped the chain: %v", err)
	}
	if !strings.Contains(out.String(), "NOT protected") {
		t.Errorf("the unprotected branches were not reported:\n%s", out.String())
	}
	if w.Task.Remote == "" {
		t.Error("the remote was not recorded")
	}
}

// With nobody to ask, the same: private, unprotected, complete.
func TestNonInteractiveKeepsItPrivateAndCompletes(t *testing.T) {
	madePublic := stubRemote(t, paid)
	err := remoteStep(&stepIO{Out: &bytes.Buffer{}}, remoteWS(t))
	if *madePublic || err != nil {
		t.Errorf("madePublic=%v err=%v; want private and complete", *madePublic, err)
	}
}

// Changing visibility fixes the paid-plan refusal and nothing else.
func TestOtherProtectionFailuresNeverAsk(t *testing.T) {
	madePublic := stubRemote(t, "NOT APPLIED: Resource not accessible by integration")
	asked := false
	err := remoteStep(&stepIO{Out: &bytes.Buffer{}, Confirm: func(string) bool { asked = true; return true }}, remoteWS(t))
	if asked || *madePublic {
		t.Errorf("asked=%v madePublic=%v for a failure going public would not fix", asked, *madePublic)
	}
	if _, ok := err.(*Degraded); !ok {
		t.Errorf("a non-plan protection failure must still stop the chain, got %v", err)
	}
}

func TestNeedsPaidPlan(t *testing.T) {
	for name, c := range map[string]struct {
		prot map[string]string
		want bool
	}{
		"both paid-plan":          {map[string]string{"main": paid, "develop": paid}, true},
		"one applied, one paid":   {map[string]string{"main": "applied", "develop": paid}, true},
		"permission error":        {map[string]string{"main": "NOT APPLIED: forbidden"}, false},
		"mixed causes":            {map[string]string{"main": paid, "develop": "NOT APPLIED: forbidden"}, false},
		"all applied, no failure": {map[string]string{"main": "applied", "develop": "applied"}, false},
		"nothing attempted":       {map[string]string{}, false},
	} {
		if got := provision.NeedsPaidPlan(&provision.Result{Protection: c.prot}); got != c.want {
			t.Errorf("%s: NeedsPaidPlan = %v, want %v", name, got, c.want)
		}
	}
}
