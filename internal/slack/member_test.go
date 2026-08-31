package slack

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
)

// directory answers users.list, users.lookupByEmail and users.info, counting
// each so the caching claim can be checked rather than assumed.
type directory struct {
	mu     sync.Mutex
	calls  map[string]int
	pages  [][]map[string]any
	emails map[string]string
	fail   map[string]string // method -> Slack error code
}

func (d *directory) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		method := strings.TrimPrefix(r.URL.Path, "/")
		d.mu.Lock()
		if d.calls == nil {
			d.calls = map[string]int{}
		}
		d.calls[method]++
		page := d.calls[method] - 1
		d.mu.Unlock()

		if code, ok := d.fail[method]; ok {
			writeJSON(w, map[string]any{"ok": false, "error": code})
			return
		}
		switch method {
		case "users.list":
			if page >= len(d.pages) {
				writeJSON(w, map[string]any{"ok": true, "members": []any{}})
				return
			}
			out := map[string]any{"ok": true, "members": d.pages[page]}
			if page+1 < len(d.pages) {
				out["response_metadata"] = map[string]any{"next_cursor": fmt.Sprintf("p%d", page+1)}
			}
			writeJSON(w, out)
		case "users.lookupByEmail":
			_ = r.ParseForm()
			id, ok := d.emails[r.FormValue("email")]
			if !ok {
				writeJSON(w, map[string]any{"ok": false, "error": "users_not_found"})
				return
			}
			writeJSON(w, map[string]any{"ok": true, "user": map[string]any{"id": id}})
		default:
			writeJSON(w, map[string]any{"ok": true})
		}
	}
}

func (d *directory) count(method string) int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.calls[method]
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func person(id, name, display string) map[string]any {
	return map[string]any{
		"id": id, "name": name,
		"profile": map[string]any{"display_name": display, "real_name": display},
	}
}

// The whole point of this file: slack.merge_approvers holds a USERNAME or an
// EMAIL, and only a member id notifies. Both forms have to arrive as one.
func TestBothAUsernameAndAnEmailResolveToAMemberID(t *testing.T) {
	d := &directory{
		pages:  [][]map[string]any{{person("U012ABCDEF", "approver", "The Reviewer")}},
		emails: map[string]string{"reviewer@example.com": "U012ABCDEF"},
	}
	c, srv := newFake(t, d.handler())
	defer srv.Close()

	for _, in := range []string{"approver", "@approver", "The Reviewer", "reviewer@example.com"} {
		got, err := c.MemberID(in)
		if err != nil {
			t.Fatalf("MemberID(%q): %v", in, err)
		}
		if got != "U012ABCDEF" {
			t.Errorf("MemberID(%q) = %q, want U012ABCDEF", in, got)
		}
	}
}

// An id is already the answer. Spending an API call to confirm it would be
// the difference between a mention and no mention on a workspace that never
// granted users:read.
func TestAMemberIDNeedsNoLookup(t *testing.T) {
	d := &directory{}
	c, srv := newFake(t, d.handler())
	defer srv.Close()

	for _, in := range []string{"U012ABCDEF", "<@U012ABCDEF>", "W012ABCDEF"} {
		got, err := c.MemberID(in)
		if err != nil {
			t.Fatalf("MemberID(%q): %v", in, err)
		}
		if !strings.HasPrefix(got, "U") && !strings.HasPrefix(got, "W") {
			t.Errorf("MemberID(%q) = %q", in, got)
		}
	}
	if d.count("users.list")+d.count("users.lookupByEmail") != 0 {
		t.Error("an id that is already an id was looked up anyway")
	}
}

// Approvers change rarely and a lookup costs an API call per approver per
// message. One resolution per run, including the one that failed -- a typo
// in merge_approvers must not re-ask on every message either.
func TestResolutionIsCachedIncludingFailures(t *testing.T) {
	d := &directory{pages: [][]map[string]any{{person("U1", "approver", "Approver")}}}
	c, srv := newFake(t, d.handler())
	defer srv.Close()

	for i := 0; i < 5; i++ {
		if _, err := c.MemberID("approver"); err != nil {
			t.Fatalf("MemberID: %v", err)
		}
		if _, err := c.MemberID("typo"); err == nil {
			t.Fatal("a name nobody has must not resolve")
		}
	}
	if n := d.count("users.list"); n != 1 {
		t.Errorf("read the workspace directory %d times; once per run is the budget", n)
	}
}

