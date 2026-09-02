// Package update tells a person when the Orion they are running is old.
//
// Orion is left installed and run for months, so a version gap is the
// default state rather than an edge case -- and it used to be silent. A fix
// that had landed and been released still behaved like the old binary,
// because the old binary was what was installed, and nothing on screen said
// so (OR-92).
//
// Three properties matter more than the notice itself:
//
//   - It never delays a command. The cache is read synchronously and
//     refreshed by a detached child process, so the command in front of the
//     user prints what was already known and never waits on a network call.
//     A goroutine cannot do this job: `orion status` exits in milliseconds
//     and would take the unfinished request with it, so the cache would
//     never refresh at all.
//   - Every failure is silence. Offline, rate limited, DNS, GitHub down --
//     none of that is something wrong with the user's machine, so none of it
//     produces a warning, an error, or a non-zero exit.
//   - It never fires in hook mode. That is enforced by the caller (see
//     showsUpdateNotice in cmd/orion), because hooks run on every matching
//     tool call and a notice there would be printed hundreds of times a day
//     into someone's editor.
package update

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/orion-sdlc/orion/internal/creds"
	"github.com/orion-sdlc/orion/internal/procsafe"
	"github.com/orion-sdlc/orion/internal/ui"
	"github.com/orion-sdlc/orion/internal/workspace"
)

// latestAPI is GitHub's "latest release" endpoint, which by definition
// excludes drafts AND prereleases. The prerelease half is load-bearing:
// betas are published to the same repo as prereleases and the Homebrew tap
// deliberately never moves on one (scripts/release.sh), so telling a stable
// user about a beta would name a version `brew upgrade` cannot give them.
//
// A var so a test can point it at a local server rather than the internet.
var latestAPI = "https://api.github.com/repos/NjAIAgents/orion-releases/releases/latest"

const (
	// ReleasePage is what we show when the install method is unknown. A
	// guessed command that does not exist on that machine teaches the reader
	// the notice is unreliable, which costs more than saying less.
	ReleasePage = "https://github.com/NjAIAgents/orion-releases/releases/latest"

	// DisableKey silences the notice completely. Read from the environment
	// first and then from ~/.orion/config.env (creds.Get does both), so
	// somebody pinned to a version on purpose can say so once, in a file,
	// rather than exporting a variable in every shell.
	DisableKey = "ORION_NO_UPDATE_CHECK"

	// maxAge is how long a cached answer stands. A release is not urgent
	// enough to ask GitHub more often than this.
	maxAge = 24 * time.Hour
)

// cache is the whole persisted state: what the latest release was, and when
// we last managed to ask.
type cache struct {
	CheckedAt time.Time `json:"checked_at"`
	Latest    string    `json:"latest"`
}

// CachePath is where the answer lives, under ORION_HOME rather than a
// project: the installed binary is a property of the machine, not the repo
// you happen to be standing in.
func CachePath(home string) string {
	return filepath.Join(home, "state", "update.json")
}

// Notice prints the update line when a newer release is known, and starts a
// background refresh when the cached answer has aged out.
//
// It prints nothing and reports nothing on any failure.
func Notice(w io.Writer, current string) {
	home := workspace.Home()
	if Suppressed(w, home, current) != "" {
		return
	}
	emit(w, home, current, spawnRefresh)
}

// emit is Notice with the refresh injected, so a test can assert the
// once-a-day rule without spawning a process or reaching the network.
func emit(w io.Writer, home, current string, refresh func()) {
	c, _ := read(home)
	if time.Since(c.CheckedAt) >= maxAge {
		refresh()
	}
	if !Newer(c.Latest, current) {
		return
	}
	exe, err := os.Executable()
	if err == nil {
		if resolved, rerr := filepath.EvalSymlinks(exe); rerr == nil {
			exe = resolved
		}
	}
	fmt.Fprint(w, Line(w, c.Latest, current, UpgradeCommand(exe)))
}

// Suppressed reports why the notice must not print, or "" when it may.
//
// Returned as a reason rather than a bool so a test names the condition it
// is asserting, and so a future `orion doctor` line could explain why the
// notice is not appearing without duplicating the rules.
func Suppressed(w io.Writer, home, current string) string {
	if v := strings.TrimSpace(creds.Get(home, DisableKey)); v != "" && v != "0" && v != "false" {
		return DisableKey + " is set"
	}
	// A build log does not need upgrade advice, and nobody reads it for one.
	if strings.TrimSpace(os.Getenv("CI")) != "" {
		return "CI is set"
	}
	// Piped or redirected output is being consumed by a program, and an
	// unexpected line at the top of it is a parse error somewhere.
	if !ui.IsTerminal(w) {
		return "not a terminal"
	}
	// A development build has no meaningful version to compare, and telling
	// somebody building from source to `brew upgrade` is wrong advice.
	if _, ok := parse(current); !ok {
		return "not a released build"
	}
	return ""
}

// Line renders the notice: a status line and its continuation.
//
// The verb is `update` rather than WARNING on purpose. Nothing is broken and
// no action is required, and if this were indistinguishable from Orion's
// warnings then a reader scanning output could not tell "your branch is
// stale" from "a newer version exists" -- the more common of the two being
// the one that does not matter.
func Line(w io.Writer, latest, current, upgrade string) string {
	return fmt.Sprintf("%s\n%s\n",
		ui.Label(w, "update", fmt.Sprintf("orion %s is available (you have %s)", latest, current)),
		ui.Detail(w, upgrade))
}

