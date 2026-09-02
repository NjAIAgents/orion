package config

import "testing"

// Absent means ON, the same decision QA's Enabled pointer records: a
// repository that never heard of the stage should get it, and only an explicit
// false is a project saying it does not want the spend.
func TestDBAAbsentMeansOn(t *testing.T) {
	if !(DBA{}).On() {
		t.Error("an absent dba.enabled read as off; verification a project never asked " +
			"to switch off must not be silently missing")
	}
	off := false
	if (DBA{Enabled: &off}).On() {
		t.Error("an explicit dba.enabled=false read as on")
	}
	on := true
	if !(DBA{Enabled: &on}).On() {
		t.Error("an explicit dba.enabled=true read as off")
	}
}

// Zero restores the shipped default rather than meaning no rounds: no rounds
// at all would escalate the first finding with nobody having tried to fix it.
func TestDBARoundsDefaults(t *testing.T) {
	if got := (DBA{}).Rounds(); got != FixRounds {
		t.Errorf("Rounds() = %d with nothing set, want the shipped %d", got, FixRounds)
	}
	if got := (DBA{MaxRounds: 7}).Rounds(); got != 7 {
		t.Errorf("Rounds() = %d, want the configured 7", got)
	}
}

// THE SAFETY PROPERTY. Empty is a static review, never "go and find one": the
// hazard this setting exists for is an agent connecting to a database nobody
// chose to hand it.
func TestDBAEmptyDSNIsStaticReview(t *testing.T) {
	if (DBA{}).Live() {
		t.Error("an empty non_prod_dsn read as a live target; empty must mean static " +
			"review, because the alternative is inferring a database")
	}
	if !(DBA{NonProdDSN: "postgres://localhost:5432/scratch"}).Live() {
		t.Error("an explicit DSN read as no target")
	}
	if (DBA{NonProdDSN: "   "}).Live() {
		t.Error("whitespace read as a target")
	}
}

// The guard that catches the failure that actually happens: a value copied
// from somewhere else with the environment left in it.
func TestDBARefusesAProductionLookingDSN(t *testing.T) {
	for _, dsn := range []string{
		"postgres://user:pw@db.prod.internal:5432/app",
		"postgres://user:pw@orders-production.example.com/app",
		"mysql://live-db.internal/app",
		"postgres://user:pw@host/prod",
	} {
		if _, isProd := (DBA{NonProdDSN: dsn}).ProductionDSN(); !isProd {
			t.Errorf("ProductionDSN(%q) = false; this names itself production and an "+
				"EXPLAIN against it is the thing this setting exists to prevent", dsn)
		}
	}
}

// And it must not refuse an ordinary one, or operators route around it -- at
// which point the guard has made things worse. Word by word over the DSN's
// punctuation, not by substring.
func TestDBAAllowsANonProductionDSN(t *testing.T) {
	for _, dsn := range []string{
		"postgres://localhost:5432/scratch",
		"postgres://user:pw@reproducible-db.test/app", // contains "prod", is not production
		"postgres://user:pw@staging.internal/app",
		"postgres://user:pw@db-ci.internal/app_test",
		"",
	} {
		if word, isProd := (DBA{NonProdDSN: dsn}).ProductionDSN(); isProd {
			t.Errorf("ProductionDSN(%q) refused it on %q; a guard that refuses ordinary "+
				"values is one people work around", dsn, word)
		}
	}
}
