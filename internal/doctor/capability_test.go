package doctor

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/orion-sdlc/orion/internal/creds"
)

// The failure this guards against is not a wrong verdict but a wrong REMEDY.
// A stale export shadows a correct config.env; if the message only names the
// variables and says "run orion config", the user edits the file, the file
// still loses to the environment, and nothing changes. The loop has no exit.
func TestJiraCredSourceNamesTheEnvironmentAndHowToClearIt(t *testing.T) {
	home := t.TempDir()
	t.Setenv("ORION_HOME", home)
	if err := creds.Save(home, map[string]string{
		creds.JiraURL:   "https://right.atlassian.net",
		creds.JiraEmail: "right@example.com",
		creds.JiraToken: "stored-token",
	}); err != nil {
		t.Fatal(err)
	}
	t.Setenv(creds.JiraEmail, "stale@example.com")

	src, hint := jiraCredSource()

	if !strings.Contains(src, "environment") {
		t.Errorf("source = %q, must say the environment is in play", src)
	}
	if !strings.Contains(hint, "unset "+creds.JiraEmail) {
		t.Errorf("hint must give the exact unset command, got:\n%s", hint)
	}
	if !strings.Contains(hint, "being ignored") {
		t.Errorf("hint must say the stored value is shadowed, got:\n%s", hint)
	}
	// Sending the user to edit the file is the wrong remedy in this state.
	if strings.Contains(hint, "--only") {
		t.Errorf("must not point at `orion config --only` while the env wins:\n%s", hint)
	}
}

// With nothing exported the file IS the thing to fix, so the remedy flips.
func TestJiraCredSourceFallsBackToTheFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("ORION_HOME", home)
	for _, k := range []string{creds.JiraURL, creds.JiraEmail, creds.JiraToken} {
		t.Setenv(k, "")
	}
	if err := creds.Save(home, map[string]string{creds.JiraToken: "t"}); err != nil {
		t.Fatal(err)
	}

	src, hint := jiraCredSource()

	if !strings.Contains(src, "config file") {
		t.Errorf("source = %q, want the config file", src)
	}
	if strings.Contains(hint, "unset") {
		t.Errorf("nothing is exported; telling the user to unset is noise:\n%s", hint)
	}
	if !strings.Contains(hint, "orion config") {
		t.Errorf("hint should point at orion config, got:\n%s", hint)
	}
}

func homeAt(t *testing.T) string {
	t.Helper()
	h := t.TempDir()
	t.Setenv("ORION_HOME", h)
	return h
}

// The cache exists so an expensive capability check is not repeated on every
// invocation. It must never outlive a config change: trusting a stale
// verdict about a DIFFERENT Jira instance is worse than rechecking.
func TestCacheIsInvalidatedByAConfigChange(t *testing.T) {
	homeAt(t)
	results := map[string]string{"jira": "OK   authenticated"}
	SaveCache("hash-a", results)

	if got, ok := Fresh("hash-a"); !ok || got["jira"] != results["jira"] {
		t.Fatalf("a fresh cache did not return: %v %v", got, ok)
	}
	if _, ok := Fresh("hash-b"); ok {
		t.Error("a cache from a different config was trusted")
	}
}

