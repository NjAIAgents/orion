package main

import (
	"bytes"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/orion-sdlc/orion/internal/actors"
	"github.com/orion-sdlc/orion/internal/config"
	"github.com/orion-sdlc/orion/internal/events"
)

func strp(s string) *string { return &s }

// A wizard pass where the operator touched nothing at all must not add
// "id": {} noise to agents.json -- that would be indistinguishable from a
// real (empty) override to a human reading the file later.
func TestAgentIsZeroOnAnUntouchedAgent(t *testing.T) {
	if !agentIsZero(config.Agent{}) {
		t.Error("a zero-value Agent must read as zero")
	}
	if agentIsZero(config.Agent{Model: "opus"}) {
		t.Error("a set Model must not read as zero")
	}
	if agentIsZero(config.Agent{Name: strp("")}) {
		t.Error("an explicitly cleared name is still a real override, not zero")
	}
}

// The wizard's save path (SaveAgents then LoadAgents) must round-trip an
// entry with only some fields set without inventing the rest -- config.Agent
// now relies on json "omitempty" doing this correctly rather than the
// hand-rolled marshalling OR-131 shipped with (OR-132).
func TestAgentRoundTripsOnlyTheFieldsThatWereSet(t *testing.T) {
	home := t.TempDir()
	want := map[string]config.Agent{
		"implementer": {Effort: "high"},
		"qa":          {Name: strp(""), Model: "haiku"},
	}
	if err := config.SaveAgents(home, want); err != nil {
		t.Fatal(err)
	}
	got, err := config.LoadAgents(home)
	if err != nil {
		t.Fatal(err)
	}
	if got["implementer"].Effort != "high" || got["implementer"].Model != "" {
		t.Errorf("implementer = %+v", got["implementer"])
	}
	if got["qa"].Name == nil || *got["qa"].Name != "" {
		t.Errorf("qa.Name = %v, want an explicit empty string, not absent", got["qa"].Name)
	}
	if got["qa"].Model != "haiku" {
		t.Errorf("qa.Model = %q", got["qa"].Model)
	}
}

// listed renders the roster for a home whose agents.json holds over, so no
// listing test can pass by rendering a roster nobody configured.
func listed(t *testing.T, over map[string]config.Agent) string {
	t.Helper()
	home := t.TempDir()
	if over != nil {
		if err := config.SaveAgents(home, over); err != nil {
			t.Fatal(err)
		}
	}
	var buf bytes.Buffer
	if err := listAgents(home, &buf); err != nil {
		t.Fatal(err)
	}
	return buf.String()
}

// rosterLine returns the listing's row for one actor.
func rosterLine(t *testing.T, out, id string) string {
	t.Helper()
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), id+" ") {
			return line
		}
	}
	t.Fatalf("no row for %q in:\n%s", id, out)
	return ""
}

// shipped is one actor's default, read through Roster rather than Get so
// the expectation does not depend on whatever another test last Configured.
func shipped(t *testing.T, id string) actors.Actor {
	t.Helper()
	for _, e := range actors.Roster(nil) {
		if e.ID == id {
			return e.Actor
		}
	}
	t.Fatalf("%q is not in the shipped roster", id)
	return actors.Actor{}
}

// The override file is not the answer, which is the whole reason this
// listing exists: agents.json holds only OVERRIDES, so most of the roster
// is absent from it and its effective model lives in Go source. A listing
// that showed only what the file mentions would reproduce the problem it
// was built to fix.
func TestTheListingShowsEveryActorIncludingOnesAbsentFromTheOverrideFile(t *testing.T) {
	t.Setenv("CLICOLOR_FORCE", "0")
	got := listed(t, map[string]config.Agent{
		events.ActorImplementer: {Model: "opus", Effort: "high"},
	})

	for _, id := range actors.ConfigurableIDs() {
		if !strings.Contains(got, id) {
			t.Errorf("%s is missing from the roster listing:\n%s", id, got)
		}
	}
	// The explorer is the case that made this a ticket: it arrived after
	// the override file was written, so the file cannot say what it runs on.
	explore := shipped(t, events.ActorExplore)
	if explore.Model == "" || explore.Name == "" {
		t.Fatal("the explore actor ships without a name or model; this test is checking the wrong thing")
	}
	line := rosterLine(t, got, events.ActorExplore)
	if !strings.Contains(line, explore.Model) || !strings.Contains(line, explore.Name) {
		t.Errorf("explore row %q carries neither its shipped name nor its shipped model, "+
			"which are exactly the values the override file cannot supply", line)
	}
}

