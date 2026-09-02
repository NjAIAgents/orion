package update

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The failure that made this ticket exist would be reintroduced by comparing
// versions as text: v0.10.0 sorts BELOW v0.9.0 that way, so a user on the
// older build would be told nothing and a user on the newer one would be
// told to downgrade.
func TestNewerIsSemverNotString(t *testing.T) {
	cases := []struct {
		latest, current string
		want            bool
	}{
		{"v0.10.0", "v0.9.0", true},  // the string comparison gets this backwards
		{"v0.9.0", "v0.10.0", false}, // and this one too
		{"v0.5.1", "v0.5.0", true},
		{"v0.5.0", "v0.5.0", false},
		{"v0.5.0", "v0.5.1", false},
		{"v1.0.0", "v0.99.99", true},
		{"0.5.1", "v0.5.0", true},         // the leading v is optional
		{"v0.9.0", "v0.9.0-beta.1", true}, // a release beats its own prerelease
		{"v0.9.0-beta.1", "v0.9.0", false},
		{"v0.5.1+deadbeef", "v0.5.0", true}, // build metadata is not precedence
		{"v0.5.1", "dev", false},            // a source build is never nagged
		{"", "v0.5.0", false},               // an empty cache says nothing
		{"v0.5", "v0.4", false},             // not a version: silence, not a guess
	}
	for _, c := range cases {
		if got := Newer(c.latest, c.current); got != c.want {
			t.Errorf("Newer(%q, %q) = %v, want %v", c.latest, c.current, got, c.want)
		}
	}
}

// The command has to match how the binary was actually installed. Telling a
// Scoop user to run brew teaches them the notice is unreliable.
func TestUpgradeCommandMatchesInstallMethod(t *testing.T) {
	t.Setenv("HOMEBREW_PREFIX", "")
	t.Setenv("SCOOP", "")
	t.Setenv("SCOOP_GLOBAL", "")

	cases := []struct{ exe, want string }{
		{"/opt/homebrew/Cellar/orion/0.5.0/bin/orion", "brew upgrade navjyotnishant/tap/orion"},
		{"/usr/local/Cellar/orion/0.5.0/bin/orion", "brew upgrade navjyotnishant/tap/orion"},
		{"/home/linuxbrew/.linuxbrew/bin/orion", "brew upgrade navjyotnishant/tap/orion"},
		{`C:/Users/nav/scoop/apps/orion/current/orion.exe`, "scoop update orion"},
		{"/usr/local/bin/orion", ReleasePage},
		{"/Users/nav/go/bin/orion", ReleasePage},
		{"", ReleasePage},
	}
	for _, c := range cases {
		if got := UpgradeCommand(c.exe); got != c.want {
			t.Errorf("UpgradeCommand(%q) = %q, want %q", c.exe, got, c.want)
		}
	}
}

func TestUpgradeCommandHonoursPrefixEnv(t *testing.T) {
	t.Setenv("HOMEBREW_PREFIX", "/custom/brew")
	t.Setenv("SCOOP", "")
	if got := UpgradeCommand("/custom/brew/bin/orion"); got != "brew upgrade navjyotnishant/tap/orion" {
		t.Errorf("a binary under HOMEBREW_PREFIX is a brew install, got %q", got)
	}

	t.Setenv("HOMEBREW_PREFIX", "")
	t.Setenv("SCOOP", `D:/apps/s`)
	if got := UpgradeCommand(`D:/apps/s/apps/orion/current/orion.exe`); got != "scoop update orion" {
		t.Errorf("a binary under SCOOP is a scoop install, got %q", got)
	}
	// A path merely NEAR the prefix is not under it.
	if got := UpgradeCommand(`D:/apps/somewhere/orion.exe`); got != ReleasePage {
		t.Errorf("a sibling of the scoop root is not a scoop install, got %q", got)
	}
}