func TestCacheExpires(t *testing.T) {
	home := homeAt(t)
	SaveCache("h", map[string]string{"x": "OK"})

	// Rewrite the stamp to just outside the TTL.
	p := filepath.Join(home, "doctor.json")
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	var c cacheFile
	if err := json.Unmarshal(b, &c); err != nil {
		t.Fatal(err)
	}
	c.CheckedAt = time.Now().Add(-cacheTTL - time.Minute)
	nb, _ := json.Marshal(c)
	if err := os.WriteFile(p, nb, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, ok := Fresh("h"); ok {
		t.Error("an expired verdict was reused; a token that passed in March is not proof in June")
	}
}

func TestCacheMissIsNotAnError(t *testing.T) {
	homeAt(t)
	if _, ok := Fresh("anything"); ok {
		t.Error("a missing cache reported as fresh")
	}
	if err := os.WriteFile(filepath.Join(os.Getenv("ORION_HOME"), "doctor.json"),
		[]byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, ok := Fresh("anything"); ok {
		t.Error("a corrupt cache reported as fresh")
	}
}

// The cache records who you authenticated as, and sits beside config.env.
func TestCacheIsOwnerOnly(t *testing.T) {
	home := homeAt(t)
	SaveCache("h", map[string]string{"jira": "OK authenticated as Someone"})
	fi, err := os.Stat(filepath.Join(home, "doctor.json"))
	if err != nil {
		t.Fatal(err)
	}
	if mode := fi.Mode().Perm(); mode&0o077 != 0 {
		t.Errorf("mode = %o", mode)
	}
}

// The gap that let a broken install look healthy: orion.json parsed, the
// limits read as in force, and the hooks meant to enforce them pointed at a
// deleted directory.
func TestCheckHooksCatchesCommandsThatDoNotResolve(t *testing.T) {
	dir := t.TempDir()
	claude := filepath.Join(dir, ".claude")
	if err := os.MkdirAll(claude, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(claude, "settings.json"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// No settings at all.
	if c := checkHooks(t.TempDir()); c.grade != warn {
		t.Errorf("missing settings graded %v, want warn", c.grade)
	}

	// Present but unparseable: Claude Code cannot read it either.
	write(`{{{ not json`)
	if c := checkHooks(dir); c.grade != warn || !strings.Contains(c.detail, "not valid JSON") {
		t.Errorf("bad JSON = %+v", c)
	}

	// Wired but pointing at something gone. This must be FAIL: every gate is
	// silently doing nothing while orion.json still says they are enabled.
	gone := filepath.Join(t.TempDir(), "removed", "orion")
	write(`{"hooks":{"PreToolUse":[{"matcher":"Bash","hooks":[
	  {"type":"command","command":"` + gone + ` hook gate"}]}]}}`)
	c := checkHooks(dir)
	if c.grade != fail {
		t.Errorf("an unresolvable hook graded %v, want fail", c.grade)
	}
	if !strings.Contains(c.fix, "orion init --force") {
		t.Errorf("the repair command must be named: %q", c.fix)
	}

	// Someone else's hook must not be judged, and must not count as ours.
	write(`{"hooks":{"PreToolUse":[{"hooks":[
	  {"type":"command","command":"/definitely/missing/prettier --write"}]}]}}`)
	if c := checkHooks(dir); c.grade != warn || !strings.Contains(c.detail, "no Orion hooks") {
		t.Errorf("a foreign hook was judged as ours: %+v", c)
	}

	// A resolvable command passes. The binary must be NAMED orion: the check
	// recognises Orion's own hooks by that name so it never grades a team's
	// prettier or husky hook, and a fake called anything else is correctly
	// ignored.
	binDir := t.TempDir()
	realOrion := filepath.Join(binDir, "orion")
	if err := os.WriteFile(realOrion, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	write(`{"hooks":{"PreToolUse":[{"hooks":[
	  {"type":"command","command":"` + realOrion + ` hook gate"}]}]}}`)
	if c := checkHooks(dir); c.grade != ok {
		t.Errorf("a resolvable hook graded %v: %+v", c.grade, c)
	}
}

// Attribution is a check because it went wrong silently: the sandbox clone
// carried no dun hooks for months and nothing in `orion doctor` said so. A
// missing dun must degrade, not pass (OR-193).
func TestCheckAttributionDegradesWithoutDun(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	c := checkAttribution(t.TempDir(), false)
	if c.grade == ok {
		t.Fatalf("reported OK with no dun on PATH: %+v", c)
	}
	if !strings.Contains(c.fix, "attribution trailer") {
		t.Errorf("fix does not say what is lost: %q", c.fix)
	}
}

// A project that has turned attribution off is not degraded, it is
// configured. Warning there is how a check gets ignored everywhere else.
func TestCheckAttributionRespectsTheDisabledFlag(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	root := t.TempDir()
	cfg := []byte(`{"version":1,"attribution":{"enabled":false}}`)
	if err := os.WriteFile(filepath.Join(root, "orion.json"), cfg, 0o600); err != nil {
		t.Fatal(err)
	}
	if c := checkAttribution(root, false); c.grade != ok {
		t.Fatalf("graded %v with attribution disabled: %+v", c.grade, c)
	}
}

func TestUniqPreservesOrder(t *testing.T) {
	got := uniq([]string{"b", "a", "b", "c", "a"})
	if strings.Join(got, ",") != "b,a,c" {
		t.Errorf("uniq = %v", got)
	}
	if len(uniq(nil)) != 0 {
		t.Error("uniq(nil) should be empty")
	}
}

// A tool that is absent must be reported as absent, not crash the check.
func TestChecksDegradeWhenToolsAreMissing(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	for name, fn := range map[string]func() check{
		"claude":        checkClaude,
		"git":           checkGit,
		"gh":            checkGH,
		"gh repo scope": checkGHScopes,
	} {
		c := fn()
		if c.grade == ok {
			t.Errorf("%s reported OK with an empty PATH: %+v", name, c)
		}
		if strings.TrimSpace(c.detail) == "" {
			t.Errorf("%s gave no detail", name)
		}
	}
}

// checkDisk REPAIRS an over-permissive home rather than only reporting it,
// because a 0755 tree left by an earlier version holds credentials, a spend
// ledger and full agent transcripts. Repair beats a warning nobody acts on.
//
// A consequence worth naming: the "readable by other users" branch inside
// checkDisk is therefore unreachable in practice, since EnsureHome has
// already fixed the mode by the time it is evaluated. That is the right
// trade -- but it means the warning is dead code, not a live safety net, and
// the repair is what must be tested.
func TestCheckDiskRepairsAnOpenHome(t *testing.T) {
	home := homeAt(t)
	if c := checkDisk(); c.grade != ok {
		t.Fatalf("a fresh home graded %v: %+v", c.grade, c)
	}
	if !creds.PermsSupported() {
		t.Skip("permission bits are emulated here")
	}
	if err := os.Chmod(home, 0o755); err != nil {
		t.Fatal(err)
	}

	c := checkDisk()
	fi, err := os.Stat(home)
	if err != nil {
		t.Fatal(err)
	}
	if mode := fi.Mode().Perm(); mode&0o077 != 0 {
		t.Errorf("home left at %o; an open tree from an earlier version was not repaired", mode)
	}
	if c.grade != ok {
		t.Errorf("graded %v after a successful repair: %+v", c.grade, c)
	}
	if !strings.Contains(c.detail, "owner-only") {
		t.Errorf("detail should state the posture it verified: %q", c.detail)
	}
}

// A home that cannot be written to at all is a hard failure: every log,
// ledger and workspace lives there.
func TestCheckDiskFailsWhenHomeIsUnwritable(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root can write anywhere")
	}
	home := homeAt(t)
	if err := os.Chmod(home, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(home, 0o700) })

	if c := checkDisk(); c.grade != fail {
		t.Errorf("an unwritable home graded %v, want fail: %+v", c.grade, c)
	}
}

func TestCheckProjectGradesAMalformedConfigAsFail(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "orion.json"), []byte(`{"limits":`), 0o644); err != nil {
		t.Fatal(err)
	}
	c := checkProject(dir)
	// FAIL not WARN: a malformed config silently falls back to defaults, so
	// the limits in force are not the ones that were written.
	if c.grade != fail {
		t.Errorf("malformed config graded %v, want fail", c.grade)
	}

	if err := os.WriteFile(filepath.Join(dir, "orion.json"),
		[]byte(`{"limits":{"max_tool_calls":42}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if c := checkProject(dir); c.grade != ok || !strings.Contains(c.detail, "42") {
		t.Errorf("a valid config graded %v: %+v", c.grade, c)
	}
}

func TestTrackerRequiredFollowsTheEnabledFlag(t *testing.T) {
	dir := t.TempDir()
	w := func(body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, "orion.json"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	w(`{"tracker":{"enabled":false,"create_project_per_idea":true}}`)
	if trackerRequired(dir) {
		t.Error("a disabled tracker was treated as required; a bad token would block every run")
	}
	w(`{"tracker":{"enabled":true}}`)
	if !trackerRequired(dir) {
		t.Error("an enabled tracker was treated as optional")
	}
}

// The hash is what invalidates the cache, so it must move when the inputs do.
func TestConfigHashChangesWithTheConfig(t *testing.T) {
	dir := t.TempDir()
	write := func(body string) string {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, "orion.json"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return configHash(dir)
	}
	a := write(`{"tracker":{"enabled":true,"project_key":"AAA"}}`)
	b := write(`{"tracker":{"enabled":true,"project_key":"BBB"}}`)
	if a == b {
		t.Error("the hash did not move when the tracker changed; a stale verdict about a different instance would be trusted")
	}
	if c := write(`{"tracker":{"enabled":true,"project_key":"BBB"}}`); c != b {
		t.Error("the hash is not stable for identical input")
	}
}

// Run must render every check and return an exit code a script can branch on.
func TestRunRendersAndGradesOverall(t *testing.T) {
	homeAt(t)
	var out strings.Builder
	code := Run(&out, t.TempDir(), false)
	s := out.String()

	if !strings.HasPrefix(s, "orion doctor") {
		t.Errorf("output should identify itself:\n%s", s)
	}
	for _, want := range []string{"claude CLI", "git", "orion home", "disk", "hooks"} {
		if !strings.Contains(s, want) {
			t.Errorf("check %q missing from the report", want)
		}
	}
	if code != 0 && code != 1 {
		t.Errorf("exit code = %d; a script needs a stable contract", code)
	}
}
