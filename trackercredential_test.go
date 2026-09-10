package orion

// OR-278. The tracker credential must never enter anything Orion writes down
// or draws.
//
// The board polls Jira on a timer for ticket stage (OR-77), and a poller that
// has to survive a restart unattended is exactly where somebody stores the
// token beside the state it already keeps -- in the registry, in the config,
// or on the snapshot the page renders. Every one of those is read by something
// else: a backup, a synced dotfiles repo, or the page itself. A Jira token
// carries issue-read and usually issue-write scope across every project the
// account can see, so one of those readers leaks all of them.
//
// The rule the code follows instead is in internal/web/tickets.go: the
// credential is resolved once at process start (internal/creds reads the
// environment, then Orion's own 0600 file) and the board is handed an
// ALREADY-AUTHENTICATED client behind a one-method interface. Nothing in the
// board's model can hold a credential because nothing is ever given one.
//
// These tests are the negative half of that: they fail if a credential-shaped
// field appears in the registry file, in the config, or anywhere reachable
// from the snapshot web.Scan produces -- whether or not anyone remembers why
// it was forbidden. A rule with nothing checking it is a comment.
//
// LIVES AT THE ROOT rather than in three packages because it is one property
// about three of them, and three copies of the same walker drift.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/orion-sdlc/orion/internal/config"
	"github.com/orion-sdlc/orion/internal/registry"
	"github.com/orion-sdlc/orion/internal/web"
)

// credentialWords are whole words that make a field a secret. Matched as
// WORDS, not substrings: "project_key" and "monkey" are not credentials, and a
// check that flagged them would be a check people delete.
//
// "auth" and "pass" are deliberately absent. They are the halves of words this
// repository already uses for other things -- ProductionRequiresAuth, an eval
// PASS rate -- and a real credential named with either carries a second word
// that is here (auth_token, password).
var credentialWords = map[string]bool{
	"token": true, "tokens": true,
	"secret": true, "secrets": true,
	"password": true, "passwd": true,
	"credential": true, "credentials": true,
	"bearer": true, "cookie": true, "pat": true, "apikey": true,
}

// keyQualifiers make a following "key" mean a cryptographic one. "key" alone
// is the tracker's project key, which is public and is half this repository's
// vocabulary.
var keyQualifiers = map[string]bool{
	"api": true, "private": true, "access": true, "secret": true,
	"signing": true, "encryption": true,
}

// exemptFields names Go fields that trip the word check and are not
// credentials; exemptJSON does the same for the serialized keys. Every entry
// needs a reason, because an exemption list is how a check stops checking.
var exemptFields = map[string]string{
	"config.Config.Budget.WeeklyTokens": "a spend ceiling in model tokens, not an auth token",
}

var exemptJSON = map[string]string{
	"orion.json.budget.weekly_tokens":                 "as above",
	"orion.json (round-tripped).budget.weekly_tokens": "as above",
}

// words splits a Go field name or a json tag into lowercase words.
func words(s string) []string {
	var out []string
	var cur strings.Builder
	flush := func() {
		if cur.Len() > 0 {
			out = append(out, strings.ToLower(cur.String()))
			cur.Reset()
		}
	}
	prevUpper := false
	for _, r := range s {
		switch {
		case r == '_' || r == '-' || r == ' ':
			flush()
		case r >= 'A' && r <= 'Z':
			// Split camelCase, but keep an acronym together: JiraURL is
			// "jira", "url", not "j", "i", "r", "a".
			if !prevUpper {
				flush()
			}
			cur.WriteRune(r)
			prevUpper = true
			continue
		default:
			cur.WriteRune(r)
		}
		prevUpper = false
	}
	flush()
	return out
}

// credentialShaped reports whether a name reads as a secret, and which word
// gave it away.
func credentialShaped(name string) (string, bool) {
	w := words(name)
	for i, word := range w {
		if credentialWords[word] {
			return word, true
		}
		if word == "key" && i > 0 && keyQualifiers[w[i-1]] {
			return w[i-1] + " key", true
		}
	}
	return "", false
}

// walkForCredentials reports every credential-shaped field reachable from typ,
// by both its Go name and its json tag.
//
// Reachable, not declared, because that is what serialization follows: a
// credential three structs down still lands in the file.
func walkForCredentials(t *testing.T, typ reflect.Type, path string, seen map[reflect.Type]bool) {
	t.Helper()

	for typ.Kind() == reflect.Ptr || typ.Kind() == reflect.Slice ||
		typ.Kind() == reflect.Array || typ.Kind() == reflect.Map {
		if typ.Kind() == reflect.Slice || typ.Kind() == reflect.Array || typ.Kind() == reflect.Map {
			path += "[]"
		}
		typ = typ.Elem()
	}
	if typ.Kind() != reflect.Struct || seen[typ] {
		return
	}
	seen[typ] = true

	// time.Time and friends are opaque values, not places a token hides.
	if typ == reflect.TypeOf(time.Time{}) {
		return
	}

	for i := 0; i < typ.NumField(); i++ {
		f := typ.Field(i)
		fieldPath := path + "." + f.Name
		if _, ok := exemptFields[fieldPath]; !ok {
			names := []string{f.Name}
			if tag := strings.Split(f.Tag.Get("json"), ",")[0]; tag != "" && tag != "-" {
				names = append(names, tag)
			}
			for _, n := range names {
				if word, bad := credentialShaped(n); bad {
					t.Errorf("%s (%q) reads as a credential (%q).\n"+
						"  OR-278: the tracker credential is resolved at process start and the\n"+
						"  board is handed an already-authenticated client (internal/web/tickets.go).\n"+
						"  Nothing that gets serialized to ORION_HOME or rendered into the board\n"+
						"  may hold one -- a backup or a synced dotfiles repo reads this file.",
						fieldPath, n, word)
				}
			}
		}
		walkForCredentials(t, f.Type, fieldPath, seen)
	}
}

