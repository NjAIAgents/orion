package dba

import (
	"strings"
	"testing"
)

// The gate is the whole economics of the stage: a ticket that touches no data
// must not pay for a database review. These are the paths that mean it does.
func TestPathsThatMeanTheDataModel(t *testing.T) {
	data := []string{
		"migrations/0007_add_orders_index.sql",
		"db/migrate/20260901_add_column.rb",
		"internal/store/schema.sql",
		"structure.sql",
		"prisma/schema.prisma",
		"app/models/order.rb",
		"src/entities/customer.ts",
		"alembic/versions/abc123_add_index.py",
		"queries/report.sql",
	}
	for _, p := range data {
		if !TouchesPath(p) {
			t.Errorf("TouchesPath(%q) = false; this change touches the data model and "+
				"would skip the review that exists for it", p)
		}
	}
}

// The other half, and the more important one: a false positive spends an opus
// run on a change with no schema in it, on every ticket that looks vaguely
// data-shaped. Equality on segments, never containment -- the same rule
// internal/work/route.go settled on.
func TestPathsThatDoNotMeanTheDataModel(t *testing.T) {
	notData := []string{
		"internal/work/migrations.go", // code ABOUT migrations, not a migration
		"docs/model-railway/index.md", // "model" inside a word, not a segment
		"cmd/orion/main.go",
		"README.md",
		"internal/schemagen/gen.go", // "schema" inside a segment, not equal to it
		"web/src/components/Table.tsx",
		"",
	}
	for _, p := range notData {
		if TouchesPath(p) {
			t.Errorf("TouchesPath(%q) = true; this change has no data model in it and "+
				"would buy a review that has nothing to review", p)
		}
	}
}

// The ticket's own markers are the only signal a ticket that has not run yet
// has, and they are matched by equality against the fields routing already
// reads -- never against free-text prose.
func TestTicketMarkers(t *testing.T) {
	if w, ok := TouchesTicket([]string{"Task", "backend", "database"}); !ok || w != "database" {
		t.Errorf("TouchesTicket = %q,%v; a `database` label is the marker a planner writes", w, ok)
	}
	if _, ok := TouchesTicket([]string{"Task", "we should index the docs page"}); ok {
		t.Error("a prose field matched; markers are matched by equality, or every ticket " +
			"mentioning a word becomes a database ticket")
	}
	if _, ok := TouchesTicket([]string{"Task", "ORION", "frontend"}); ok {
		t.Error("an ordinary ticket matched the database markers")
	}
}

// Windows-shaped paths and mixed case are still the same file. A gate that
// depended on the separator would run on one checkout and not another.
func TestPathMatchingIsCaseAndSeparatorInsensitive(t *testing.T) {
	for _, p := range []string{`Migrations\0007_add.sql`, "MIGRATIONS/0007_add.SQL"} {
		if !TouchesPath(p) {
			t.Errorf("TouchesPath(%q) = false", p)
		}
	}
}

// The announcement is the operator's one chance to say "that is not a schema
// change" before the money is spent, so it has to name what actually matched
// and where it came from.
func TestSignalsNameTheEvidence(t *testing.T) {
	sigs := Signals(
		[]string{"README.md", "migrations/0007_add.sql"},
		[]string{"Task", "database"})
	if len(sigs) != 2 {
		t.Fatalf("Signals = %+v; want the path and the label", sigs)
	}
	if sigs[0].From != "diff" || sigs[0].What != "migrations/0007_add.sql" {
		t.Errorf("first signal = %+v; the diff is ground truth and comes first", sigs[0])
	}
	if sigs[1].From != "ticket" || sigs[1].What != "database" {
		t.Errorf("second signal = %+v", sigs[1])
	}
	reason := Reason(sigs)
	if !strings.Contains(reason, "migrations/0007_add.sql") || !strings.Contains(reason, "diff") {
		t.Errorf("Reason = %q; a reader has to be able to check it", reason)
	}
}

// No signal means no stage. This is the assertion that keeps the stage free
// for the tickets it is not about.
func TestNoSignalsWhenNothingTouchesData(t *testing.T) {
	if sigs := Signals([]string{"cmd/orion/main.go", "README.md"}, []string{"Task", "ORION"}); len(sigs) != 0 {
		t.Errorf("Signals = %+v; a change with no data in it must cost nothing", sigs)
	}
	if r := Reason(nil); !strings.Contains(r, "nothing") {
		t.Errorf("Reason(nil) = %q; it has to say the stage was skipped, not stay silent", r)
	}
}

// A change that rewrote forty migrations has made its point by the fifth. The
// line exists to let a reader recognise the change, not to enumerate it.
func TestSignalsAreCapped(t *testing.T) {
	var many []string
	for i := 0; i < 40; i++ {
		many = append(many, "migrations/000"+string(rune('a'+i%26))+".sql")
	}
	if got := len(Signals(many, []string{"database"})); got != MaxSignals {
		t.Errorf("Signals returned %d; want the %d cap", got, MaxSignals)
	}
}
