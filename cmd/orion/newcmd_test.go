package main

import (
	"strings"
	"testing"

	"github.com/orion-sdlc/orion/internal/adopt"
	"github.com/orion-sdlc/orion/internal/tracker"
)

// fakeTracker records what it was asked to create, so the promise the gate
// made to the human can be compared against what actually happened.
type fakeTracker struct {
	existing map[string]string
	created  []tracker.Binding
	descs    []string
}

func (f *fakeTracker) Name() string { return "fake" }
func (f *fakeTracker) Probe() (tracker.Capability, error) {
	return tracker.Capability{Reachable: true, Authenticated: true, CanCreateProject: true}, nil
}
func (f *fakeTracker) ProjectExists(key string) (bool, string, error) {
	id, ok := f.existing[key]
	return ok, id, nil
}
func (f *fakeTracker) CreateProject(key, name, lead, description string) (tracker.Binding, error) {
	b := tracker.Binding{Provider: "fake", Key: key, Name: name, ProjectID: "id-" + key, Created: true}
	f.created = append(f.created, b)
	f.descs = append(f.descs, description)
	return b, nil
}

// The whole point of routing through adopt.RemotePlan is that the key named
// in the confirmation is the key that gets created. Resolving the collision
// suffix after the confirmation instead would make the gate a formality: the
// human would agree to CSP and get CSP3.
func TestTheConfirmedKeyIsTheKeyCreated(t *testing.T) {
	f := &fakeTracker{existing: map[string]string{"CSP": "1", "CSP2": "2"}}

	plan, err := newProjectPlan(f, "https://x.atlassian.net", "Claim status portal", "claim-status-portal", "")
	if err != nil {
		t.Fatal(err)
	}
	if plan.JiraKey != "CSP3" {
		t.Fatalf("plan key = %q, want the resolved CSP3", plan.JiraKey)
	}
	if plan.JiraExists {
		t.Error("a free key must not be described as existing")
	}

	described := plan.Describe()
	if !strings.Contains(described, "CSP3") {
		t.Fatalf("the gate did not name the key it will create:\n%s", described)
	}
	// This sentence is the reason the gate exists at all.
	if !strings.Contains(described, "cannot be deleted without admin rights") {
		t.Fatalf("the gate did not state the irreversibility:\n%s", described)
	}

	b, err := createProjectFromPlan(f, plan, "claim-status-portal", "lead", "desc")
	if err != nil {
		t.Fatal(err)
	}
	if b.Key != plan.JiraKey {
		t.Fatalf("created %q after describing %q", b.Key, plan.JiraKey)
	}
}

// The elaborated description is the output of the one interactive exchange in
// the system. A creation that dropped it would still report success.
func TestTheElaboratedDescriptionReachesTheCreatedProject(t *testing.T) {
	f := &fakeTracker{existing: map[string]string{}}
	plan, err := newProjectPlan(f, "site", "Claim status portal", "claim-status-portal", "")
	if err != nil {
		t.Fatal(err)
	}
	const desc = "## Who it is for\n\nClaims handlers.\n"
	if _, err := createProjectFromPlan(f, plan, "claim-status-portal", "lead", desc); err != nil {
		t.Fatal(err)
	}
	if len(f.descs) != 1 || f.descs[0] != desc {
		t.Fatalf("created with %q, want the elaborated description", f.descs)
	}
}

// A configured project_key means bind, not create. The plan has to say so
// before the confirmation, and nothing may be created afterwards -- this is
// the path where an accidental create is permanent.
func TestAConfiguredKeyIsBoundAndNothingIsCreated(t *testing.T) {
	f := &fakeTracker{existing: map[string]string{"PLAT": "id-PLAT"}}

	plan, err := newProjectPlan(f, "site", "Anything", "anything", "PLAT")
	if err != nil {
		t.Fatal(err)
	}
	if !plan.JiraExists {
		t.Fatal("an existing key must be described as bound, not created")
	}
	if !strings.Contains(plan.Describe(), "will be bound, not created") {
		t.Fatalf("the gate offered to create a project it will bind:\n%s", plan.Describe())
	}
	if strings.Contains(plan.Describe(), "cannot be deleted") {
		t.Error("binding is reversible; warning about deletion here trains people to skim")
	}

	b, err := createProjectFromPlan(f, plan, "anything", "lead", "desc")
	if err != nil {
		t.Fatal(err)
	}
	if len(f.created) != 0 {
		t.Fatalf("bound path created %v", f.created)
	}
	if b.Key != "PLAT" || b.Created {
		t.Fatalf("binding = %+v, want PLAT bound rather than created", b)
	}
}

// A project_key naming a project that is not there is a config error, and it
// has to surface before anything is created rather than as a silent create
// under a key nobody chose.
func TestAMissingConfiguredKeyIsAnErrorNotACreate(t *testing.T) {
	f := &fakeTracker{existing: map[string]string{}}
	if _, err := newProjectPlan(f, "site", "Anything", "anything", "GONE"); err == nil {
		t.Fatal("expected an error for a project_key that does not exist")
	}
	if len(f.created) != 0 {
		t.Fatalf("created %v while resolving a bad key", f.created)
	}
}

// Nothing() drives whether the confirmation is asked at all, so a plan that
// would create a project must never report itself as a no-op.
func TestAPlanThatCreatesIsNotNothing(t *testing.T) {
	f := &fakeTracker{existing: map[string]string{}}
	plan, err := newProjectPlan(f, "site", "Claim status portal", "claim-status-portal", "")
	if err != nil {
		t.Fatal(err)
	}
	if (adopt.RemotePlan)(plan).Nothing() {
		t.Fatal("a plan that creates a Jira project reported Nothing(), so the gate would not ask")
	}
}