func TestSuppressed(t *testing.T) {
	home := t.TempDir()
	tty := ttyWriter(t)

	t.Run("a pipe is not a terminal", func(t *testing.T) {
		clearEnv(t)
		if got := Suppressed(&bytes.Buffer{}, home, "v0.5.0"); got != "not a terminal" {
			t.Errorf("got %q, want the not-a-terminal reason", got)
		}
	})

	t.Run("CI", func(t *testing.T) {
		clearEnv(t)
		t.Setenv("CI", "true")
		if got := Suppressed(tty, home, "v0.5.0"); got != "CI is set" {
			t.Errorf("got %q, want the CI reason", got)
		}
	})

	t.Run("the environment variable", func(t *testing.T) {
		clearEnv(t)
		t.Setenv(DisableKey, "1")
		if got := Suppressed(tty, home, "v0.5.0"); got == "" {
			t.Error("ORION_NO_UPDATE_CHECK=1 must silence the notice completely")
		}
		// An explicit off is not a request to be silenced.
		t.Setenv(DisableKey, "0")
		if got := Suppressed(tty, home, "v0.5.0"); got != "" {
			t.Errorf("ORION_NO_UPDATE_CHECK=0 must not silence it, got %q", got)
		}
	})

	t.Run("the config key", func(t *testing.T) {
		clearEnv(t)
		cfgHome := t.TempDir()
		if err := os.WriteFile(filepath.Join(cfgHome, "config.env"),
			[]byte(DisableKey+"=1\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if got := Suppressed(tty, cfgHome, "v0.5.0"); got == "" {
			t.Error("the key in config.env must silence the notice; saying it once is the point")
		}
	})

	t.Run("a source build", func(t *testing.T) {
		clearEnv(t)
		if got := Suppressed(tty, home, "dev"); got != "not a released build" {
			t.Errorf("got %q, want the source-build reason", got)
		}
	})

	t.Run("nothing in the way", func(t *testing.T) {
		clearEnv(t)
		if got := Suppressed(tty, home, "v0.5.0"); got != "" {
			t.Errorf("a released build on a terminal must be eligible, got %q", got)
		}
	})

	// "false" is spelled out in the ticket alongside "0" as a value that must
	// NOT suppress -- only an explicit true-ish value opts out.
	t.Run("the environment variable set to false", func(t *testing.T) {
		clearEnv(t)
		t.Setenv(DisableKey, "false")
		if got := Suppressed(tty, home, "v0.5.0"); got != "" {
			t.Errorf("ORION_NO_UPDATE_CHECK=false must not silence it, got %q", got)
		}
	})

	// The config file honours the same "0"/"false"/empty carve-out as the
	// environment variable -- a file is just the other place this value can
	// live, not a different rule.
	t.Run("the config key set to false or 0 does not suppress", func(t *testing.T) {
		for _, v := range []string{"0", "false", ""} {
			cfgHome := t.TempDir()
			if err := os.WriteFile(filepath.Join(cfgHome, "config.env"),
				[]byte(DisableKey+"="+v+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			clearEnv(t)
			if got := Suppressed(tty, cfgHome, "v0.5.0"); got != "" {
				t.Errorf("%s=%q in config.env must not silence it, got %q", DisableKey, v, got)
			}
		}
	})

	// Environment first, file second (creds.Get's own contract) -- an
	// explicit per-invocation override must win even when a file says the
	// opposite, or a deliberate "just this once" cannot be expressed.
	t.Run("environment overrides the config file", func(t *testing.T) {
		cfgHome := t.TempDir()
		if err := os.WriteFile(filepath.Join(cfgHome, "config.env"),
			[]byte(DisableKey+"=1\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		clearEnv(t)
		t.Setenv(DisableKey, "0")
		if got := Suppressed(tty, cfgHome, "v0.5.0"); got != "" {
			t.Errorf("env %s=0 must win over config.env=1, got %q", DisableKey, got)
		}
	})

}

// A cache file that exists but is not valid JSON must be treated the same as
// a missing one -- silence, not a crash and not a notice built on garbage.
func TestEmitIgnoresACorruptCacheFile(t *testing.T) {
	corrupt := t.TempDir()
	if err := os.MkdirAll(filepath.Join(corrupt, "state"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(CachePath(corrupt), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	refreshed := false
	emit(&buf, corrupt, "v0.5.0", func() { refreshed = true })
	if buf.Len() != 0 {
		t.Errorf("printed %q from a corrupt cache", buf.String())
	}
	if !refreshed {
		t.Error("a corrupt cache reads as no known CheckedAt, so it must still trigger a refresh")
	}
}

func TestEmit(t *testing.T) {
	t.Run("prints the gap and the command", func(t *testing.T) {
		home := seed(t, cache{CheckedAt: time.Now(), Latest: "v0.5.1"})
		var buf bytes.Buffer
		emit(&buf, home, "v0.5.0", func() { t.Error("a fresh cache must not be refreshed") })
		out := buf.String()
		for _, want := range []string{"update", "v0.5.1 is available", "you have v0.5.0"} {
			if !strings.Contains(out, want) {
				t.Errorf("the notice does not say %q: %q", want, out)
			}
		}
		if lines := strings.Split(strings.TrimRight(out, "\n"), "\n"); len(lines) != 2 {
			t.Errorf("the notice is one status line plus its command, got %d lines: %q", len(lines), out)
		}
	})

	t.Run("says nothing when up to date", func(t *testing.T) {
		home := seed(t, cache{CheckedAt: time.Now(), Latest: "v0.5.0"})
		var buf bytes.Buffer
		emit(&buf, home, "v0.5.0", func() {})
		if buf.Len() != 0 {
			t.Errorf("printed %q on the current version", buf.String())
		}
	})

	t.Run("says nothing with no cache at all", func(t *testing.T) {
		var buf bytes.Buffer
		refreshed := false
		emit(&buf, t.TempDir(), "v0.5.0", func() { refreshed = true })
		if buf.Len() != 0 {
			t.Errorf("printed %q with nothing known; offline must look exactly like today", buf.String())
		}
		if !refreshed {
			t.Error("an absent cache must start a refresh")
		}
	})

	t.Run("re-checks at most once a day", func(t *testing.T) {
		fresh := seed(t, cache{CheckedAt: time.Now().Add(-23 * time.Hour), Latest: "v0.5.0"})
		emit(&bytes.Buffer{}, fresh, "v0.5.0", func() {
			t.Error("checked again inside 24 hours")
		})

		stale := seed(t, cache{CheckedAt: time.Now().Add(-25 * time.Hour), Latest: "v0.5.0"})
		refreshed := false
		emit(&bytes.Buffer{}, stale, "v0.5.0", func() { refreshed = true })
		if !refreshed {
			t.Error("a cache older than 24 hours must be refreshed")
		}
	})
}

// Notice must write nothing to a pipe, whatever the cache says: that is the
// acceptance criterion a redirected command depends on.
func TestNoticeIsSilentOffATerminal(t *testing.T) {
	clearEnv(t)
	t.Setenv("ORION_HOME", seed(t, cache{CheckedAt: time.Now(), Latest: "v9.9.9"}))
	var buf bytes.Buffer
	Notice(&buf, "v0.5.0")
	if buf.Len() != 0 {
		t.Errorf("printed %q to a pipe", buf.String())
	}
}

func TestRefresh(t *testing.T) {
	serve := func(t *testing.T, status int, body string) {
		t.Helper()
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(status)
			_, _ = io.WriteString(w, body)
		}))
		t.Cleanup(srv.Close)
		old := latestAPI
		latestAPI = srv.URL
		t.Cleanup(func() { latestAPI = old })
	}

	t.Run("stores the tag", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("ORION_HOME", home)
		serve(t, 200, `{"tag_name":"v0.5.1","draft":false,"prerelease":false}`)
		Refresh()
		c, err := read(home)
		if err != nil || c.Latest != "v0.5.1" {
			t.Fatalf("read back %+v (%v), want v0.5.1", c, err)
		}
		if time.Since(c.CheckedAt) > time.Minute {
			t.Errorf("CheckedAt is %v, want now", c.CheckedAt)
		}
	})

	t.Run("a failure keeps what was known and still stamps the check", func(t *testing.T) {
		home := seed(t, cache{CheckedAt: time.Now().Add(-48 * time.Hour), Latest: "v0.5.1"})
		t.Setenv("ORION_HOME", home)
		serve(t, 503, "down")
		Refresh()
		c, _ := read(home)
		if c.Latest != "v0.5.1" {
			t.Errorf("a failed fetch discarded the known version: %+v", c)
		}
		// Stamped anyway, or an offline machine spawns a child for every
		// command it runs for the rest of time.
		if time.Since(c.CheckedAt) > time.Minute {
			t.Errorf("a failed check did not stamp CheckedAt: %v", c.CheckedAt)
		}
	})

	t.Run("a draft is not a release", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("ORION_HOME", home)
		serve(t, 200, `{"tag_name":"v9.9.9","draft":true}`)
		Refresh()
		if c, _ := read(home); c.Latest != "" {
			t.Errorf("advertised a draft: %+v", c)
		}
	})
}

func TestCacheRoundTrip(t *testing.T) {
	home := t.TempDir()
	want := cache{CheckedAt: time.Now().UTC().Truncate(time.Second), Latest: "v1.2.3"}
	if err := write(home, want); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(CachePath(home)); err != nil {
		t.Fatalf("the cache is not where the rest of Orion's home state lives: %v", err)
	}
	got, err := read(home)
	if err != nil {
		t.Fatal(err)
	}
	if got.Latest != want.Latest || !got.CheckedAt.Equal(want.CheckedAt) {
		t.Errorf("read back %+v, wrote %+v", got, want)
	}
}

func seed(t *testing.T, c cache) string {
	t.Helper()
	home := t.TempDir()
	if err := write(home, c); err != nil {
		t.Fatal(err)
	}
	return home
}

// clearEnv removes every variable that would otherwise decide the outcome
// for us -- CI is set on the machine running these tests.
func clearEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{"CI", DisableKey, "HOMEBREW_PREFIX", "SCOOP", "SCOOP_GLOBAL"} {
		t.Setenv(k, "")
	}
	os.Unsetenv("CI")
	os.Unsetenv(DisableKey)
}

// ttyWriter returns a writer that passes the character-device test, so the
// other suppression rules can be exercised without a real terminal.
func ttyWriter(t *testing.T) *os.File {
	t.Helper()
	f, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		t.Skipf("no %s to stand in for a terminal: %v", os.DevNull, err)
	}
	t.Cleanup(func() { _ = f.Close() })
	return f
}
