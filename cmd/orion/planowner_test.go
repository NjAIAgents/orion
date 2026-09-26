package main

// OR-490: the remote step asks which account or organisation to create the
// repository in, when nobody said.

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/orion-sdlc/orion/internal/provision"
)

func stubOwners(t *testing.T, user string, orgs []string, err error) {
	t.Helper()
	old := listOwnersFn
	listOwnersFn = func() (string, []string, error) { return user, orgs, err }
	t.Cleanup(func() { listOwnersFn = old })
}

// ownerAnswers returns an asker that replays the given answers and counts calls.
func ownerAnswers(a ...string) (asker, *int) {
	n := 0
	return func(string) string {
		defer func() { n++ }()
		if n < len(a) {
			return a[n]
		}
		return ""
	}, &n
}

func TestOwnerChoice(t *testing.T) {
	for name, c := range map[string]struct {
		answers []string
		want    string
	}{
		"a number picks that org":     {[]string{"2"}, "NjAIAgents"},
		"enter picks your account":    {[]string{""}, "navjyotnishant"},
		"a typed name is accepted":    {[]string{"acme-labs"}, "acme-labs"},
		"re-asked after a bad answer": {[]string{"9", "3"}, "acme-labs"},
		"three bad answers -> you":    {[]string{"x", "0", "99"}, "navjyotnishant"},
	} {
		t.Run(name, func(t *testing.T) {
			stubOwners(t, "navjyotnishant", []string{"NjAIAgents", "acme-labs"}, nil)
			w := chainWS(t)
			ask, _ := ownerAnswers(c.answers...)
			var out bytes.Buffer
			chooseRemoteOwner(&stepIO{Out: &out, Ask: ask}, w)
			if w.Task.RemoteOrg != c.want {
				t.Errorf("owner = %q, want %q\n%s", w.Task.RemoteOrg, c.want, out.String())
			}
		})
	}
}

func TestOwnerIsNotAskedWhenThereIsNothingToAsk(t *testing.T) {
	for name, c := range map[string]struct {
		preset string
		orgs   []string
		err    error
		noAsk  bool
	}{
		"--org or an earlier choice": {preset: "NjAIAgents", orgs: []string{"NjAIAgents"}},
		"non-interactive":            {orgs: []string{"NjAIAgents"}, noAsk: true},
		"no organisations":           {orgs: nil},
		"org list unreadable":        {orgs: []string{"NjAIAgents"}, err: errors.New("gh: 401")},
	} {
		t.Run(name, func(t *testing.T) {
			stubOwners(t, "navjyotnishant", c.orgs, c.err)
			w := chainWS(t)
			w.Task.RemoteOrg = c.preset
			ask, calls := ownerAnswers("2")
			sio := &stepIO{Out: &bytes.Buffer{}, Ask: ask}
			if c.noAsk {
				sio.Ask = nil
			}
			chooseRemoteOwner(sio, w)
			if *calls != 0 {
				t.Errorf("asked %d time(s) with nothing to choose", *calls)
			}
			if w.Task.RemoteOrg != c.preset {
				t.Errorf("owner changed to %q, want %q (today's behaviour)", w.Task.RemoteOrg, c.preset)
			}
		})
	}
}

// End to end through the remote step: the chosen org reaches repo creation,
// and a resume does not ask again.
func TestRemoteStepCreatesUnderTheChosenOwner(t *testing.T) {
	stubOwners(t, "navjyotnishant", []string{"NjAIAgents"}, nil)
	var gotOrg string
	oRemote, oSettings := remoteFn, ensureRepoSettingsFn
	remoteFn = func(opts provision.Options) (*provision.Result, error) {
		gotOrg = opts.Org
		return &provision.Result{RemoteURL: "https://github.com/" + opts.Org + "/x.git",
			Protection: map[string]string{"main": "applied", "develop": "applied"}}, nil
	}
	ensureRepoSettingsFn = func(string) {}
	t.Cleanup(func() { remoteFn, ensureRepoSettingsFn = oRemote, oSettings })

	w := chainWS(t)
	ask, calls := ownerAnswers("2")
	var out bytes.Buffer
	_ = remoteStep(&stepIO{Out: &out, Ask: ask, Confirm: func(string) bool { return false }}, w)
	if gotOrg != "NjAIAgents" {
		t.Errorf("repository created under %q, want NjAIAgents", gotOrg)
	}
	if !strings.Contains(out.String(), "Where should the repository be created?") {
		t.Errorf("the question was not shown:\n%s", out.String())
	}
	_ = remoteStep(&stepIO{Out: &bytes.Buffer{}, Ask: ask, Confirm: func(string) bool { return false }}, w)
	if *calls != 1 {
		t.Errorf("asked %d times across a resume, want once", *calls)
	}
}
