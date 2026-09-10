package orion

// OR-278, cases 20-23. See trackercredential_test.go for the shared word
// list, the walker, and the reasoning this file assumes rather than repeats;
// see trackercredential_serialization_test.go for fillStrings.

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/orion-sdlc/orion/internal/config"
	"github.com/orion-sdlc/orion/internal/registry"
	"github.com/orion-sdlc/orion/internal/web"
)

// Case 20: a credential-shaped field guarded by `omitempty` at its zero
// value produces no JSON key at all -- a value-based scan of the marshaled
// bytes would see nothing to flag. walkForCredentials does not have that
// blind spot because it reads the field's Go name and json tag off the
// reflect.Type, never the value, so a field never gets a pass just because
// nobody has set it yet. This pins that a hidden, never-populated credential
// field is still caught -- the exact shape an `omitempty` token field would
// take the day after it is added and before anything sets it.
type hiddenUntilSet struct {
	Existing string `json:"existing"`
	APIToken string `json:"api_token,omitempty"`
}

func TestOmittedZeroValueCredentialFieldStillFailsTheTypeCheck(t *testing.T) {
	var zero hiddenUntilSet
	b, err := json.Marshal(zero)
	if err != nil {
		t.Fatal(err)
	}
	// Confirms the blind spot exists: the zero-valued, omitempty field left
	// no trace in the bytes a value-based check would have to work from.
	var decoded map[string]any
	if err := json.Unmarshal(b, &decoded); err != nil {
		t.Fatal(err)
	}
	if _, present := decoded["api_token"]; present {
		t.Fatal("api_token unexpectedly present in the zero-value marshal; this test no longer demonstrates the blind spot it exists to cover")
	}

	// The type-level walk catches it anyway, because it never looked at the
	// value.
	sub := &testing.T{}
	walkForCredentials(sub, reflect.TypeOf(hiddenUntilSet{}), "hiddenUntilSet", map[reflect.Type]bool{})
	if !sub.Failed() {
		t.Fatal("walkForCredentials did not flag api_token even though it never inspects field values; " +
			"an omitempty credential at its zero value would pass silently")
	}
}

// Case 21: registry.Bind writes an Entry and registry.Load reads it back;
// this checks the round trip actually preserves the entry Bind was given
// (not merely that no credential-shaped key appears, which
// TestRegistryRoundTripWritesNoTokenField already covers) -- a Load that
// silently dropped or rewrote fields would make the credential-absence
// check meaningless, since it would no longer be testing the same data.
func TestRegistryBindLoadRoundTripPreservesTheEntry(t *testing.T) {
	home := t.TempDir()
	source := t.TempDir()

	e := registry.Entry{}
	rv := reflect.ValueOf(&e).Elem()
	for i := 0; i < rv.NumField(); i++ {
		if f := rv.Field(i); f.Kind() == reflect.String && f.CanSet() {
			f.SetString("filled")
		}
	}
	e.Key = "OR"
	e.Source = source
	e.Workspace = home + "/ws"

	if err := registry.Bind(home, e); err != nil {
		t.Fatal(err)
	}

	f, err := registry.Load(home)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := f.Repos["OR"]
	if !ok {
		t.Fatal("Load did not return the entry Bind wrote under key \"OR\"")
	}

	b, err := json.Marshal(f)
	if err != nil {
		t.Fatal(err)
	}
	assertNoCredentialKeys(t, "repos.json (Bind/Load round trip)", b)

	if got.Source != source {
		t.Errorf("Entry.Source = %q after round trip, want %q", got.Source, source)
	}
	if got.Channel != "filled" {
		t.Errorf("Entry.Channel = %q after round trip, want %q", got.Channel, "filled")
	}
}

// Case 22: config.Defaults() round-trips through JSON with the values it
// ships, not only with every string field forced non-empty -- the shape a
// fresh orion.json actually has -- and still carries no credential-shaped
// key, with the round trip preserving what Defaults() set.
func TestConfigDefaultsRoundTripPreservesValuesAndNoCredential(t *testing.T) {
	want := config.Defaults()

	b, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	assertNoCredentialKeys(t, "orion.json (Defaults round trip)", b)

	var got config.Config
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(want, got) {
		t.Errorf("config.Defaults() did not round-trip unchanged through JSON:\n got:  %+v\n want: %+v", got, want)
	}

	again, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	assertNoCredentialKeys(t, "orion.json (Defaults round trip, re-marshaled)", again)
}

// Case 23: web.Ticket -- what every Tickets implementation must return -- is
// declared with exactly Key, Title and Stage (trackercredential_test.go
// already pins that no reachable field reads as a credential). So a new
// implementation that stashes a credential in an unexported field has
// nowhere to put it on the way out: the return type itself has no slot for
// it. This exercises that with an implementation that tries anyway, and
// confirms neither the returned value nor its marshaled form carries the
// credential the implementation is holding.
type cheatingTickets struct {
	// apiToken is exactly what OR-278 forbids: a credential kept next to
	// state that gets handed back out. It exists here only to prove it
	// cannot escape through Ticket's return value.
	apiToken string
}

func (c cheatingTickets) Ticket(key string) (web.Ticket, error) {
	return web.Ticket{Key: key, Title: "title for " + key, Stage: "In Progress"}, nil
}

func TestNewTicketsImplementationCannotLeakAPrivateCredentialField(t *testing.T) {
	var tk web.Tickets = cheatingTickets{apiToken: "totally-secret-token-value"}

	got, err := tk.Ticket("OR-278")
	if err != nil {
		t.Fatal(err)
	}

	// The return type has no field to carry it -- already enforced by
	// TestBoardModelHoldsNoCredential's walk over web.Ticket -- so this
	// checks the actual value handed back names none of the held secret.
	rv := reflect.ValueOf(got)
	for i := 0; i < rv.NumField(); i++ {
		if s, ok := rv.Field(i).Interface().(string); ok && s == "totally-secret-token-value" {
			t.Fatalf("Ticket.%s carries the implementation's private credential value", rv.Type().Field(i).Name)
		}
	}

	b, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	assertNoCredentialKeys(t, "web.Ticket (from a cheating implementation)", b)
}
