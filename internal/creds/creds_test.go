package creds

import (
	"os"
	"runtime"
	"strings"
	"testing"
)

func TestMissingFileIsNotAnError(t *testing.T) {
	m, err := Load(t.TempDir())
	if err != nil {
		t.Fatalf("a missing config is a normal state, not a fault: %v", err)
	}
	if len(m) != 0 {
		t.Errorf("expected nothing, got %v", m)
	}
}

func TestSaveAndLoadRoundTrip(t *testing.T) {
	home := t.TempDir()
	in := map[string]string{
		JiraURL:   "https://x.atlassian.net",
		JiraToken: "ATATT-secret",
	}
	if err := Save(home, in); err != nil {
		t.Fatal(err)
	}
	out, err := Load(home)
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range in {
		if out[k] != v {
			t.Errorf("%s = %q, want %q", k, out[k], v)
		}
	}
}

// The file holds secrets. Created 0600 at open time rather than chmod'ed
// afterwards, so there is no window where it is readable by anyone else.
func TestFileIsNotReadableByOthers(t *testing.T) {
	// Windows has no POSIX mode bits: every file reports 0666 there, so this
	// asserts the operating system rather than the code (OR-334).
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission bits do not exist on Windows")
	}
	if !PermsSupported() {
		t.Skip("Unix permission bits are emulated on this platform; NTFS ACLs govern access")
	}
	home := t.TempDir()
	if err := Save(home, map[string]string{JiraToken: "s"}); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(Path(home))
	if err != nil {
		t.Fatal(err)
	}
	if mode := fi.Mode().Perm(); mode&0o077 != 0 {
		t.Errorf("mode = %o, want no group or world bits", mode)
	}
	okPerm, _, err := CheckPerms(home)
	if err != nil || !okPerm {
		t.Errorf("CheckPerms said %v (err %v)", okPerm, err)
	}
}

func TestSavePreservesUntouchedKeys(t *testing.T) {
	home := t.TempDir()
	if err := Save(home, map[string]string{JiraURL: "u", SlackToken: "xoxb-1"}); err != nil {
		t.Fatal(err)
	}
	// Change one; the other must survive.
	if err := Save(home, map[string]string{JiraURL: "u2"}); err != nil {
		t.Fatal(err)
	}
	m, _ := Load(home)
	if m[SlackToken] != "xoxb-1" {
		t.Errorf("SlackToken = %q: an untouched key was lost", m[SlackToken])
	}
	if m[JiraURL] != "u2" {
		t.Errorf("JiraURL = %q, want u2", m[JiraURL])
	}
}

func TestEmptyValueDeletes(t *testing.T) {
	home := t.TempDir()
	_ = Save(home, map[string]string{SlackToken: "xoxb-1"})
	_ = Save(home, map[string]string{SlackToken: ""})
	m, _ := Load(home)
	if _, present := m[SlackToken]; present {
		t.Error("an empty value should clear the key, so a credential can be removed")
	}
}

// An exported variable is a deliberate override for one invocation and must
// beat anything stored.
func TestEnvironmentWinsOverFile(t *testing.T) {
	home := t.TempDir()
	_ = Save(home, map[string]string{JiraURL: "from-file"})

	if got := Get(home, JiraURL); got != "from-file" {
		t.Fatalf("Get = %q, want the file value when the env is unset", got)
	}
	t.Setenv(JiraURL, "from-env")
	if got := Get(home, JiraURL); got != "from-env" {
		t.Errorf("Get = %q, want the environment to win", got)
	}
	if s := Source(home, JiraURL); s != "environment" {
		t.Errorf("Source = %q, want environment", s)
	}
}

