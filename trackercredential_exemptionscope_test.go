package orion

// OR-278 QA addendum to case 14/15. exemptFields and exemptJSON key by the
// FULL path ("config.Config.Budget.WeeklyTokens"), not by the bare field
// name ("WeeklyTokens"). That specificity is the only thing standing between
// "documented exemption for one known field" and "blanket exemption for
// anything sharing its name" -- an exemption list that matched on the leaf
// name would silently clear a real token field the day someone reused
// "WeeklyTokens" (or "weekly_tokens") elsewhere in the tree. Nothing in the
// existing suite pins that the match is exact rather than suffix-based, so a
// regression there would pass every case already written.

import (
	"encoding/json"
	"reflect"
	"testing"
)

// A field with the same leaf name as the documented exemption
// (config.Config.Budget.WeeklyTokens) but a different full path. If the
// exemption were keyed on the leaf name instead of the full path, this would
// wrongly pass.
type otherBudget struct {
	WeeklyTokens string // deliberately NOT the exempted type or path
}

type hostWithLookalikeExemptField struct {
	Existing string
	Budget2  otherBudget
}

func TestExemptFieldsMatchesTheFullPathNotJustTheLeafName(t *testing.T) {
	if _, exempt := exemptFields["hostWithLookalikeExemptField.Budget2.WeeklyTokens"]; exempt {
		t.Fatal("test setup error: this path must not already be in exemptFields")
	}

	sub := &testing.T{}
	walkForCredentials(sub, reflect.TypeOf(hostWithLookalikeExemptField{}), "hostWithLookalikeExemptField", map[reflect.Type]bool{})
	if !sub.Failed() {
		t.Fatal("walkForCredentials treated a same-named field on an unrelated type as exempt; " +
			"the exemption for config.Config.Budget.WeeklyTokens must not bleed into a lookalike path")
	}
}

// The JSON-key exemption path is keyed the same way: "<what>.<dotted path>",
// so a same-named key under a DIFFERENT "what" label must still be flagged.
func TestExemptJSONMatchesTheFullLabelNotJustTheLeafKey(t *testing.T) {
	label := "a different file entirely (not orion.json)"
	if _, exempt := exemptJSON[label+".budget.weekly_tokens"]; exempt {
		t.Fatal("test setup error: this label must not already be in exemptJSON")
	}

	doc := map[string]any{
		"existing": "fine",
		"budget":   map[string]any{"weekly_tokens": 5},
	}
	b, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}

	sub := &testing.T{}
	assertNoCredentialKeys(sub, label, b)
	if !sub.Failed() {
		t.Fatal("assertNoCredentialKeys treated budget.weekly_tokens as exempt under a label that was never " +
			"granted that exemption; the exemption is per (label, path), not per bare key")
	}
}
