package tracker

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The claim criterion is BOTH signals, and the version half is in the JQL.
//
// OR-221: the queue filtered on the label and the status alone, so a labelled
// ticket with no release was picked up and worked. Its changelog fragment
// then had no version to collate against and the release it rode in could not
// account for it -- which is how OR-208 came to be filed retroactively.
func TestAnEnforcedProjectRequiresAnOpenMilestoneInTheJQL(t *testing.T) {
	s := Schedules{"OR": {"v0.8.6", "v0.9.0"}}

	got := s.Scope([]string{"OR"})

	if !strings.Contains(got, `fixVersion IN ("v0.8.6", "v0.9.0")`) {
		t.Fatalf("the milestone requirement is not in the query: %s", got)
	}
	if !strings.Contains(got, `project = "OR"`) {
		t.Errorf("lost the project scope: %s", got)
	}
}

// A project that does not use versions must be queried exactly as before.
//
// Orion adopts arbitrary repositories and FCIA is registered alongside OR.
// Enforcing a convention a project never opted into would halt it completely.
func TestAProjectWithNoVersionsIsUnaffected(t *testing.T) {
	s := Schedules{"FCIA": nil}

	if got, want := s.Scope([]string{"FCIA"}), `project IN ("FCIA")`; got != want {
		t.Errorf("got %s, want %s -- an unversioned project must not be gated", got, want)
	}
	if got := s.HeldScope([]string{"FCIA"}); got != "" {
		t.Errorf("an unversioned project can hold nothing back, got %s", got)
	}
	if s.Enforced([]string{"FCIA"}) {
		t.Error("a project with no open milestone must not be enforced")
	}
}

// A project whose every version is CLOSED is unenforced too.
//
// Between releases there is no open milestone to attach to, so enforcing
// would halt the project until somebody creates one -- a gate on the wrong
// thing. This is the same detection, not a separate rule.
func TestAProjectWhoseVersionsAreAllClosedIsUnaffected(t *testing.T) {
	open := OpenVersions([]Version{
		{Name: "v0.8.0", Released: true},
		{Name: "v0.7.0", Archived: true},
	})
	if len(open) != 0 {
		t.Fatalf("a released or archived version is not claimable: %v", open)
	}
	s := Schedules{"OR": open}
	if got, want := s.Scope([]string{"OR"}), `project IN ("OR")`; got != want {
		t.Errorf("got %s, want %s", got, want)
	}
}

// Mixed scope: one enforced project alongside one that is not.
//
// The OR must be bracketed or JQL's precedence silently rewrites the whole
// criterion -- AND binds tighter, so an unbracketed alternation would become
// `orProject OR (fciaProject AND labels = ... AND statusCategory != ...)` and
// claim every OR ticket in the project regardless of label or status.
func TestAMixedScopeBracketsTheAlternation(t *testing.T) {
	s := Schedules{"OR": {"v0.8.6"}, "FCIA": nil}

	got := s.Scope([]string{"OR", "FCIA"})

	if !strings.HasPrefix(got, "(") || !strings.HasSuffix(got, ")") {
		t.Errorf("the alternation is not bracketed, so AND will bind tighter: %s", got)
	}
	if !strings.Contains(got, `(project = "OR" AND fixVersion IN ("v0.8.6"))`) {
		t.Errorf("the enforced project lost its milestone requirement: %s", got)
	}
	if !strings.Contains(got, `(project = "FCIA")`) {
		t.Errorf("the unenforced project was dropped from scope entirely: %s", got)
	}
}

// The held query must find a ticket with NO fixVersion.
//
// `fixVersion NOT IN (...)` alone excludes rows where the field is empty, so
// the very case this ticket is about would fall out of both queries and be
// reported by neither -- a ticket that silently never runs.
func TestTheHeldQueryFindsTicketsWithNoFixVersionAtAll(t *testing.T) {
	got := Schedules{"OR": {"v0.8.6"}}.HeldScope([]string{"OR"})

	if !strings.Contains(got, "fixVersion IS EMPTY") {
		t.Errorf("a ticket with no fixVersion is in neither query: %s", got)
	}
	if !strings.Contains(got, `fixVersion NOT IN ("v0.8.6")`) {
		t.Errorf("a ticket on a closed release is in neither query: %s", got)
	}
	if !strings.Contains(got, `project = "OR"`) {
		t.Errorf("lost the project scope: %s", got)
	}
}