// UpgradeCommand returns how to upgrade the binary living at exe.
//
// Detected from the binary's own path, because that is the only evidence on
// the machine of how it got there. Telling a Scoop user to run brew teaches
// them the notice is unreliable and they stop reading it, so anything we
// cannot identify gets the release page instead of a guess.
func UpgradeCommand(exe string) string {
	p := filepath.ToSlash(exe)
	switch {
	case underPrefix(p, os.Getenv("HOMEBREW_PREFIX")),
		strings.Contains(p, "/Cellar/"),
		strings.Contains(p, "/homebrew/"),
		strings.Contains(p, "/linuxbrew/"):
		return "brew upgrade navjyotnishant/tap/orion"
	case underPrefix(p, os.Getenv("SCOOP")),
		underPrefix(p, os.Getenv("SCOOP_GLOBAL")),
		strings.Contains(strings.ToLower(p), "/scoop/"):
		return "scoop update orion"
	}
	return ReleasePage
}

func underPrefix(path, prefix string) bool {
	prefix = strings.TrimSpace(filepath.ToSlash(prefix))
	if prefix == "" {
		return false
	}
	return strings.HasPrefix(path, strings.TrimSuffix(prefix, "/")+"/")
}

// Refresh asks GitHub what the latest release is and writes the cache. It is
// what the detached child runs; it prints nothing and returns nothing.
//
// CheckedAt is stamped even when the fetch fails, so an offline machine asks
// once a day rather than spawning a child for every command it runs.
func Refresh() {
	home := workspace.Home()
	c, _ := read(home)
	c.CheckedAt = time.Now().UTC()
	if tag, err := fetchLatest(); err == nil && tag != "" {
		c.Latest = tag
	}
	_ = write(home, c)
}

// spawnRefresh starts `orion update-check` and does not wait for it.
func spawnRefresh() {
	exe, err := os.Executable()
	if err != nil {
		return
	}
	cmd := exec.Command(exe, "update-check")
	// No stdin, no stdout, no stderr: the child must not be able to write
	// anything into the terminal the parent is drawing in.
	cmd.Stdin, cmd.Stdout, cmd.Stderr = nil, nil, nil
	if err := cmd.Start(); err != nil {
		return
	}
	// Reaped only if this process lives long enough (`orion watch` does);
	// a short command exits first and the child is reparented, which is the
	// point of not waiting on it.
	go func() { _ = cmd.Wait() }()
}

func fetchLatest() (string, error) {
	req, err := http.NewRequest(http.MethodGet, latestAPI, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("github: %s", resp.Status)
	}
	var body struct {
		Tag   string `json:"tag_name"`
		Draft bool   `json:"draft"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&body); err != nil {
		return "", err
	}
	if body.Draft {
		return "", nil
	}
	return strings.TrimSpace(body.Tag), nil
}

func read(home string) (cache, error) {
	var c cache
	b, err := os.ReadFile(CachePath(home))
	if err != nil {
		return c, err
	}
	err = json.Unmarshal(b, &c)
	return c, err
}

func write(home string, c cache) error {
	b, err := json.Marshal(c)
	if err != nil {
		return err
	}
	return procsafe.WriteFile(CachePath(home), b, 0o600)
}

// Newer reports whether latest is a strictly newer release than current.
//
// Semver-aware, not string-aware: "v0.10.0" sorts BELOW "v0.9.0" as text,
// and a notice that fires backwards is worse than no notice.
func Newer(latest, current string) bool {
	l, ok := parse(latest)
	if !ok {
		return false
	}
	c, ok := parse(current)
	if !ok {
		return false
	}
	return l.after(c)
}

type version struct {
	num [3]int
	pre string
}

// parse reads vX.Y.Z, with an optional -prerelease and +build. Anything else
// -- "dev", a branch name, an empty cache -- is not a version.
func parse(s string) (version, bool) {
	var v version
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "v")
	if s == "" {
		return v, false
	}
	if i := strings.IndexByte(s, '+'); i >= 0 {
		s = s[:i]
	}
	if i := strings.IndexByte(s, '-'); i >= 0 {
		v.pre, s = s[i+1:], s[:i]
	}
	parts := strings.Split(s, ".")
	if len(parts) != 3 {
		return v, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return v, false
		}
		v.num[i] = n
	}
	return v, true
}

func (v version) after(o version) bool {
	for i := range v.num {
		if v.num[i] != o.num[i] {
			return v.num[i] > o.num[i]
		}
	}
	// Same numbers: a release beats a prerelease of itself, which is the
	// case that matters here (running v0.9.0-beta.1 when v0.9.0 is out).
	// Two prereleases fall back to a text comparison -- the endpoint we read
	// never returns one, so full identifier precedence would be untestable
	// code guarding a case that cannot arrive.
	if v.pre == "" {
		return o.pre != ""
	}
	if o.pre == "" {
		return false
	}
	return v.pre > o.pre
}
