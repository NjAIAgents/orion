package advise

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The scoping is the whole reason this is a role rather than a hint to the
// architect. A role that can read everything answers out of scope, which is
// what Artifacts' own comment says the architect's narrowness prevents -- so
// the database architect must not be handed spec.md, plan.md or intent.md
// either.
func TestDBAArtifactsAreTheSchemaAndNothingElse(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "spec.md")
	write(t, dir, "plan.md")
	write(t, dir, "intent.md")
	write(t, dir, "migrations/0007_add.sql")
	write(t, dir, "prisma/schema.prisma")
	write(t, dir, "docs/decisions/0001-x.md")

	got := Artifacts(dir, RoleDBA)
	joined := strings.Join(got, " ")
	for _, forbidden := range []string{"spec.md", "plan.md", "intent.md"} {
		if strings.Contains(joined, forbidden) {
			t.Errorf("the database architect was handed %s: %v.\n"+
				"A role that can read everything answers out of scope, which is the "+
				"laundering this package exists to prevent", forbidden, got)
		}
	}
	for _, want := range []string{"migrations", "prisma/schema.prisma", "docs/decisions"} {
		if !contains(got, want) {
			t.Errorf("%s is missing from %v; it is where this repository's committed "+
				"truth about its data model lives", want, got)
		}
	}
}

// Only what exists. Unlike spec.md, which Orion creates, the schema lives
// wherever the repository's framework puts it -- so the candidate list is
// filtered, not asserted.
func TestDBAArtifactsListOnlyWhatIsThere(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "db/schema.rb")

	got := Artifacts(dir, RoleDBA)
	if len(got) != 1 || got[0] != "db/schema.rb" {
		t.Errorf("Artifacts = %v; want only the file that exists", got)
	}
}

// A repository with no data model at all hands the advisor nothing, and
// promptFor then tells it to refuse -- which is the correct outcome, not a
// failure. See the package comment.
func TestDBAWithNoSchemaGetsNothingAndIsToldToRefuse(t *testing.T) {
	dir := t.TempDir()
	if got := Artifacts(dir, RoleDBA); len(got) != 0 {
		t.Fatalf("Artifacts = %v; want nothing", got)
	}
	p := promptFor(RoleDBA, "should orders be denormalised?", nil)
	if !strings.Contains(p, "Refuse") {
		t.Error("an advisor with no artifacts was not told to refuse; a decision with " +
			"nothing behind it is the invention this package exists to stop")
	}
}

// The router has to be able to REACH the new role, or it is unreachable from
// a blocked implementer and only two of its three invocation paths exist.
func TestRouteReachesTheDBA(t *testing.T) {
	for reply, want := range map[string]Role{
		"DATA":                   RoleDBA,
		"data\n":                 RoleDBA,
		"PRODUCT":                RolePM,
		"TECHNICAL":              RoleArchitect,
		"something unrecognised": RoleArchitect,
	} {
		got := Route(func(dir, model, prompt string) (string, error) { return reply, nil }, "", "q")
		if got != want {
			t.Errorf("a router reply of %q routed to %q, want %q", reply, got, want)
		}
	}
}

// The classifier cannot pick a class it was never told about.
func TestRoutePromptOffersTheDataClass(t *testing.T) {
	p := routePrompt("why is this query slow?")
	if !strings.Contains(p, "DATA") {
		t.Error("routePrompt does not offer DATA, so the router can never return it " +
			"and the advisor role is unreachable")
	}
}

// A transport failure still falls back to the architect: its refusal is cheap
// and informative, and adding a third class must not change that.
func TestRouteStillFallsBackToTheArchitect(t *testing.T) {
	got := Route(func(dir, model, prompt string) (string, error) {
		return "", os.ErrDeadlineExceeded
	}, "", "q")
	if got != RoleArchitect {
		t.Errorf("an unreachable router routed to %q, want the architect", got)
	}
}

func write(t *testing.T, dir, rel string) {
	t.Helper()
	path := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func contains(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}
