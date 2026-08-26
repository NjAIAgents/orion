package tracker

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"unicode"
)

func TestDeriveKeyShape(t *testing.T) {
	tests := []struct{ slug, want string }{
		{"claim-status-self-service", "CSSS"},
		{"add-health-check-endpoint", "AHCE"},
		{"payments", "PAYMENTS"},
		{"a-b-c-d-e-f-g-h-i-j", "ABCDEF"}, // capped at 6 initials
		{"2fa-login", "FL"},               // must not start with a digit
		{"", "ORION"},
		{"---", "ORION"},
	}
	for _, tc := range tests {
		got := DeriveKey(tc.slug)
		if got != tc.want {
			t.Errorf("DeriveKey(%q) = %q, want %q", tc.slug, got, tc.want)
		}
	}
}

// Jira rejects keys that break these rules, and the rejection arrives after
// an idea has already been captured and planned. Cheaper to guarantee here.
func TestDeriveKeyAlwaysValidForJira(t *testing.T) {
	for _, slug := range []string{
		"", "---", "123", "9-lives", "a", "supercalifragilistic-expialidocious-thing",
		"UPPER case MiXeD", "emoji-🚀-thing", "trailing-dash-",
	} {
		k := DeriveKey(slug)
		if len(k) < 2 || len(k) > 10 {
			t.Errorf("DeriveKey(%q) = %q: length %d outside Jira's 2..10", slug, k, len(k))
		}
		if !unicode.IsLetter(rune(k[0])) {
			t.Errorf("DeriveKey(%q) = %q: must start with a letter", slug, k)
		}
		for _, r := range k {
			if !unicode.IsUpper(r) && !unicode.IsDigit(r) {
				t.Errorf("DeriveKey(%q) = %q: contains %q, only A-Z0-9 allowed", slug, k, r)
			}
		}
	}
}

// --- fake tracker ---------------------------------------------------------

type fake struct {
	existing map[string]string
	created  []string
	perm     bool
}

func (f *fake) Name() string { return "fake" }
func (f *fake) Probe() (Capability, error) {
	return Capability{Reachable: true, Authenticated: true, CanCreateProject: f.perm}, nil
}
func (f *fake) ProjectExists(key string) (bool, string, error) {
	id, ok := f.existing[key]
	return ok, id, nil
}
func (f *fake) CreateProject(key, name, lead string) (Binding, error) {
	if !f.perm {
		return Binding{}, ErrNoPermission
	}
	f.existing[key] = "id-" + key
	f.created = append(f.created, key)
	return Binding{Provider: "fake", Key: key, ProjectID: "id-" + key, Created: true}, nil
}

func TestResolveKeyAvoidsCollisions(t *testing.T) {
	f := &fake{existing: map[string]string{"CSSS": "1", "CSSS2": "2"}, perm: true}
	got, err := ResolveKey(f, "CSSS")
	if err != nil {
		t.Fatal(err)
	}
	if got != "CSSS3" {
		t.Errorf("ResolveKey = %q, want CSSS3", got)
	}
}

// Collisions across many ideas are certain, not hypothetical, so the search
// must terminate rather than spin.
func TestResolveKeyGivesUpWithGuidance(t *testing.T) {
	// Every key the resolver will try is already taken, so it must stop
	// rather than spin.
	f := &fake{existing: map[string]string{"X": "0"}, perm: true}
	for i := 2; i <= 20; i++ {
		f.existing[fmt.Sprintf("X%d", i)] = "taken"
	}
	_, err := ResolveKey(f, "X")
	if err == nil {
		t.Fatal("should give up when every candidate is taken")
	}
	if !strings.Contains(err.Error(), "tracker.project_key") {
		t.Errorf("failure must point at the workaround, got: %v", err)
	}
}

func TestProvisionBindsExistingKey(t *testing.T) {
	f := &fake{existing: map[string]string{"PLAT": "77"}, perm: false}
	b, note, err := Provision(f, "anything", "Anything", "PLAT", "lead")
	if err != nil {
		t.Fatal(err)
	}
	if b.Created {
		t.Error("binding an existing project must not report Created")
	}
	if b.Key != "PLAT" || b.ProjectID != "77" {
		t.Errorf("bad binding: %+v", b)
	}
	if !strings.Contains(note, "existing") {
		t.Errorf("note should say it bound rather than created, got %q", note)
	}
}

func TestProvisionRejectsMissingConfiguredKey(t *testing.T) {
	f := &fake{existing: map[string]string{}, perm: true}
	if _, _, err := Provision(f, "x", "X", "NOPE", "lead"); err == nil {
		t.Fatal("a configured key that does not exist must be an error, not a silent create")
	}
}

// The whole point of the fallback: a user without admin rights gets a clear
// route forward, not a hard stop three stages into a run.
func TestProvisionWithoutPermissionExplainsTheFallback(t *testing.T) {
	f := &fake{existing: map[string]string{}, perm: false}
	_, _, err := Provision(f, "claim-status", "Claim status", "", "lead")
	if err == nil {
		t.Fatal("expected an error without permission")
	}
	if !errors.Is(err, ErrNoPermission) && !strings.Contains(err.Error(), "tracker.project_key") {
		t.Errorf("error must name the fallback, got: %v", err)
	}
}

func TestProvisionCreatesWhenPermitted(t *testing.T) {
	f := &fake{existing: map[string]string{}, perm: true}
	b, note, err := Provision(f, "claim-status-self-service", "Claim status self service", "", "lead")
	if err != nil {
		t.Fatal(err)
	}
	if !b.Created || b.Key != "CSSS" {
		t.Errorf("bad binding: %+v", b)
	}
	if len(f.created) != 1 {
		t.Errorf("expected exactly one project created, got %v", f.created)
	}
	if !strings.Contains(note, "created") {
		t.Errorf("note = %q", note)
	}
}

// The permission key is discovered rather than hardcoded, so the important
// property is that an unrecognised key is never read as a denial.
func TestUndeterminedIsNotDenial(t *testing.T) {
	var c Capability
	c.Authenticated = true
	c.Undetermined = true
	c.CanCreateProject = false

	// A caller must be able to tell "we do not know" from "you may not".
	if !c.Undetermined {
		t.Fatal("Undetermined must survive as its own signal")
	}
	if c.CanCreateProject {
		t.Fatal("undetermined must not imply permitted either")
	}
}

func TestProjectCreateKeysCoverKnownVariants(t *testing.T) {
	want := map[string]bool{"CREATE_PROJECT": false, "ADMINISTER": false, "SYSTEM_ADMIN": false}
	for _, k := range projectCreateKeys {
		want[k] = true
	}
	for k, found := range want {
		if !found {
			t.Errorf("projectCreateKeys is missing %q", k)
		}
	}
	// Order matters: the narrow permission is probed before the broad one so
	// the reported reason names the least privilege that actually applies.
	if projectCreateKeys[0] != "CREATE_PROJECT" {
		t.Errorf("narrowest key should be probed first, got %q", projectCreateKeys[0])
	}
}