// People will paste the line they have been copying around all day.
func TestParsesPastedExportLines(t *testing.T) {
	home := t.TempDir()
	body := "# comment\n" +
		"export ORION_JIRA_URL='https://x.atlassian.net'\n" +
		"ORION_JIRA_EMAIL=\"a@b.com\"\n" +
		"\n" +
		"ORION_SLACK_TOKEN=xoxb-plain\n"
	if err := os.WriteFile(Path(home), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	m, err := Load(home)
	if err != nil {
		t.Fatal(err)
	}
	if m[JiraURL] != "https://x.atlassian.net" {
		t.Errorf("export-prefixed line: %q", m[JiraURL])
	}
	if m[JiraEmail] != "a@b.com" {
		t.Errorf("double-quoted value: %q", m[JiraEmail])
	}
	if m[SlackToken] != "xoxb-plain" {
		t.Errorf("bare value: %q", m[SlackToken])
	}
}

// A value containing a quote must survive a write/read cycle, or saving a
// token with one silently corrupts it.
func TestQuotesInValuesSurvive(t *testing.T) {
	home := t.TempDir()
	weird := "abc'def"
	if err := Save(home, map[string]string{JiraToken: weird}); err != nil {
		t.Fatal(err)
	}
	m, _ := Load(home)
	if m[JiraToken] != weird {
		t.Errorf("round trip lost data: %q != %q", m[JiraToken], weird)
	}
}

func TestMaskNeverRevealsEnough(t *testing.T) {
	long := "ATATT3xFfGF0YVWITxHVl7EIJu88041q0Fb2P5vSHzSvVBa8fPha"
	m := Mask(long)
	if strings.Contains(m, long) || len(m) >= len(long) {
		t.Errorf("Mask(%q) = %q leaks too much", long, m)
	}
	if !strings.HasPrefix(m, "ATATT3") {
		t.Errorf("Mask should keep a recognisable prefix, got %q", m)
	}
	// A short secret gives up nothing at all: six characters of a twelve
	// character token is most of it.
	if got := Mask("shortone"); got != "********" {
		t.Errorf("Mask(short) = %q, want full masking", got)
	}
	if Mask("") != "" {
		t.Error("Mask(empty) should stay empty rather than becoming stars")
	}
}

func TestSecretClassification(t *testing.T) {
	for _, k := range []string{JiraToken, SlackToken, Webhook} {
		if !Secret(k) {
			t.Errorf("%s must be treated as a secret", k)
		}
	}
	for _, k := range []string{JiraURL, JiraEmail} {
		if Secret(k) {
			t.Errorf("%s is not a secret and should display in full", k)
		}
	}
}

func TestCheckPermsSpotsAnOpenFile(t *testing.T) {
	if !PermsSupported() {
		t.Skip("Chmod only toggles the read-only attribute on this platform")
	}
	home := t.TempDir()
	_ = Save(home, map[string]string{JiraToken: "s"})
	if err := os.Chmod(Path(home), 0o644); err != nil {
		t.Fatal(err)
	}
	okPerm, mode, err := CheckPerms(home)
	if err != nil {
		t.Fatal(err)
	}
	if okPerm {
		t.Errorf("mode %o should be reported as too open", mode)
	}
	if err := Tighten(home); err != nil {
		t.Fatal(err)
	}
	if okPerm, _, _ := CheckPerms(home); !okPerm {
		t.Error("Tighten did not fix the permissions")
	}
}

func TestPromptKeepsCurrentOnEmptyInput(t *testing.T) {
	var out strings.Builder
	got, err := Prompt(strings.NewReader("\n"), &out, "Label", "existing", false)
	if err != nil {
		t.Fatal(err)
	}
	if got != "existing" {
		t.Errorf("empty input should keep the current value, got %q", got)
	}
}

func TestPromptAcceptsAPastedExportLine(t *testing.T) {
	var out strings.Builder
	got, err := Prompt(strings.NewReader("export ORION_JIRA_URL='https://x.atlassian.net'\n"),
		&out, "Label", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if got != "https://x.atlassian.net" {
		t.Errorf("got %q: a pasted export line should yield just the value", got)
	}
}

// The masked current value must never appear in what gets stored.
func TestPromptDoesNotEchoSecretIntoTheValue(t *testing.T) {
	var out strings.Builder
	got, err := Prompt(strings.NewReader("\n"), &out, "Token", "supersecretvalue", true)
	if err != nil {
		t.Fatal(err)
	}
	if got != "supersecretvalue" {
		t.Errorf("keeping the current secret should return it intact, got %q", got)
	}
	if strings.Contains(out.String(), "supersecretvalue") {
		t.Error("the prompt printed the secret in full; it must show a mask")
	}
}

// A permission warning that is always wrong is worse than none: it teaches
// people to ignore the real ones.
func TestCheckPermsDoesNotCryWolfWhereBitsAreMeaningless(t *testing.T) {
	home := t.TempDir()
	if err := Save(home, map[string]string{JiraToken: "s"}); err != nil {
		t.Fatal(err)
	}
	okPerm, _, err := CheckPerms(home)
	if err != nil {
		t.Fatal(err)
	}
	if !PermsSupported() && !okPerm {
		t.Error("must not report TOO OPEN on a platform where the bits are emulated")
	}
}