// A shipped default and a value somebody chose look identical in a table,
// and the difference is exactly what a reader is checking -- so the listing
// says, per field, which is which.
func TestAnOverriddenValueIsDistinguishableFromADefault(t *testing.T) {
	t.Setenv("CLICOLOR_FORCE", "0")
	got := listed(t, map[string]config.Agent{
		events.ActorImplementer: {Effort: "high"},
	})

	impl := rosterLine(t, got, events.ActorImplementer)
	if !strings.Contains(impl, "effort") {
		t.Errorf("implementer row %q does not name effort as the overridden field", impl)
	}
	// The model on that same row is a shipped default and must NOT be
	// claimed as overridden. Per-field provenance is the point: a row that
	// said "overridden" wholesale would misreport three columns out of four.
	if strings.Contains(impl, "model") {
		t.Errorf("implementer row %q claims model is overridden; only effort is", impl)
	}
	if !strings.Contains(impl, shipped(t, events.ActorImplementer).Model) {
		t.Errorf("implementer row %q lost its shipped model when one other field was overridden", impl)
	}

	qa := rosterLine(t, got, events.ActorQA)
	if strings.Contains(qa, "effort") || strings.Contains(qa, "model") {
		t.Errorf("qa row %q claims an override, but nothing was set for it", qa)
	}
}

// The listing must work with stdout redirected, because that is how anyone
// captures it to share -- and it must never be able to start a prompt.
// runConfig documents a real bug where --help fell through to a wizard's
// default case and blocked on stdin with Ctrl-C the only way out, so what
// is under test is the ORDERING inside runConfigAgents rather than
// listAgents in isolation: --list is answered before the terminal check.
//
// A regression fails this loudly rather than subtly. With stdin not a
// terminal the wizard exits 1 and takes the test binary with it; with stdin
// a terminal it blocks on a prompt and the test times out.
func TestTheListingNeverReachesThePromptEvenWithStdoutRedirected(t *testing.T) {
	t.Setenv("ORION_HOME", t.TempDir())
	t.Setenv("CLICOLOR_FORCE", "0")

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	saved := os.Stdout
	os.Stdout = w
	done := make(chan string, 1)
	go func() {
		var b bytes.Buffer
		_, _ = io.Copy(&b, r)
		done <- b.String()
	}()

	runConfigAgents([]string{"--list"})

	os.Stdout = saved
	_ = w.Close()
	got := <-done
	_ = r.Close()

	if !strings.Contains(got, "roster") {
		t.Fatalf("--list wrote no roster to a redirected stdout:\n%s", got)
	}
	if !strings.Contains(got, events.ActorImplementer) {
		t.Errorf("the redirected listing is missing the implementer row:\n%s", got)
	}
}

// Piped output carries no escape codes -- enabled() reports false the
// moment the destination is not a terminal -- and has to say everything
// anyway. Colour is an accelerator here, never the carrier.
func TestPipedListingHasNoEscapeCodesAndIsStillUnambiguous(t *testing.T) {
	t.Setenv("CLICOLOR_FORCE", "0")
	got := listed(t, map[string]config.Agent{
		events.ActorDevOps: {Effort: "high"},
	})

	if strings.Contains(got, "\x1b") {
		t.Fatalf("piped listing contains escape codes:\n%q", got)
	}
	for _, want := range []string{"id", "name", "designation", "model", "effort", "overridden"} {
		if !strings.Contains(got, want) {
			t.Errorf("piped listing has no %q column:\n%s", want, got)
		}
	}
	// An absent value is a real answer for three different reasons, so the
	// marker standing in for it has to be explained on the page.
	if !strings.Contains(got, "not set") {
		t.Errorf("nothing in the listing says what an unset field means:\n%s", got)
	}
	devops := rosterLine(t, got, events.ActorDevOps)
	if !strings.Contains(devops, "high") || !strings.Contains(devops, "effort") {
		t.Errorf("devops row %q does not read as \"effort high, and that is the override\" "+
			"once the colour is gone", devops)
	}
}

