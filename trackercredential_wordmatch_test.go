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
// cryptographic one) and "weekly_tokens" (a spend limit in model tokens, not
// an auth token) must NOT be flagged -- these are the two false positives
// the word-boundary + qualifier design exists to exempt.
func TestCredentialShapedExemptsKnownFalsePositives(t *testing.T) {
	cases := []string{"project_key", "ProjectKey", "weekly_tokens", "WeeklyTokens"}
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
