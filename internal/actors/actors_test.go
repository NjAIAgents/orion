package actors

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/orion-sdlc/orion/internal/config"
	"github.com/orion-sdlc/orion/internal/events"
)

func ptr(s string) *string { return &s }

// Name AND job title, on every line. The name alone is opaque to anyone who
// has not memorised the cast, which is the failure this replaces.
func TestAnActorRendersAsNameAndDesignation(t *testing.T) {
	t.Cleanup(Reset)
	if got := Display(events.ActorImplementer); !strings.Contains(got, Separator) {
		t.Fatalf("Display(implementer) = %q, want a name and a job title", got)
	}
	if got := Model(events.ActorImplementer); got != "opus" {
		t.Errorf("the developer's model is the one real cost decision; got %q", got)
	}
}

// orion and ci have no name on purpose, and a nameless actor must not render
// with a dangling separator.
func TestANamelessActorRendersWithoutASeparator(t *testing.T) {
	for _, id := range []string{events.ActorOrion, events.ActorCI, events.ActorHuman} {
		got := Display(id)
		if strings.Contains(got, Separator) || got == "" {
			t.Errorf("Display(%s) = %q, want the job title alone", id, got)
		}
	}
}

// An actor this build has never heard of -- from a newer release, or a log
// written by one -- must still be attributable.
func TestAnUnmappedActorRendersAsItsIdentifier(t *testing.T) {
	if got := Display("archaeologist"); got != "archaeologist" {
		t.Fatalf("Display(unknown) = %q, want the identifier rather than a blank column", got)
	}
}

// The four identifiers this change adds, two of which describe work that
// already happened with nobody's name on it.
func TestTheNewActorsExist(t *testing.T) {
	for _, id := range []string{
		events.ActorRouter, events.ActorDescriber,
		events.ActorFrontend, events.ActorDevOps,
	} {
		if a := Get(id); a.Name == "" || a.Designation == "" {
			t.Errorf("%s is not in the roster: %+v", id, a)
		}
	}
}

func TestConfigOverridesOneFieldAndKeepsTheRest(t *testing.T) {
	t.Cleanup(Reset)
	if err := Configure(map[string]config.Agent{
		events.ActorImplementer: {Name: ptr("Xu")},
	}); err != nil {
		t.Fatalf("Configure: %v", err)
	}
	a := Get(events.ActorImplementer)
	if a.Name != "Xu" {
		t.Errorf("name = %q, want the override", a.Name)
	}
	if a.Designation == "" || a.Model != "opus" {
		t.Errorf("overriding a name must not reset the rest: %+v", a)
	}
}

// Clearing a name is how a team turns personas off, and it must leave a
// working, plainer output rather than a blank column.
func TestAnEmptyNameFallsBackToTheDesignationAlone(t *testing.T) {
	t.Cleanup(Reset)
	if err := Configure(map[string]config.Agent{
		events.ActorImplementer: {Name: ptr("")},
	}); err != nil {
		t.Fatalf("Configure: %v", err)
	}
	got := Display(events.ActorImplementer)
	if got == "" || strings.Contains(got, Separator) {
		t.Fatalf("Display = %q, want the job title alone", got)
	}
}

// Distinguishability is the whole reason names exist here, so a duplicate is
// refused rather than warned about -- and it names BOTH, or the reader is
// left to diff the roster to find the pair.
func TestADuplicateNameIsRefusedNamingBothActors(t *testing.T) {
	t.Cleanup(Reset)
	dup := Get(events.ActorArchitect).Name
	err := Configure(map[string]config.Agent{
		events.ActorImplementer: {Name: ptr(dup)},
	})
	if err == nil {
		t.Fatal("two agents were allowed to share one name")
	}
	for _, id := range []string{events.ActorImplementer, events.ActorArchitect} {
		if !strings.Contains(err.Error(), id) {
			t.Errorf("the error must name %s: %v", id, err)
		}
	}
	if Get(events.ActorImplementer).Name == dup {
		t.Error("a refused roster must not be applied")
	}
}

func TestCIAndTheHumanAreNotConfigurable(t *testing.T) {
	t.Cleanup(Reset)
	for _, id := range []string{events.ActorCI, events.ActorHuman} {
		if err := Configure(map[string]config.Agent{id: {Name: ptr("Somebody")}}); err == nil {
			t.Errorf("%s was configurable; it is not a judgement-making role", id)
		}
	}
}

// A key for an actor a later release defines must not fail the whole run.
func TestAnUnknownAgentKeyIsIgnored(t *testing.T) {
	t.Cleanup(Reset)
	if err := Configure(map[string]config.Agent{"security": {Name: ptr("Somebody")}}); err != nil {
		t.Fatalf("an unknown agent key must be ignored, not refused: %v", err)
	}
}

// Orion posts to the tracker under its operator's account, so a ticket
// already carries comments that read as though the operator wrote them --
// and the architect defaults to the operator's own name. A comment an agent
// wrote has to say so, or the reader cannot tell which of the two acted.
func TestATrackerCommentNamesTheAgentThatWroteIt(t *testing.T) {
	t.Cleanup(Reset)
	got := Comment(events.ActorArchitect, "by issuer, per spec.md §4")
	if !strings.HasPrefix(got, Attribution(events.ActorArchitect)) {
		t.Fatalf("a comment must lead with who wrote it:\n%s", got)
	}
	if !strings.Contains(got, "Orion agent") {
		t.Errorf("an agent's comment must be distinguishable from the human's:\n%s", got)
	}
	if !strings.Contains(got, "by issuer, per spec.md §4") {
		t.Errorf("the comment body was lost:\n%s", got)
	}
	// Orion itself is not an agent with a name, and must not claim to be one.
	if orion := Comment(events.ActorOrion, "opened a pull request"); !strings.HasPrefix(orion, "Orion:") {
		t.Errorf("Orion's own comment reads oddly:\n%s", orion)
	}
}

// The constraint that makes configuration work at all.
//
// A name written into a prompt, a Slack template or a fixture survives a
// rename, so a team that renamed the developer gets an agent addressed by a
// name that appears nowhere in their config -- and nothing says why. Only
// the registry may know a name.
func TestNoDefaultNameAppearsOutsideTheRegistry(t *testing.T) {
	root := filepath.Join("..", "..")
	allowed := map[string]bool{
		filepath.Join(root, "internal", "actors", "actors.go"):      true,
		filepath.Join(root, "internal", "actors", "actors_test.go"): true,
	}
	var pats []*regexp.Regexp
	for _, n := range DefaultNames() {
		pats = append(pats, regexp.MustCompile(`\b`+regexp.QuoteMeta(n)+`\b`))
	}

	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(path, ".go") || allowed[path] {
			return err
		}
		b, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		for i, p := range pats {
			if p.Match(b) {
				t.Errorf("%s contains the default name %q. Refer to the role and let the "+
					"renderer supply the name, or a renamed agent is addressed by a name "+
					"the user never configured.", path, DefaultNames()[i])
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
}
