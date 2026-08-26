package slack

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode"
)

// Slack rejects a malformed channel name rather than normalising it, and the
// rejection arrives after a workspace has already been provisioned. Cheaper
// to guarantee the name is acceptable before the call.
func TestNormalizeChannelNameAlwaysValid(t *testing.T) {
	inputs := []string{
		"customers-should-see-claim-status-7df0cf",
		"Add Rate Limiting To The Status Endpoint",
		"UPPER.case/with.dots",
		"trailing-dash-", "-leading-dash", "__underscores__",
		"emoji-🚀-thing", "日本語のみ", "", "   ", "...",
		strings.Repeat("very-long-slug-", 20),
	}
	for _, in := range inputs {
		got := NormalizeChannelName(in)
		if got == "" {
			t.Errorf("NormalizeChannelName(%q) is empty", in)
		}
		if len(got) > 80 {
			t.Errorf("NormalizeChannelName(%q) = %d chars, Slack allows 80", in, len(got))
		}
		if strings.HasPrefix(got, "-") || strings.HasSuffix(got, "-") {
			t.Errorf("NormalizeChannelName(%q) = %q: leading/trailing separator", in, got)
		}
		for _, r := range got {
			ok := (unicode.IsLetter(r) && r < 128) || unicode.IsDigit(r) || r == '-' || r == '_'
			if !ok {
				t.Errorf("NormalizeChannelName(%q) = %q contains %q", in, got, r)
			}
			if unicode.IsUpper(r) {
				t.Errorf("NormalizeChannelName(%q) = %q is not lowercase", in, got)
			}
		}
	}
}

func TestNormalizeChannelNameShape(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"Claim Status", "claim-status"},
		{"a//b..c", "a-b-c"},
		{"already-fine", "already-fine"},
		{"日本語のみ", "orion"}, // nothing ASCII survives; a placeholder beats a guess
	} {
		if got := NormalizeChannelName(tc.in); got != tc.want {
			t.Errorf("NormalizeChannelName(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// newFake returns a client pointed at a test server. The api const is a
// package-level string, so the test rewrites the client's transport instead.
func newFake(t *testing.T, handler http.HandlerFunc) (*Client, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(handler)
	c := &Client{
		Token: "xoxb-test",
		HTTP: &http.Client{Transport: rewrite{
			base: srv.URL, rt: http.DefaultTransport,
		}},
	}
	return c, srv
}

type rewrite struct {
	base string
	rt   http.RoundTripper
}

func (r rewrite) RoundTrip(req *http.Request) (*http.Response, error) {
	u := *req.URL
	method := strings.TrimPrefix(u.Path, "/api/")
	nu, err := req.URL.Parse(r.base + "/" + method)
	if err != nil {
		return nil, err
	}
	req2 := req.Clone(req.Context())
	req2.URL = nu
	req2.Host = nu.Host
	return r.rt.RoundTrip(req2)
}

// The single most important behaviour: Slack answers HTTP 200 on failure.
// A client that checks only the status code reports success for every error.
func TestFailureArrivesAsHTTP200(t *testing.T) {
	c, srv := newFake(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":false,"error":"missing_scope"}`))
	})
	defer srv.Close()

	_, err := c.AuthTest()
	if err == nil {
		t.Fatal("a 200 carrying ok:false must be an error")
	}
	if !strings.Contains(err.Error(), "missing_scope") {
		t.Errorf("error should carry Slack's code, got: %v", err)
	}
	// The remedy people actually miss is reinstalling after adding a scope.
	if !strings.Contains(err.Error(), "REINSTALL") {
		t.Errorf("missing_scope should mention reinstalling the app, got: %v", err)
	}
}

// Provisioning must be idempotent: an existing channel is attached to, not
// an error. A half-provisioned workspace is worse than a reused channel.
func TestCreateChannelReusesExisting(t *testing.T) {
	var listed bool
	c, srv := newFake(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "conversations.create"):
			_, _ = w.Write([]byte(`{"ok":false,"error":"name_taken"}`))
		case strings.HasSuffix(r.URL.Path, "conversations.list"):
			listed = true
			_, _ = w.Write([]byte(`{"ok":true,"channels":[{"id":"C123","name":"claim-status"}],"response_metadata":{"next_cursor":""}}`))
		default:
			_, _ = w.Write([]byte(`{"ok":true}`))
		}
	})
	defer srv.Close()

	ch, err := c.CreateChannel("Claim Status", false)
	if err != nil {
		t.Fatalf("name_taken should resolve to the existing channel, got: %v", err)
	}
	if !listed {
		t.Error("expected a lookup after name_taken")
	}
	if ch.ID != "C123" {
		t.Errorf("channel = %+v, want C123", ch)
	}
	if ch.Created {
		t.Error("a reused channel must not report Created")
	}
}

// A workspace with many channels returns paginated results; stopping at the
// first page reports "not found" for a channel that plainly exists.
func TestFindChannelPaginates(t *testing.T) {
	page := 0
	c, srv := newFake(t, func(w http.ResponseWriter, r *http.Request) {
		page++
		if page == 1 {
			_, _ = w.Write([]byte(`{"ok":true,"channels":[{"id":"C1","name":"other"}],"response_metadata":{"next_cursor":"abc"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"ok":true,"channels":[{"id":"C9","name":"wanted"}],"response_metadata":{"next_cursor":""}}`))
	})
	defer srv.Close()

	ch, err := c.FindChannel("wanted", false)
	if err != nil {
		t.Fatal(err)
	}
	if ch.ID != "C9" {
		t.Errorf("got %+v, want C9 from the second page", ch)
	}
	if page < 2 {
		t.Error("expected pagination to continue past the first page")
	}
}

func TestPostSendsChannelAndText(t *testing.T) {
	var got map[string]any
	c, srv := newFake(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		_, _ = w.Write([]byte(`{"ok":true}`))
	})
	defer srv.Close()

	if err := c.Post("C123", "hello"); err != nil {
		t.Fatal(err)
	}
	if got["channel"] != "C123" || got["text"] != "hello" {
		t.Errorf("payload = %v", got)
	}
}

func TestChannelURL(t *testing.T) {
	if got := ChannelURL("T1", "C1"); got != "https://app.slack.com/client/T1/C1" {
		t.Errorf("ChannelURL = %q", got)
	}
	if got := ChannelURL("", "C1"); got != "" {
		t.Errorf("an unknown team must yield no link, got %q", got)
	}
}
