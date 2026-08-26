package notify

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// isolate clears every delivery channel so a test opts in to the one it is
// exercising, rather than accidentally firing a real webhook.
func isolate(t *testing.T) {
	t.Helper()
	prev := SetSlackSender(func(string, string) error { return nil })
	t.Cleanup(func() { SetSlackSender(prev) })
	SetWebhookResolver(func() string { return "" })
	t.Setenv("ORION_NOTIFY_WEBHOOK", "")
	t.Setenv("ORION_NOTIFY_COMMAND", "")
	t.Setenv("ORION_SLACK_TOKEN", "")
	t.Cleanup(func() {
		webhookURL = func() string { return strings.TrimSpace(os.Getenv("ORION_NOTIFY_WEBHOOK")) }
	})
}

// The promise this package makes: a notification that fails must never take
// down the run it was reporting on, and one broken channel must not stop the
// others. A failure notification is needed most when things are already going
// wrong, which is exactly when a delivery path is most likely to be broken.
func TestABrokenChannelDoesNotStopTheOthers(t *testing.T) {
	isolate(t)
	if runtime.GOOS == "windows" {
		t.Skip("uses a POSIX shell for the custom command")
	}
	marker := filepath.Join(t.TempDir(), "custom-ran")

	// A webhook pointing at nothing, alongside a custom command that works.
	SetWebhookResolver(func() string { return "http://127.0.0.1:1/nope" })
	t.Setenv("ORION_NOTIFY_COMMAND", "touch "+marker)

	errs := Send(Event{Level: Blocked, Title: "a title", Body: "a body"})

	if len(errs) == 0 {
		t.Error("the dead webhook should have been reported")
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatal("a failing channel prevented a working one from delivering")
	}
}

func TestSendReturnsErrorsRatherThanPanicking(t *testing.T) {
	isolate(t)
	SetWebhookResolver(func() string { return "://not-a-url" })
	errs := Send(Event{Title: "t", Body: "b"})
	if len(errs) == 0 {
		t.Error("a malformed webhook URL was swallowed entirely")
	}
	for _, e := range errs {
		if e == nil {
			t.Error("a nil error in the returned slice")
		}
	}
}

func TestSendStampsTheTime(t *testing.T) {
	isolate(t)
	var got Event
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &got)
	}))
	defer srv.Close()
	SetWebhookResolver(func() string { return srv.URL })

	Send(Event{Title: "t", Body: "b"})
	if got.At.IsZero() {
		t.Error("an event with no timestamp was delivered without one")
	}
}

// The webhook must speak Slack's shape with no adapter, because a raw Slack
// incoming webhook URL is the most likely thing anyone pastes in.
func TestWebhookPayloadIsSlackCompatible(t *testing.T) {
	isolate(t)
	var payload map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if ct := r.Header.Get("Content-Type"); !strings.Contains(ct, "application/json") {
			t.Errorf("Content-Type = %q", ct)
		}
		b, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(b, &payload); err != nil {
			t.Fatalf("body is not JSON: %s", b)
		}
	}))
	defer srv.Close()

	e := Event{Level: Blocked, Title: "Orion stopped", Body: "quota exhausted",
		Workspace: "fcia-1", At: time.Now()}
	if err := webhook(srv.URL, e); err != nil {
		t.Fatal(err)
	}
	text, _ := payload["text"].(string)
	if !strings.Contains(text, "Orion stopped") || !strings.Contains(text, "quota exhausted") {
		t.Errorf("text = %q; Slack renders this field and nothing else", text)
	}
	for _, k := range []string{"level", "title", "body", "workspace", "at"} {
		if _, ok := payload[k]; !ok {
			t.Errorf("payload is missing %q, so a non-Slack consumer cannot filter", k)
		}
	}
}

// Slack answers 200 on some failures, but a webhook that returns 4xx or 5xx
// has genuinely not delivered, and reporting success there would mean a
// failure notification silently going nowhere.
func TestWebhookTreatsNon2xxAsFailure(t *testing.T) {
	for _, code := range []int{300, 400, 404, 500} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(code)
		}))
		err := webhook(srv.URL, Event{Title: "t"})
		srv.Close()
		if err == nil {
			t.Errorf("status %d was treated as delivered", code)
		}
	}
}

func TestWebhookSucceedsOn2xx(t *testing.T) {
	for _, code := range []int{200, 201, 204} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(code)
		}))
		err := webhook(srv.URL, Event{Title: "t"})
		srv.Close()
		if err != nil {
			t.Errorf("status %d reported as a failure: %v", code, err)
		}
	}
}

// The custom command is for anyone whose notification path is not a webhook,
// so the event has to actually reach it.
func TestCustomCommandReceivesTheEventInItsEnvironment(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a POSIX shell")
	}
	out := filepath.Join(t.TempDir(), "env.txt")
	e := Event{Level: Warning, Title: "the title", Body: "the body", Workspace: "ws-1"}
	if err := custom("env | grep ^ORION_EVENT_ > "+out, e); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	got := string(b)
	for _, want := range []string{
		"ORION_EVENT_LEVEL=warning", "ORION_EVENT_TITLE=the title",
		"ORION_EVENT_BODY=the body", "ORION_EVENT_WORKSPACE=ws-1",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}

func TestCustomCommandFailureIsReported(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a POSIX shell")
	}
	if err := custom("exit 3", Event{Title: "t"}); err == nil {
		t.Error("a failing notify command was reported as success")
	}
}

