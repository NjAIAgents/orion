package tracker

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// versionSrv is a Jira stub holding one project's versions in memory, so the
// idempotence path can be exercised as a real sequence of calls rather than
// asserted against a canned response.
func versionSrv(t *testing.T, existing []Version) (*Jira, *[]map[string]any) {
	t.Helper()
	var posted []map[string]any
	vs := append([]Version(nil), existing...)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		// Only project OR exists, so a lookup for anything else 404s the way
		// Jira would. Without this the stub answers for every key and the
		// missing-project path is never actually exercised.
		case r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/versions"):
			if !strings.Contains(r.URL.Path, "/project/OR/") {
				w.WriteHeader(404)
				return
			}
			_ = json.NewEncoder(w).Encode(vs)

		case r.Method == "GET" && strings.Contains(r.URL.Path, "/project/"):
			if !strings.HasSuffix(r.URL.Path, "/project/OR") {
				w.WriteHeader(404)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]string{"id": "10002"})

		case r.Method == "POST" && r.URL.Path == "/rest/api/3/version":
			b, _ := io.ReadAll(r.Body)
			var body map[string]any
			_ = json.Unmarshal(b, &body)
			posted = append(posted, body)
			name, _ := body["name"].(string)
			for _, v := range vs {
				if v.Name == name {
					// What Jira actually does on a duplicate name.
					w.WriteHeader(400)
					_, _ = w.Write([]byte(`{"errors":{"name":"A version with this name already exists."}}`))
					return
				}
			}
			created := Version{ID: "20001", Name: name}
			vs = append(vs, created)
			_ = json.NewEncoder(w).Encode(created)

		case r.Method == "PUT" && strings.Contains(r.URL.Path, "/rest/api/3/version/"):
			b, _ := io.ReadAll(r.Body)
			var body map[string]any
			_ = json.Unmarshal(b, &body)
			posted = append(posted, body)
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "20001", "released": true})

		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(srv.Close)
	return &Jira{BaseURL: srv.URL, Email: "e", Token: "t", client: srv.Client()}, &posted
}

func TestCreateVersionCreatesWhenAbsent(t *testing.T) {
	j, posted := versionSrv(t, nil)

	v, created, err := j.CreateVersion("OR", "v0.9.0", "")
	if err != nil {
		t.Fatal(err)
	}
	if !created {
		t.Error("reported the version as pre-existing when the project had none")
	}
	if v.Name != "v0.9.0" || v.ID == "" {
		t.Errorf("created version came back without a usable name and id: %+v", v)
	}
	// projectId must be a NUMBER. Sending the key, or the id as a string, is
	// a 400 whose message does not say which field was wrong.
	if got, ok := (*posted)[0]["projectId"].(float64); !ok || int(got) != 10002 {
		t.Errorf("projectId was not sent as a number: %#v", (*posted)[0]["projectId"])
	}
	if (*posted)[0]["released"] != false {
		t.Error("a new milestone was created already released")
	}
}

// The property the whole command rests on: safe to re-run. A promotion that
// retries after a partial failure must not need a human to check what
// already happened first.
func TestCreateVersionIsIdempotent(t *testing.T) {
	j, _ := versionSrv(t, []Version{{ID: "10", Name: "v0.9.0"}})

	v, created, err := j.CreateVersion("OR", "v0.9.0", "")
	if err != nil {
		t.Fatalf("re-creating an existing version errored, so the command cannot be "+
			"called twice and is useless to automation: %v", err)
	}
	if created {
		t.Error("claimed to create a version that already existed")
	}
	if v.ID != "10" {
		t.Errorf("returned a different version than the existing one: %+v", v)
	}
}

// Exact match, not prefix. v0.8 and v0.8.1 are different milestones, and
// resolving one to the other files work against the wrong release.
func TestFindVersionMatchesExactlyNotByPrefix(t *testing.T) {
	j, _ := versionSrv(t, []Version{{ID: "10", Name: "v0.8.1"}})

	if _, found, err := j.FindVersion("OR", "v0.8"); err != nil {
		t.Fatal(err)
	} else if found {
		t.Error("v0.8 matched v0.8.1, so a ticket would be filed against the wrong milestone")
	}
}

func TestMarkReleasedSendsReleasedAndADate(t *testing.T) {
	j, posted := versionSrv(t, []Version{{ID: "20001", Name: "v0.9.0"}})

	if err := j.MarkReleased("20001"); err != nil {
		t.Fatal(err)
	}
	last := (*posted)[len(*posted)-1]
	if last["released"] != true {
		t.Error("did not set released")
	}
	if d, _ := last["releaseDate"].(string); len(d) != len("2006-01-02") {
		t.Errorf("releaseDate is not a plain date: %q", d)
	}
}

func TestListVersionsNamesAMissingProject(t *testing.T) {
	j, _ := versionSrv(t, nil)

	_, err := j.ListVersions("NOPE")
	if err == nil {
		t.Fatal("a missing project listed versions without error")
	}
	if !strings.Contains(err.Error(), "NOPE") {
		t.Errorf("error does not name the project that was missing: %v", err)
	}
}
