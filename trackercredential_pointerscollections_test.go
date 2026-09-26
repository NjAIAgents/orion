package orion

// OR-278, cases 16-19. See trackercredential_test.go for the shared word
// list, the walker, and the reasoning this file assumes rather than repeats.
//
// walkForCredentials strips reflect.Ptr, reflect.Slice, reflect.Array and
// reflect.Map down to their element type before deciding whether it is a
// struct worth walking (trackercredential_test.go's dereference loop). These
// cases pin that stripping actually catches a credential hiding behind each
// of those indirections, rather than merely being present in the code -- a
// poller is exactly as likely to park a token behind a *Client, a []Peer, or
// a map[string]Cache as directly on a struct field.

import (
	"encoding/json"
	"reflect"
	"testing"
)

// Case 16: a credential-shaped field inside a struct reached only through a
// POINTER is still caught. The walker's dereference loop has to run before
// it decides typ.Kind() != reflect.Struct, or a *Sub field is skipped
// entirely -- indistinguishable from a field of an unsupported kind.
type pointedToSecret struct {
	APIKey string `json:"api_key"`
}

type hostWithPointerField struct {
	Existing string `json:"existing"`
	Sub      *pointedToSecret
}

func TestWalkForCredentialsCatchesFieldBehindAPointer(t *testing.T) {
	sub := &testing.T{}
	walkForCredentials(sub, reflect.TypeOf(hostWithPointerField{}), "hostWithPointerField", map[reflect.Type]bool{})
	if !sub.Failed() {
		t.Fatal("walkForCredentials did not flag a credential field reached through a pointer field; " +
			"a *Client or *Config parked on a struct would pass silently")
	}
}

// Case 17: a credential-shaped field inside the ELEMENT type of a slice, and
// separately an array, is still caught. A poller is at least as likely to
// keep a slice of peers or backends -- each carrying its own token -- as a
// single struct.
type secretHoldingElement struct {
	Token string `json:"token"`
}

type hostWithSliceField struct {
	Existing string `json:"existing"`
	Peers    []secretHoldingElement
}

type hostWithArrayField struct {
	Existing string `json:"existing"`
	Peers    [2]secretHoldingElement
}

func TestWalkForCredentialsCatchesFieldInsideASliceElement(t *testing.T) {
	sub := &testing.T{}
	walkForCredentials(sub, reflect.TypeOf(hostWithSliceField{}), "hostWithSliceField", map[reflect.Type]bool{})
	if !sub.Failed() {
		t.Fatal("walkForCredentials did not flag a credential field on a slice element type; " +
			"a []Peer carrying its own token would pass silently")
	}
}

func TestWalkForCredentialsCatchesFieldInsideAnArrayElement(t *testing.T) {
	sub := &testing.T{}
	walkForCredentials(sub, reflect.TypeOf(hostWithArrayField{}), "hostWithArrayField", map[reflect.Type]bool{})
	if !sub.Failed() {
		t.Fatal("walkForCredentials did not flag a credential field on an array element type; " +
			"a [N]Peer carrying its own token would pass silently")
	}
}

// Case 18: maps are rejected on two independent axes. The walker follows a
// map's VALUE type the same as a slice's element type, so a credential
// nested inside what a map holds is caught at the type level; a credential
// living as a map KEY only exists once real data is marshaled, so that half
// is caught by assertNoCredentialKeys walking the decoded JSON, the same
// helper trackercredential_serialization_test.go's round-trip cases use.
type hostWithMapOfSecrets struct {
	Existing string `json:"existing"`
	Backends map[string]secretHoldingElement
}

func TestWalkForCredentialsCatchesFieldInsideAMapValue(t *testing.T) {
	sub := &testing.T{}
	walkForCredentials(sub, reflect.TypeOf(hostWithMapOfSecrets{}), "hostWithMapOfSecrets", map[reflect.Type]bool{})
	if !sub.Failed() {
		t.Fatal("walkForCredentials did not flag a credential field on a map's value type; " +
			"a map[string]Backend carrying its own token would pass silently")
	}
}

func TestAssertNoCredentialKeysCatchesACredentialShapedMapKey(t *testing.T) {
	// A poller keying a cache by which credential it used ("api_token":
	// "...") has no Go struct field to name -- the field IS the map, and the
	// bad key only exists in the marshaled bytes. map[string]any preserves
	// this shape through json.Marshal without a Go type in the loop at all.
	doc := map[string]any{
		"existing":  "fine",
		"api_token": "shhh",
	}
	b, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}

	sub := &testing.T{}
	assertNoCredentialKeys(sub, "hostWithMapOfSecrets", b)
	if !sub.Failed() {
		t.Fatal("assertNoCredentialKeys did not flag a credential-shaped map key; " +
			"a cache keyed by which token was used would pass silently")
	}
}

// Case 19: the walker checks BOTH the Go field name and its json tag
// (trackercredential_test.go builds `names := []string{f.Name}` then appends
// the tag). A field whose Go name reads as ordinary English but whose wire
// name is the giveaway -- or the reverse -- must be caught either way, since
// a reviewer skimming Go source sees the field name while everything that
// reads ORION_HOME back off disk sees only the tag.
type wireNameIsTheGiveaway struct {
	// "Secret" here is plain English (a shared config value), but the wire
	// name it serializes under is exactly what a poller would call a token.
	Secret string `json:"bearer_token"`
}

type goNameIsTheGiveaway struct {
	// The Go name is the giveaway; the wire name is deliberately
	// unrelated-looking, the way a hand-rolled DTO sometimes remaps fields.
	APIKey string `json:"cfg_value_3"`
}

func TestWalkForCredentialsCatchesAJSONTagThatDiffersFromTheGoFieldName(t *testing.T) {
	sub := &testing.T{}
	walkForCredentials(sub, reflect.TypeOf(wireNameIsTheGiveaway{}), "wireNameIsTheGiveaway", map[reflect.Type]bool{})
	if !sub.Failed() {
		t.Fatal("walkForCredentials did not flag a json tag that reads as a credential " +
			"while the Go field name does not; only checking f.Name would miss this")
	}
}

func TestWalkForCredentialsCatchesAGoFieldNameEvenWhenTheJSONTagDoesNot(t *testing.T) {
	sub := &testing.T{}
	walkForCredentials(sub, reflect.TypeOf(goNameIsTheGiveaway{}), "goNameIsTheGiveaway", map[reflect.Type]bool{})
	if !sub.Failed() {
		t.Fatal("walkForCredentials did not flag a credential-shaped Go field name " +
			"whose json tag was deliberately disguised; only checking the tag would miss this")
	}
}
