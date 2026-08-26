package slack

import (
	"net/http"
	"net/http/httptest"
	"net/url"
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
	var got url.Values
	c, srv := newFake(t, func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		got = r.PostForm
		_, _ = w.Write([]byte(`{"ok":true}`))
	})
	defer srv.Close()

	if err := c.Post("C123", "hello"); err != nil {
		t.Fatal(err)
	}
	if got.Get("channel") != "C123" || got.Get("text") != "hello" {
		t.Errorf("payload = %v", got)
	}
}

// Arguments must travel as FORM fields, not a JSON body.
//
// Slack's cursor-paginated read methods ignore a JSON body and answer with
// the DEFAULT result set and ok:true -- no error, just a wrong answer that
// looks right. With JSON, conversations.list silently dropped types, limit,
// cursor and exclude_archived, so private channels were invisible (and
// private is the default), an existing channel read as missing, name_taken
// could never resolve to it, and pagination re-fetched page one forever.
func TestArgumentsAreFormEncodedNotJSON(t *testing.T) {
	var ct string
	var got url.Values
	c, srv := newFake(t, func(w http.ResponseWriter, r *http.Request) {
		ct = r.Header.Get("Content-Type")
		_ = r.ParseForm()
		got = r.PostForm
		_, _ = w.Write([]byte(`{"ok":true,"channels":[],"response_metadata":{"next_cursor":""}}`))
	})
	defer srv.Close()

	_, _ = c.FindChannel("nope", true)

	if !strings.HasPrefix(ct, "application/x-www-form-urlencoded") {
		t.Errorf("Content-Type = %q; Slack ignores a JSON body on this method", ct)
	}
	// BOTH types, always. Slack's channel namespace is global across public
	// and private, so a lookup narrowed to the kind Orion wanted cannot
	// answer "who holds this name" -- which is the only question the
	// name_taken fallback is asking.
	if !strings.Contains(got.Get("types"), "private_channel") ||
		!strings.Contains(got.Get("types"), "public_channel") {
		t.Errorf("types = %q; both kinds must be searched, or a name held by the other kind is unresolvable",
			got.Get("types"))
	}
	if got.Get("limit") != "200" {
		t.Errorf("limit = %q, want the integer form-encoded", got.Get("limit"))
	}
	// Archived channels are INCLUDED, deliberately. An archived channel
	// still holds its name: conversations.create refuses with name_taken and
	// the fallback lookup then finds nothing, which reported the genuinely
	// unreadable "orion exists but could not be resolved: channel not found".
	if got.Get("exclude_archived") != "false" {
		t.Errorf("exclude_archived = %q; archived channels hold their names and must be findable",
			got.Get("exclude_archived"))
	}
}

// The archived case, end to end: creating is refused because the name is
// taken, the lookup finds the archived channel, and the caller is told the
// truth rather than being handed a room that accepts no messages.
func TestAnArchivedChannelIsRefusedWithAWayOut(t *testing.T) {
	c, srv := newFake(t, func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if strings.HasSuffix(r.URL.Path, "conversations.create") {
			_, _ = w.Write([]byte(`{"ok":false,"error":"name_taken"}`))
			return
		}
		_, _ = w.Write([]byte(`{"ok":true,"channels":[{"id":"C1","name":"orion","is_archived":true}],` +
			`"response_metadata":{"next_cursor":""}}`))
	})
	defer srv.Close()

	ch, err := c.CreateChannel("orion", true)
	if err == nil {
		t.Fatalf("bound to an archived channel: %+v", ch)
	}
	for _, want := range []string{"ARCHIVED", "Un-archive", "channel_prefix"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error must contain %q, got: %v", want, err)
		}
	}
}

// Pagination only advances if the cursor actually reaches Slack.
func TestPaginationSendsTheCursor(t *testing.T) {
	var cursors []string
	page := 0
	c, srv := newFake(t, func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		cursors = append(cursors, r.PostForm.Get("cursor"))
		page++
		if page == 1 {
			_, _ = w.Write([]byte(`{"ok":true,"channels":[],"response_metadata":{"next_cursor":"CUR2"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"ok":true,"channels":[{"id":"C9","name":"wanted"}],"response_metadata":{"next_cursor":""}}`))
	})
	defer srv.Close()

	if _, err := c.FindChannel("wanted", false); err != nil {
		t.Fatal(err)
	}
	if len(cursors) < 2 || cursors[1] != "CUR2" {
		t.Errorf("cursors sent = %v; without the cursor the same page is fetched forever", cursors)
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

// A bot is a member of a channel it CREATED, but not of one it merely found.
// setTopic requires membership, so the reuse path must join first or every
// later call fails with not_in_channel.
func TestReusedPublicChannelIsJoined(t *testing.T) {
	joined := false
	c, srv := newFake(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "conversations.create"):
			_, _ = w.Write([]byte(`{"ok":false,"error":"name_taken"}`))
		case strings.HasSuffix(r.URL.Path, "conversations.list"):
			_, _ = w.Write([]byte(`{"ok":true,"channels":[{"id":"C1","name":"reused"}],"response_metadata":{"next_cursor":""}}`))
		case strings.HasSuffix(r.URL.Path, "conversations.join"):
			joined = true
			_, _ = w.Write([]byte(`{"ok":true}`))
		default:
			_, _ = w.Write([]byte(`{"ok":true}`))
		}
	})
	defer srv.Close()

	if _, err := c.CreateChannel("reused", false); err != nil {
		t.Fatal(err)
	}
	if !joined {
		t.Error("a reused PUBLIC channel must be joined, or setTopic fails with not_in_channel")
	}
}

// Slack provides no way for an app to add itself to a private channel. That
// is a policy choice, so Orion must not attempt it and pretend otherwise.
func TestReusedPrivateChannelIsNotJoined(t *testing.T) {
	joined := false
	c, srv := newFake(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "conversations.create"):
			_, _ = w.Write([]byte(`{"ok":false,"error":"name_taken"}`))
		case strings.HasSuffix(r.URL.Path, "conversations.list"):
			_, _ = w.Write([]byte(`{"ok":true,"channels":[{"id":"C2","name":"secret"}],"response_metadata":{"next_cursor":""}}`))
		case strings.HasSuffix(r.URL.Path, "conversations.join"):
			joined = true
			_, _ = w.Write([]byte(`{"ok":false,"error":"method_not_supported_for_channel_type"}`))
		default:
			_, _ = w.Write([]byte(`{"ok":true}`))
		}
	})
	defer srv.Close()

	if _, err := c.CreateChannel("secret", true); err != nil {
		t.Fatalf("a reused private channel should still resolve: %v", err)
	}
	if joined {
		t.Error("must not attempt to self-join a private channel; a human has to invite the bot")
	}
}

// not_in_channel is the error a user will actually hit, so it must explain
// the private-channel invite rather than just naming the code.
func TestNotInChannelExplainsTheInvite(t *testing.T) {
	e := &APIError{Method: "chat.postMessage", Code: "not_in_channel"}
	if !strings.Contains(e.Error(), "/invite") {
		t.Errorf("not_in_channel should tell the user to invite the bot, got: %v", e)
	}
}