// The reason, per ticket, in the words the operator sees. A held ticket that
// is never explained is how somebody spends an afternoon wondering whether
// the watcher is broken.
func TestHoldReasonNamesWhichOfTheTwoFailuresItIs(t *testing.T) {
	s := Schedules{"OR": {"v0.8.6"}}

	unscheduled := s.HoldReason(Issue{Key: "OR-221"}, "ORION")
	if !strings.Contains(unscheduled, "not attached to a release") {
		t.Errorf("a ticket with no fixVersion is unexplained: %q", unscheduled)
	}
	if !strings.Contains(unscheduled, "ORION") {
		t.Errorf("the reason does not name the label that made it a candidate: %q", unscheduled)
	}

	// A closed release is refused too, and says so differently: it is
	// scheduled for a train that has left, which is the OR-105 failure and
	// not the same problem as having no version.
	closed := s.HoldReason(Issue{Key: "OR-105", FixVersions: []string{"v0.8.0"}}, "ORION")
	if !strings.Contains(closed, "already closed") {
		t.Errorf("a ticket on a shipped release is unexplained: %q", closed)
	}
	if closed == unscheduled {
		t.Error("the two failures must not read as the same one")
	}

	if got := s.HoldReason(Issue{Key: "OR-1", FixVersions: []string{"v0.8.6"}}, "ORION"); got != "" {
		t.Errorf("a scheduled ticket must be claimable, got %q", got)
	}
	// A project that does not use versions holds nothing back.
	if got := s.HoldReason(Issue{Key: "FCIA-7"}, "ORION"); got != "" {
		t.Errorf("an unversioned project must hold nothing back, got %q", got)
	}
}

// The rule is applied per PROJECT, and an issue carries its project in its
// key. A lookup that missed on case would report a project as unenforced and
// claim exactly the work this refuses.
func TestProjectOfIsCaseInsensitiveAndRejectsNonKeys(t *testing.T) {
	for in, want := range map[string]string{
		"or-221":  "OR",
		"OR-221":  "OR",
		"FCIA-7":  "FCIA",
		"ORION":   "",
		"":        "",
		"-12":     "",
		"OR-1-2 ": "OR-1",
	} {
		if got := ProjectOf(in); got != want {
			t.Errorf("ProjectOf(%q) = %q, want %q", in, got, want)
		}
	}
	if got := (Schedules{"OR": {"v1"}}).Claimable("or"); len(got) != 1 {
		t.Errorf("a lower-case project key missed its own schedule: %v", got)
	}
}

// Not knowing whether a ticket is scheduled must not resolve to "claim it".
//
// Degrading to unenforced on a failed version read would mean a network blip
// re-opens the gate -- the watcher already retries a transient tracker error
// on the next tick, which is the correct response to not knowing.
func TestLoadSchedulesFailsRatherThanDegradingToUnenforced(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(500)
		_, _ = w.Write([]byte(`{"error":"boom"}`))
	}))
	defer srv.Close()

	if _, err := LoadSchedules(&Jira{BaseURL: srv.URL}, []string{"OR"}); err == nil {
		t.Fatal("a failed version read must be an error, not an open gate")
	}
}

// The versions actually read are the project's own, and only the open ones
// reach the query.
func TestLoadSchedulesKeepsOnlyOpenVersions(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "/project/OR/versions") {
			_ = json.NewEncoder(w).Encode([]map[string]any{})
			return
		}
		_ = json.NewEncoder(w).Encode([]map[string]any{
			{"id": "1", "name": "v0.8.0", "released": true},
			{"id": "2", "name": "v0.8.6"},
			{"id": "3", "name": "v0.7.0", "archived": true},
		})
	}))
	defer srv.Close()

	s, err := LoadSchedules(&Jira{BaseURL: srv.URL}, []string{"or", "OR", "FCIA"})
	if err != nil {
		t.Fatal(err)
	}
	if got := s.Claimable("OR"); len(got) != 1 || got[0] != "v0.8.6" {
		t.Errorf("claimable milestones = %v, want only the open one", got)
	}
	if got := s.Claimable("FCIA"); len(got) != 0 {
		t.Errorf("FCIA defines no versions, got %v", got)
	}
}