// The body is AGENT OUTPUT. It reaches a desktop notifier by being
// interpolated into an AppleScript or PowerShell string, so a quote in an
// error message must not end the literal and let the rest run as script.
func TestQuotingCannotEscapeTheScriptLiteral(t *testing.T) {
	hostile := []string{
		`"; do shell script "rm -rf /"; "`,
		`' ; Remove-Item C:\ -Recurse ; '`,
		`back\slash "and" 'quotes'`,
		"",
	}
	for _, in := range hostile {
		a := appleQuote(in)
		if !strings.HasPrefix(a, `"`) || !strings.HasSuffix(a, `"`) {
			t.Errorf("appleQuote(%q) = %q is not a closed literal", in, a)
		}
		// Every quote inside must be escaped, so none of them can terminate
		// the literal early.
		inner := a[1 : len(a)-1]
		for i := 0; i < len(inner); i++ {
			if inner[i] == '"' && (i == 0 || inner[i-1] != '\\') {
				t.Errorf("appleQuote(%q) = %q has an unescaped quote", in, a)
				break
			}
		}

		p := psQuote(in)
		if !strings.HasPrefix(p, "'") || !strings.HasSuffix(p, "'") {
			t.Errorf("psQuote(%q) = %q is not a closed literal", in, p)
		}
		// PowerShell escapes ' by doubling it, so every internal run of
		// quotes must have even length.
		body := p[1 : len(p)-1]
		run := 0
		for i := 0; i < len(body); i++ {
			if body[i] == '\'' {
				run++
				continue
			}
			if run%2 != 0 {
				t.Errorf("psQuote(%q) = %q has an unbalanced quote run", in, p)
				break
			}
			run = 0
		}
		if run%2 != 0 {
			t.Errorf("psQuote(%q) = %q ends on an unbalanced quote run", in, p)
		}
	}
}

// A desktop notification shows one line. Sending a whole stack trace to a
// system notifier is how a useful alert becomes an unreadable blob.
func TestFirstLine(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"just one", "just one"},
		{"first\nsecond\nthird", "first"},
		{"\nleading newline", ""},
		{"", ""},
	} {
		if got := firstLine(tc.in); got != tc.want {
			t.Errorf("firstLine(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// The resolver exists so a webhook in Orion's config file works under cron,
// where a shell profile is never read.
func TestSetWebhookResolverIgnoresNil(t *testing.T) {
	isolate(t)
	SetWebhookResolver(func() string { return "http://example.test/hook" })
	SetWebhookResolver(nil)
	if got := webhookURL(); got != "http://example.test/hook" {
		t.Errorf("a nil resolver clobbered the configured one: %q", got)
	}
}

// A desktop notifier that is not installed is not an error worth reporting
// on every single event.
func TestDesktopIsSilentWhenNoNotifierExists(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	if err := desktop(Event{Title: "t", Body: "b"}); err != nil {
		t.Errorf("a missing notifier was reported as an error: %v", err)
	}
}

// The delivery path this package exists for. Untested until now, which is
// how the discarded-error bug survived.
func TestSlackIsSentOnlyWhenAChannelIsSet(t *testing.T) {
	isolate(t)

	var gotChannel, gotText string
	calls := 0
	SetSlackSender(func(ch, text string) error {
		calls++
		gotChannel, gotText = ch, text
		return nil
	})

	// No channel: nothing to post to, and posting into a default room would
	// be worse than staying quiet.
	Send(Event{Title: "t", Body: "b"})
	if calls != 0 {
		t.Fatalf("posted with no channel configured (%d calls)", calls)
	}

	Send(Event{Channel: "C123", Title: "Orion stopped", Body: "quota exhausted"})
	if calls != 1 {
		t.Fatalf("calls = %d, want 1", calls)
	}
	if gotChannel != "C123" {
		t.Errorf("channel = %q", gotChannel)
	}
	// Both halves must survive: a title with no body says something happened
	// and refuses to say what.
	if !strings.Contains(gotText, "Orion stopped") || !strings.Contains(gotText, "quota exhausted") {
		t.Errorf("text = %q; title and body must both reach Slack", gotText)
	}
}

// The bug this seam exposed: a missing or malformed token used to be
// swallowed, so the caller believed the notification had been delivered.
func TestSlackFailuresAreReportedNotSwallowed(t *testing.T) {
	isolate(t)
	SetSlackSender(func(string, string) error {
		return errors.New("ORION_SLACK_TOKEN is not set")
	})

	errs := Send(Event{Channel: "C123", Title: "t", Body: "b"})
	if len(errs) == 0 {
		t.Fatal("a Slack delivery failure was reported as success")
	}
	joined := ""
	for _, e := range errs {
		joined += e.Error() + " "
	}
	if !strings.Contains(joined, "slack") {
		t.Errorf("errors = %v; the failing channel must be identifiable", errs)
	}
	if !strings.Contains(joined, "ORION_SLACK_TOKEN") {
		t.Errorf("errors = %v; the underlying cause must survive", errs)
	}
}

// Slack failing must not stop the webhook, and vice versa. These are
// independent paths precisely so one workspace problem cannot mute all of
// them at once.
func TestSlackFailureDoesNotStopTheWebhook(t *testing.T) {
	isolate(t)
	SetSlackSender(func(string, string) error { return errors.New("token revoked") })

	delivered := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		delivered = true
	}))
	defer srv.Close()
	SetWebhookResolver(func() string { return srv.URL })

	errs := Send(Event{Channel: "C123", Title: "t", Body: "b"})
	if !delivered {
		t.Error("a Slack failure prevented the webhook from delivering")
	}
	if len(errs) != 1 {
		t.Errorf("errs = %v; exactly the Slack failure should be reported", errs)
	}
}
