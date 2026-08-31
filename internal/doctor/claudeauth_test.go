package doctor

import (
	"errors"
	"strings"
	"testing"
)

// A present-but-logged-out CLI passes the binary check and fails every run.
// Doctor exists to catch exactly that, and a pass here is a lie that costs a
// whole batch of tickets to discover (OR-212).
func TestALoggedOutCLIIsAFailNamingReAuthentication(t *testing.T) {
	c := claudeAuthVerdict([]byte(`{"loggedIn":false}`), nil)
	if c.grade != fail {
		t.Fatalf("grade = %s, want FAIL", c.grade.label())
	}
	if !strings.Contains(strings.ToLower(c.detail+c.fix), "sign in") {
		t.Errorf("the fix does not say to sign in: %q / %q", c.detail, c.fix)
	}
}

// A signed-in CLI passes, and names the account -- OR-11's standard: say WHICH
// credential, not merely that there is one.
func TestASignedInCLIPassesAndNamesTheAccount(t *testing.T) {
	c := claudeAuthVerdict([]byte(
		`{"loggedIn":true,"authMethod":"claude.ai","email":"someone@example.com"}`), nil)
	if c.grade != ok {
		t.Fatalf("grade = %s, want OK (%s)", c.grade.label(), c.detail)
	}
	if !strings.Contains(c.detail, "someone@example.com") {
		t.Errorf("detail does not name the account: %q", c.detail)
	}
}

// A CLI too old to answer is a check that could not be made, not a logged-out
// one. Blocking here would stop a working machine over an unanswerable
// question -- the degrade-never-require rule.
func TestAnUnanswerableProbeWarnsRatherThanBlocking(t *testing.T) {
	for _, tc := range []struct {
		name string
		out  []byte
		err  error
	}{
		{"command failed", nil, errors.New("unknown command 'auth'")},
		{"not json", []byte("Logged in as someone"), nil},
		{"no field", []byte(`{"authMethod":"claude.ai"}`), nil},
	} {
		if g := claudeAuthVerdict(tc.out, tc.err).grade; g != warn {
			t.Errorf("%s: grade = %s, want WARN", tc.name, g.label())
		}
	}
}

// The auth check has to actually run as part of `orion doctor`, alongside
// the binary check -- a present-but-logged-out CLI is exactly the case that
// passing checkClaude alone would miss (OR-212).
func TestDoctorsFullCheckListIncludesTheAuthCheck(t *testing.T) {
	var buf strings.Builder
	Run(&buf, t.TempDir(), false)
	out := buf.String()
	if !strings.Contains(out, "claude auth") {
		t.Errorf("the check list does not include the claude auth check:\n%s", out)
	}
	if !strings.Contains(out, "claude CLI") {
		t.Errorf("the check list no longer includes the binary check either:\n%s", out)
	}
}
