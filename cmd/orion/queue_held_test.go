package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/orion-sdlc/orion/internal/config"
	"github.com/orion-sdlc/orion/internal/tracker"
)

// versionServer answers the project-versions endpoint and nothing else.
func versionServer(t *testing.T, versions []map[string]any) *tracker.Jira {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "/project/OR/versions") {
			w.WriteHeader(404)
			return
		}
		_ = json.NewEncoder(w).Encode(versions)
	}))
	t.Cleanup(srv.Close)
	return &tracker.Jira{BaseURL: srv.URL}
}

func queueCfg() config.Config {
	cfg := config.Config{}
	cfg.Tracker.ProjectKey = "OR"
	cfg.Tracker.QueueLabel = "ORION"
	return cfg
}

// `orion queue` must SHOW a ticket the watcher will not claim, with the
// reason -- not quietly leave it out.
//
// OR-221. The watcher's own query now excludes an unscheduled ticket, and if
// this command filtered the same way the ticket would vanish from the one
// view whose entire job is to say what the watcher would do and why. A ticket
// that silently never runs is how somebody spends an afternoon wondering
// whether the watcher is broken.
func TestQueueSaysWhyAnUnscheduledTicketWillNotBeClaimed(t *testing.T) {
	j := versionServer(t, []map[string]any{
		{"id": "1", "name": "v0.8.6"},
		{"id": "2", "name": "v0.8.0", "released": true},
	})
	var buf bytes.Buffer

	holds := queueHolds(&buf, j, queueCfg(), []tracker.Issue{
		{Key: "OR-300", Labels: []string{"ORION"}},
		{Key: "OR-301", Labels: []string{"ORION"}, FixVersions: []string{"v0.8.6"}},
		{Key: "OR-302", Labels: []string{"ORION"}, FixVersions: []string{"v0.8.0"}},
	})

	if r := holds["OR-300"]; !strings.Contains(r, "not attached to a release") {
		t.Errorf("a labelled ticket with no fixVersion was not explained: %q", r)
	}
	// A version already marked released is refused too, and says so
	// differently: it is scheduled for a train that has left (OR-105).
	if r := holds["OR-302"]; !strings.Contains(r, "already closed") {
		t.Errorf("a ticket on a shipped release was not explained: %q", r)
	}
	if r, ok := holds["OR-301"]; ok {
		t.Errorf("a scheduled ticket must not be held: %q", r)
	}
}

// A ticket already claimed is not "will not be claimed". Telling its reader
// otherwise would be false, and the label state is the only thing that says
// which of the two it is.
func TestQueueDoesNotHoldBackTicketsAlreadyInFlight(t *testing.T) {
	j := versionServer(t, []map[string]any{{"id": "1", "name": "v0.8.6"}})
	var buf bytes.Buffer

	holds := queueHolds(&buf, j, queueCfg(), []tracker.Issue{
		{Key: "OR-310", Labels: []string{"ORION", "orion-working"}},
		{Key: "OR-311", Labels: []string{"ORION", "orion-ci-wait"}},
		{Key: "OR-312", Labels: []string{"ORION", "orion-failed"}},
	})

	if len(holds) != 0 {
		t.Errorf("a claimed ticket was reported as unclaimable: %v", holds)
	}
}

// A project with no versions at all must be unaffected: nothing held, nothing
// warned about. Orion adopts arbitrary repositories, and FCIA is registered
// alongside OR.
func TestQueueHoldsNothingBackInAProjectWithoutVersions(t *testing.T) {
	j := versionServer(t, []map[string]any{})
	var buf bytes.Buffer

	holds := queueHolds(&buf, j, queueCfg(), []tracker.Issue{
		{Key: "OR-320", Labels: []string{"ORION"}},
	})

	if len(holds) != 0 {
		t.Errorf("an unversioned project gated its own queue: %v", holds)
	}
}

// A version read that fails DEGRADES and says it degraded. This command is
// read-only; turning a 500 into an error would be worse, and staying silent
// would be worse still -- a reader would take an unmarked ticket for a
// claimable one.
func TestQueueSaysWhenItCouldNotReadTheReleases(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(500)
	}))
	defer srv.Close()
	var buf bytes.Buffer

	holds := queueHolds(&buf, &tracker.Jira{BaseURL: srv.URL}, queueCfg(),
		[]tracker.Issue{{Key: "OR-330", Labels: []string{"ORION"}}})

	if len(holds) != 0 {
		t.Errorf("holds invented from a failed read: %v", holds)
	}
	if !strings.Contains(buf.String(), "could not read") {
		t.Errorf("the gap in the report was not disclosed: %s", buf.String())
	}
}

// The LISTING query must not carry the milestone requirement. It is the
// watcher's claim query that gates; this one has to fetch the held tickets in
// order to be able to report them.
func TestTheQueueListingQueryDoesNotFilterOnFixVersion(t *testing.T) {
	if jql := queueJQL(queueCfg()); strings.Contains(jql, "fixVersion") {
		t.Errorf("the listing filters out the very tickets it must report: %s", jql)
	}
}