// The registry is written to ORION_HOME on every bind. If a poller's token
// ever gets parked on an Entry, it lands in repos.json.
func TestRegistryTypeHoldsNoCredential(t *testing.T) {
	walkForCredentials(t, reflect.TypeOf(registry.File{}), "registry.File", map[reflect.Type]bool{})
}

// orion.json is committed in the repositories Orion works. A credential here
// would not merely leak locally; it would be pushed.
func TestConfigTypeHoldsNoCredential(t *testing.T) {
	walkForCredentials(t, reflect.TypeOf(config.Config{}), "config.Config", map[reflect.Type]bool{})
}

// The snapshot is what the page renders. web.Scan builds it from the event
// log; OR-77 fills the tracker's half of it through web.Tickets, which is
// narrow precisely so the client behind it cannot come along.
func TestBoardModelHoldsNoCredential(t *testing.T) {
	seen := map[reflect.Type]bool{}
	walkForCredentials(t, reflect.TypeOf(web.Snapshot{}), "web.Snapshot", seen)
	walkForCredentials(t, reflect.TypeOf(web.Ticket{}), "web.Ticket", seen)
}

// The type walk says no field is NAMED like a credential. This says the bytes
// actually written contain no key that is -- the round trip the ticket asks
// for, over the real file the real code writes.
func TestRegistryRoundTripWritesNoTokenField(t *testing.T) {
	home := t.TempDir()
	source := t.TempDir()

	// Every string field is filled, by reflection rather than by hand, so an
	// `omitempty` credential cannot hide behind its zero value -- a field that
	// only appears in the file once something sets it is exactly the field a
	// poller would set. A field added later is filled too, without this test
	// being updated to know about it.
	e := registry.Entry{}
	rv := reflect.ValueOf(&e).Elem()
	for i := 0; i < rv.NumField(); i++ {
		if f := rv.Field(i); f.Kind() == reflect.String && f.CanSet() {
			f.SetString("filled")
		}
	}
	e.Key = "OR"
	e.Source = source
	e.Workspace = filepath.Join(home, "ws")

	if err := registry.Bind(home, e); err != nil {
		t.Fatal(err)
	}

	b, err := os.ReadFile(filepath.Join(home, "repos.json"))
	if err != nil {
		t.Fatal(err)
	}
	assertNoCredentialKeys(t, "repos.json", b)

	// And it survives a reload unchanged: a round trip that dropped the file
	// and rebuilt it would prove nothing about what the file holds.
	f, err := registry.Load(home)
	if err != nil {
		t.Fatal(err)
	}
	again, err := json.Marshal(f)
	if err != nil {
		t.Fatal(err)
	}
	assertNoCredentialKeys(t, "repos.json (reloaded)", again)
}

// The same for the config Orion ships and writes.
func TestConfigRoundTripWritesNoTokenField(t *testing.T) {
	b, err := json.Marshal(config.Defaults())
	if err != nil {
		t.Fatal(err)
	}
	assertNoCredentialKeys(t, "orion.json", b)

	var back config.Config
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	again, err := json.Marshal(back)
	if err != nil {
		t.Fatal(err)
	}
	assertNoCredentialKeys(t, "orion.json (round-tripped)", again)
}

// assertNoCredentialKeys walks decoded JSON and fails on a credential-shaped
// object key at any depth.
func assertNoCredentialKeys(t *testing.T, what string, b []byte) {
	t.Helper()

	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatalf("%s is not valid JSON: %v", what, err)
	}

	var walk func(any, string)
	walk = func(v any, path string) {
		switch x := v.(type) {
		case map[string]any:
			for k, sub := range x {
				p := path + "." + k
				if _, ok := exemptJSON[what+p]; !ok {
					if word, bad := credentialShaped(k); bad {
						t.Errorf("%s contains %s, which reads as a credential (%q).\n"+
							"  OR-278: this file is read by backups and by anything syncing\n"+
							"  ORION_HOME. The tracker credential is resolved at process start\n"+
							"  and never written here.", what, p, word)
					}
				}
				walk(sub, p)
			}
		case []any:
			for _, sub := range x {
				walk(sub, path+"[]")
			}
		}
	}
	walk(v, "")
}
