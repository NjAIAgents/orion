package supervisor

import (
	"strings"
	"testing"
)

// The reasoning is the deliverable as much as the name is. A prompt that asks
// only for a choice gets "Postgres", which nobody can evaluate or revisit.
func TestChoosePromptAsksForTheReasoningAndTheRejections(t *testing.T) {
	p := DBAPlanChoosePrompt("a payments API", "docs/intent/pay.md", "specs/pay.spec.md")
	for _, want := range []string{
		"docs/intent/pay.md", "specs/pay.spec.md",
		"what you rejected", "reasoning", DBARecommendation,
	} {
		if !strings.Contains(p, want) {
			t.Errorf("the choose prompt does not ask for %q:\n%s", want, p)
		}
	}
}

// The order is the design: the schema is a separate recommendation, asked for
// only after a person has confirmed the database. A choose run that drew a
// schema too would be proposing one on a premise nobody had agreed to.
func TestChoosePromptForbidsDesigningTheSchemaAndWritingFiles(t *testing.T) {
	p := DBAPlanChoosePrompt("a payments API", "docs/intent/pay.md", "specs/pay.spec.md")
	if !strings.Contains(p, "DO NOT DESIGN THE SCHEMA YET") {
		t.Errorf("the choose prompt does not hold the schema back:\n%s", p)
	}
	if !strings.Contains(p, "Write no file") {
		t.Errorf("the choose prompt does not stop the agent filing its own record:\n%s", p)
	}
}

// The schema run is a fresh session that never saw the first one, so the
// decision has to travel in the prompt -- in full, not paraphrased.
func TestSchemaPromptCarriesTheConfirmedDecision(t *testing.T) {
	p := DBAPlanSchemaPrompt("Postgres 16, because the ledger query joins four entities.",
		"docs/intent/pay.md", "specs/pay.spec.md")
	if !strings.Contains(p, "Postgres 16, because the ledger query joins four entities.") {
		t.Errorf("the schema prompt does not carry the confirmed decision:\n%s", p)
	}
	if !strings.Contains(p, "run no migration") || !strings.Contains(p, "Write no file") {
		t.Errorf("the schema prompt does not keep it a proposal:\n%s", p)
	}
	if !strings.Contains(p, DBARecommendation) {
		t.Errorf("the schema prompt does not say how to report:\n%s", p)
	}
}

// What follows the marker is copied verbatim into a document somebody is
// asked to approve, so the boundary is read literally and never inferred.
func TestDBARecommendsReadsFromTheMarkerOnly(t *testing.T) {
	got, ok := DBARecommends("I looked at the spec.\n" + DBARecommendation + "\nPostgres 16.\nBecause joins.")
	if !ok {
		t.Fatal("a marked recommendation was not recognised")
	}
	if got != "Postgres 16.\nBecause joins." {
		t.Errorf("the preamble leaked into the record: %q", got)
	}
}

func TestDBARecommendsRefusesProseAndAnEmptyMarker(t *testing.T) {
	if _, ok := DBARecommends("Postgres is probably fine, honestly."); ok {
		t.Error("prose with no marker was read as a recommendation")
	}
	if _, ok := DBARecommends("thinking\n" + DBARecommendation + "\n   \n"); ok {
		t.Error("a marker with nothing under it was read as a recommendation")
	}
}
