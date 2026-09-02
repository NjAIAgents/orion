package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/orion-sdlc/orion/internal/actors"
	"github.com/orion-sdlc/orion/internal/config"
	"github.com/orion-sdlc/orion/internal/events"
	"github.com/orion-sdlc/orion/internal/supervisor"
)

// The explicitly-asked review is its own actor on its own model, attributed to
// the ticket when there is one. That attribution is the reason OR-135 gives
// for this being an actor at all: its runs need their own row in the cost
// report, and a review charged to nothing is spend nobody can account for.
func TestDBAAskRunsAsItsOwnActorAndModel(t *testing.T) {
	o := dbaAskOptions("FCIA-6", "this query got slow", config.Config{})
	if o.Actor != events.ActorDBA {
		t.Errorf("actor = %q, want %q", o.Actor, events.ActorDBA)
	}
	if o.Key != "FCIA-6" {
		t.Errorf("key = %q; without it the spend lands on no ticket's cost report", o.Key)
	}
	if o.Model != actors.Model(events.ActorDBA) {
		t.Errorf("model = %q, want the roster's %q -- not the CLI's own default (OR-133)",
			o.Model, actors.Model(events.ActorDBA))
	}
	if o.Stage != "dba" {
		t.Errorf("stage = %q", o.Stage)
	}
	if !strings.Contains(o.Prompt, "this query got slow") {
		t.Error("the question did not reach the prompt")
	}
}

// A performance complaint usually precedes anybody writing a ticket, so the
// command has to work with no key. An unattributed answer is still an answer.
func TestDBAAskWorksWithNoTicket(t *testing.T) {
	o := dbaAskOptions("", "why is the orders report slow", config.Config{})
	if o.Key != "" {
		t.Errorf("key = %q, want empty", o.Key)
	}
	if !strings.Contains(o.Prompt, "orders report") {
		t.Error("the question did not reach the prompt")
	}
}

// THE PRODUCTION GUARD, on the path most able to reach a database: a person
// typing `orion dba` is asking for a query plan. A DSN that names itself
// production is REFUSED, not warned about and used -- a warning printed above
// a session that then connects is read after the EXPLAIN.
func TestDBAAskRefusesAProductionDSN(t *testing.T) {
	warn := tmpFile(t)
	cfg := config.Config{DBA: config.DBA{
		NonProdDSN: "postgres://user:pw@orders.prod.internal:5432/app"}}

	target := dbaTarget(cfg, warn)

	if target.DSN != "" {
		t.Fatalf("target DSN = %q; a production-looking DSN must be refused, not used",
			target.DSN)
	}
	if said := readAll(t, warn); !strings.Contains(said, "refused") {
		t.Errorf("nothing was said about the refusal: %q", said)
	}
	// And the prompt that results is the static one, which tells the agent not
	// to go looking for a database either.
	p := supervisor.DBAAskPrompt("q", target)
	if !strings.Contains(p, "No non-production database is configured") {
		t.Error("after a refusal the review is not static")
	}
}

// An ordinary DSN is passed through, or the setting does nothing.
func TestDBAAskUsesANonProductionDSN(t *testing.T) {
	const dsn = "postgres://localhost:5432/scratch"
	warn := tmpFile(t)
	target := dbaTarget(config.Config{DBA: config.DBA{NonProdDSN: dsn}}, warn)
	if target.DSN != dsn {
		t.Errorf("target DSN = %q, want %q", target.DSN, dsn)
	}
	if said := readAll(t, warn); said != "" {
		t.Errorf("an ordinary DSN produced a warning: %q", said)
	}
}

// `orion dba OR-135` is what somebody reaches for, so a leading issue key is
// one -- and a question that merely contains a hyphen is not.
func TestIssueKeysAreToldFromQuestions(t *testing.T) {
	for _, s := range []string{"OR-135", "FCIA-6", "or-135"} {
		if !looksLikeIssueKey(s) {
			t.Errorf("looksLikeIssueKey(%q) = false", s)
		}
	}
	for _, s := range []string{
		"this query got slow",
		"the orders-report is slow",
		"why is order-lookup slow",
		"-135", "OR-", "OR-13a", "",
	} {
		if looksLikeIssueKey(s) {
			t.Errorf("looksLikeIssueKey(%q) = true; it is a question, and swallowing it as "+
				"a key leaves the review with nothing to look at", s)
		}
	}
}

func tmpFile(t *testing.T) *os.File {
	t.Helper()
	f, err := os.Create(filepath.Join(t.TempDir(), "warn"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.Close() })
	return f
}

func readAll(t *testing.T, f *os.File) string {
	t.Helper()
	b, err := os.ReadFile(f.Name())
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