// A workspace larger than one page must still resolve, or an approver simply
// past the page boundary is silently never notified.
func TestTheDirectoryPaginates(t *testing.T) {
	d := &directory{pages: [][]map[string]any{
		{person("U1", "somebody", "Somebody")},
		{person("U2", "approver", "Approver")},
	}}
	c, srv := newFake(t, d.handler())
	defer srv.Close()

	got, err := c.MemberID("approver")
	if err != nil {
		t.Fatalf("MemberID: %v", err)
	}
	if got != "U2" {
		t.Errorf("MemberID = %q, want U2 from the second page", got)
	}
}

// The allowlist names who may APPROVE. Broadcasting to a channel Orion
// created per project reaches people with no standing to answer, and a room
// that gets tagged for everything is a room that gets muted.
func TestABroadcastIsRefusedRatherThanMentioned(t *testing.T) {
	d := &directory{pages: [][]map[string]any{{person("U1", "channel", "channel")}}}
	c, srv := newFake(t, d.handler())
	defer srv.Close()

	for _, in := range []string{"channel", "@here", "<@everyone>", "HERE"} {
		id, err := c.MemberID(in)
		if err == nil {
			t.Errorf("MemberID(%q) resolved to %q; it must be refused", in, id)
		}
	}
}

// users:read.email is not in Orion's default manifest, so this failure is
// ordinary. What it must not be is silent: the error has to name the scope,
// because "users_not_found" tells a person nothing they can act on.
func TestAMissingEmailScopeSaysWhichScope(t *testing.T) {
	d := &directory{fail: map[string]string{"users.lookupByEmail": "missing_scope"}}
	c, srv := newFake(t, d.handler())
	defer srv.Close()

	_, err := c.MemberID("reviewer@example.com")
	if err == nil {
		t.Fatal("a missing scope must be reported")
	}
	if !strings.Contains(err.Error(), "users:read.email") {
		t.Errorf("the error does not name the scope to add: %v", err)
	}
	if !strings.Contains(err.Error(), "REINSTALL") {
		t.Errorf("a scope added without a reinstall does not reach the token: %v", err)
	}
}

// A username needs users:read, not users:read.email. That scope has to be
// named specifically, or a workspace missing it gets the wrong install
// instructions and still cannot resolve a single name.
func TestAMissingDirectoryScopeSaysWhichScope(t *testing.T) {
	d := &directory{fail: map[string]string{"users.list": "missing_scope"}}
	c, srv := newFake(t, d.handler())
	defer srv.Close()

	_, err := c.MemberID("approver")
	if err == nil {
		t.Fatal("a missing scope must be reported")
	}
	if !strings.Contains(err.Error(), "users:read") || strings.Contains(err.Error(), "users:read.email") {
		t.Errorf("the error names the wrong scope: %v", err)
	}
}

// The value a person actually copies out of Slack carries both the mention
// brackets and stray whitespace at once -- not one or the other. Both have to
// come off before the lookup, or a value pasted straight from a message never
// resolves.
func TestDecorationAndWhitespaceTogetherAreNormalized(t *testing.T) {
	d := &directory{pages: [][]map[string]any{{person("U012ABCDEF", "approver", "Approver")}}}
	c, srv := newFake(t, d.handler())
	defer srv.Close()

	for _, in := range []string{"  <@U012ABCDEF>  ", "  @approver  ", "\t<@approver>\n"} {
		got, err := c.MemberID(in)
		if err != nil {
			t.Fatalf("MemberID(%q): %v", in, err)
		}
		if got != "U012ABCDEF" {
			t.Errorf("MemberID(%q) = %q, want U012ABCDEF", in, got)
		}
	}
}

// A bot's name resolving would tag something that cannot approve, and a
// deleted account's name would tag nobody at all -- both look like a working
// mention right up until the merge waits forever.
func TestBotsAndDeletedAccountsAreNotMentionable(t *testing.T) {
	bot := person("UBOT", "orion", "Orion")
	bot["is_bot"] = true
	gone := person("UOLD", "leaver", "A Leaver")
	gone["deleted"] = true
	d := &directory{pages: [][]map[string]any{{bot, gone}}}
	c, srv := newFake(t, d.handler())
	defer srv.Close()

	for _, in := range []string{"orion", "leaver"} {
		if id, err := c.MemberID(in); err == nil {
			t.Errorf("MemberID(%q) = %q; it cannot approve anything", in, id)
		}
	}
}