// NO_COLOR is the cross-tool convention this listing has to honour like
// every other ui surface -- and, per enabled()'s own doc comment, it wins
// even over an explicit CLICOLOR_FORCE. A regression that swapped ui.Dim /
// ui.Identity for a raw escape code in listAgents would slip past every
// other test in this file, because they all disable colour with
// CLICOLOR_FORCE=0 rather than NO_COLOR.
func TestNoColorSuppressesColourInTheRosterEvenOverAnExplicitForce(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	t.Setenv("CLICOLOR_FORCE", "1")
	got := listed(t, nil)

	if strings.Contains(got, "\x1b[") {
		t.Fatalf("NO_COLOR did not suppress colour in the roster listing, even though "+
			"CLICOLOR_FORCE=1 was also set:\n%q", got)
	}
}

// Only the id and name columns are painted, from the non-semantic palette.
// A row where designation/model/effort/overridden pick up ANY escape code --
// not just a semantic one -- has drifted from "two identity columns,
// nothing else" even if it never collides with green/red/yellow.
func TestOnlyTheIdentityColumnsCarryColour(t *testing.T) {
	t.Setenv("CLICOLOR_FORCE", "1")
	got := listed(t, nil)
	if !strings.Contains(got, "\x1b[") {
		t.Fatal("CLICOLOR_FORCE produced no colour at all, so the rest of this test proves nothing")
	}

	// rosterLine assumes an uncoloured line start, which does not hold once
	// CLICOLOR_FORCE paints the id cell -- so find the row by the pair only
	// this actor has: the id must lead the first painted cell and the name
	// must lead the second.
	name := shipped(t, events.ActorImplementer).Name
	var row string
	for _, line := range strings.Split(got, "\n") {
		if strings.Contains(line, "m"+events.ActorImplementer+"\x1b") && strings.Contains(line, name) {
			row = line
			break
		}
	}
	if row == "" {
		t.Fatalf("no row for %q in:\n%s", events.ActorImplementer, got)
	}
	parts := strings.SplitN(row, "\x1b[0m", 3)
	if len(parts) < 3 {
		t.Fatalf("implementer row has fewer than two painted cells (id, name): %q", row)
	}
	rest := parts[2]
	if strings.Contains(rest, "\x1b[") {
		t.Errorf("implementer row paints something past the id and name columns:\n%q", row)
	}
}

// Green, red and yellow mean outcome everywhere else in this output. An
// agent name in red reads as broken and a model in red reads as failing,
// whatever the column header claims -- so identities are painted from the
// non-semantic set or not at all. This is also the guard against the
// tempting mistake of colouring opus red to mean expensive: that collides
// with failure and makes a correct configuration look like a fault.
func TestNoIdentityIsRenderedInAnOutcomeColour(t *testing.T) {
	t.Setenv("CLICOLOR_FORCE", "1")
	got := listed(t, nil)

	if !strings.Contains(got, "\x1b") {
		t.Fatal("CLICOLOR_FORCE produced no colour at all, so the rest of this test proves nothing")
	}
	for code, meaning := range map[string]string{
		"\x1b[32m": "success",
		"\x1b[31m": "failure",
		"\x1b[33m": "warning",
	} {
		if strings.Contains(got, code) {
			t.Errorf("the roster paints something in the %s colour %q; no identity and no "+
				"model may borrow an outcome colour:\n%q", meaning, code, got)
		}
	}
}
