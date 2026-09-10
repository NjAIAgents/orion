package orion

import (
	"strings"
	"testing"
)

// OR-278 case 13: a credential hiding behind a compound name -- an API
// token, an OAuth access token, an RSA private key, a JWT signing key -- is
// still caught, because credentialShaped works on the WORDS a name splits
// into, not on an exact field name.
func TestCredentialShapedCatchesCompositeCredentialNames(t *testing.T) {
	cases := []string{
		"api_token",
		"APIToken",
		"access_token",
		"AccessToken",
		"private_key",
		"PrivateKey",
		"signing_key",
		"SigningKey",
	}
	for _, name := range cases {
		if _, bad := credentialShaped(name); !bad {
			t.Errorf("credentialShaped(%q) = false, want true (composite credential name)", name)
		}
	}
}

// OR-278 case 14: "project_key" (the tracker's public project key, not a
// cryptographic one) must NOT be flagged -- credentialShaped's word-boundary
// + qualifier design exempts it generically, because "key" only reads as a
// credential when preceded by a qualifier like "api" or "private".
//
// "weekly_tokens" is deliberately NOT a case here. An earlier version of
// this test also asserted credentialShaped("weekly_tokens") == false, but
// TestExemptJSONMatchesTheFullLabelNotJustTheLeafKey (exemptionscope_test.go,
// added in the same change) pins the opposite: exempting "weekly_tokens" at
// the word level would exempt it under ANY label or path, defeating the
// per-(label, path) design that test exists to protect. The one known
// legitimate "weekly_tokens" -- config.Config.Budget.WeeklyTokens -- stays
// exempt through exemptFields/exemptJSON instead, scoped to its exact path.
func TestCredentialShapedExemptsKnownFalsePositives(t *testing.T) {
	cases := []string{"project_key", "ProjectKey"}
	for _, name := range cases {
		if word, bad := credentialShaped(name); bad {
			t.Errorf("credentialShaped(%q) = true (word %q), want false (known false positive)", name, word)
		}
	}
}

// OR-278 case 15: every entry in the exemption lists carries a non-blank,
// substantive reason. An exemption list is how a credential check stops
// checking, so a blank or placeholder reason is itself a finding.
func TestExemptionListEntriesAreJustified(t *testing.T) {
	assertJustified := func(t *testing.T, list map[string]string) {
		t.Helper()
		if len(list) == 0 {
			t.Fatal("exemption list is empty; this test has nothing to audit")
		}
		placeholders := map[string]bool{"": true, "todo": true, "n/a": true, "fixme": true, "tbd": true}
		for key, reason := range list {
			if placeholders[strings.ToLower(strings.TrimSpace(reason))] {
				t.Errorf("%q has a blank or placeholder exemption reason %q; an exemption needs a real justification", key, reason)
			}
		}
	}

	t.Run("exemptFields", func(t *testing.T) { assertJustified(t, exemptFields) })
	t.Run("exemptJSON", func(t *testing.T) { assertJustified(t, exemptJSON) })
}
