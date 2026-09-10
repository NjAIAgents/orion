package orion

// OR-278, cases 6-10. See trackercredential_test.go for the shared word list,
// the walker, and the reasoning this file assumes rather than repeats.
//
// The existing suite exercises the real write paths (registry.Bind,
// config.Defaults) end to end. These cases exercise the TYPES themselves,
// directly, so a credential field cannot hide behind a code path that
// happens not to set it: a struct literal built by reflection fills every
// string field, `omitempty` included, before it is ever marshaled.

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/orion-sdlc/orion/internal/config"
	"github.com/orion-sdlc/orion/internal/registry"
	"github.com/orion-sdlc/orion/internal/web"
)

// init extends the shared exemptJSON list (trackercredential_test.go) with
// the "what" labels this file uses. budget.weekly_tokens is already exempted
// under the labels the other round-trip tests use; these are the same field,
// reached through a different marshal call with a different label.
func init() {
	for _, what := range []string{
		"config.Config (fully filled)",
		"config.Config (fully filled, round-tripped)",
	} {
		exemptJSON[what+".budget.weekly_tokens"] = "a spend ceiling in model tokens, not an auth token"
	}
}

// fillStrings sets every settable string field reachable from v to a
// non-empty value, so a field guarded by `omitempty` still appears in the
// marshaled JSON. That is precisely how a credential field would hide: a
// zero-value token never round-trips into the file that gets read.
//
// No cycle guard: none of the types this is used on (registry.File,
// registry.Entry, config.Config) are self-referential.
func fillStrings(v reflect.Value) {
	for v.Kind() == reflect.Ptr {
		if v.IsNil() {
			if !v.CanSet() {
				return
			}
			v.Set(reflect.New(v.Type().Elem()))
		}
		v = v.Elem()
	}
	if v.Kind() != reflect.Struct {
		return
	}
	for i := 0; i < v.NumField(); i++ {
		f := v.Field(i)
		if !f.CanSet() {
			continue
		}
		if f.Kind() == reflect.String {
			f.SetString("filled")
			continue
		}
		fillStrings(f)
	}
}

// Case 6: registry.File, marshaled directly, produces no credential-shaped
// JSON key -- the on-disk shape itself, not only the Bind/Load path that
// already covers TestRegistryRoundTripWritesNoTokenField.
func TestRegistryFileRoundTripHasNoCredentialKey(t *testing.T) {
	var f registry.File
	fillStrings(reflect.ValueOf(&f).Elem())

	var entry registry.Entry
	fillStrings(reflect.ValueOf(&entry).Elem())
	f.Repos = map[string]registry.Entry{"OR": entry}

	b, err := json.Marshal(f)
	if err != nil {
		t.Fatal(err)
	}
	assertNoCredentialKeys(t, "registry.File", b)

	var back registry.File
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	again, err := json.Marshal(back)
	if err != nil {
		t.Fatal(err)
	}
	assertNoCredentialKeys(t, "registry.File (round-tripped)", again)
}

// Case 7: config.Config, marshaled directly with every string field filled
// (not just the shipped Defaults(), which leaves most optional fields at
// their zero value), produces no credential-shaped JSON key.
func TestConfigStructRoundTripHasNoCredentialKey(t *testing.T) {
	var c config.Config
	fillStrings(reflect.ValueOf(&c).Elem())

	b, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	assertNoCredentialKeys(t, "config.Config (fully filled)", b)

	var back config.Config
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	again, err := json.Marshal(back)
	if err != nil {
		t.Fatal(err)
	}
	assertNoCredentialKeys(t, "config.Config (fully filled, round-tripped)", again)
}

// Case 8: any new struct type added anywhere reachable from web.Snapshot,
// registry.File or config.Config is walked by walkForCredentials regardless
// of how deep it sits -- the walk recurses into every field's type, not just
// the top-level ones, and TestRegistryTypeHoldsNoCredential /
// TestConfigTypeHoldsNoCredential / TestBoardModelHoldsNoCredential already
// call it on those three roots. This test pins that the walk itself is what
// catches a new struct, by exercising it against a struct defined here that
// is reachable only through an added field -- proving the mechanism a real
// regression would rely on, without editing production types.
type addedByAPoller struct {
	APIToken string `json:"api_token"`
}

type hostWithNewField struct {
	Existing string `json:"existing"`
	Added    addedByAPoller
}

func TestNewStructTypeFailsTheCredentialWordCheck(t *testing.T) {
	rt := reflect.TypeOf(hostWithNewField{})
	sub := &testing.T{}
	walkForCredentials(sub, rt, "hostWithNewField", map[reflect.Type]bool{})
	if !sub.Failed() {
		t.Fatal("walkForCredentials did not flag a credential field on a struct nested two levels deep; " +
			"a new type reachable from Snapshot/File/Config would pass silently")
	}
}

// Case 9: a field added to an EXISTING type (rather than a new type) is
// caught the same way. registry.Entry is real production state; this proves
// the walk flags a token-shaped field on it without requiring the field to
// actually be added to the source (which would defeat the point of a test
// that must keep passing).
func TestNewFieldOnExistingTypeFailsTheCredentialWordCheck(t *testing.T) {
	type entryWithToken struct {
		registry.Entry
		APIKey string `json:"api_key"`
	}

	sub := &testing.T{}
	walkForCredentials(sub, reflect.TypeOf(entryWithToken{}), "registry.Entry", map[reflect.Type]bool{})
	if !sub.Failed() {
		t.Fatal("walkForCredentials did not flag a credential field added to an existing type")
	}
}

// Case 10: web.Tickets' method set structurally cannot accept or return a
// credential -- no parameter or result whose type name reads as a token, a
// client, or a config. Read from the interface's reflect.Type rather than
// from a specific implementation, so widening the interface itself (not just
// stubTickets) is what this catches.
func TestTicketsInterfaceSignatureCannotCarryACredential(t *testing.T) {
	forbidden := []string{"token", "config", "client"}

	typ := reflect.TypeOf((*web.Tickets)(nil)).Elem()
	if typ.NumMethod() == 0 {
		t.Fatal("web.Tickets has no methods; nothing to check")
	}

	check := func(paramOrResult string, tp reflect.Type) {
		name := strings.ToLower(tp.String())
		for _, word := range forbidden {
			if strings.Contains(name, word) {
				t.Errorf("web.Tickets method %s has type %s, which reads as %q -- "+
					"a credential must never be accepted or returned by this interface",
					paramOrResult, tp.String(), word)
			}
		}
		// A pointer to an unexported/struct type flowing in or out is exactly
		// the shape a *Config or *Client argument would take.
		if tp.Kind() == reflect.Ptr {
			t.Errorf("web.Tickets method %s is a pointer type (%s); "+
				"Tickets takes and returns only plain values, never a pointer to a client or config",
				paramOrResult, tp.String())
		}
	}

	for i := 0; i < typ.NumMethod(); i++ {
		m := typ.Method(i)
		for j := 0; j < m.Type.NumIn(); j++ {
			check(m.Name+" param", m.Type.In(j))
		}
		for j := 0; j < m.Type.NumOut(); j++ {
			out := m.Type.Out(j)
			if out.Implements(reflect.TypeOf((*error)(nil)).Elem()) {
				continue
			}
			check(m.Name+" result", out)
		}
	}
}
