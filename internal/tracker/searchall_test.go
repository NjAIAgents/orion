package tracker

import (
	"strings"
	"testing"
)

// OR-542: SearchAll follows the page token to the last page, where Search
// stops at the first 100.
func TestSearchAllFollowsThePageToken(t *testing.T) {
	var asked []string
	j := fakeJira(t, func(method, path string, _ []byte) (int, string) {
		asked = append(asked, path)
		if strings.Contains(path, "nextPageToken=p2") {
			return 200, `{"isLast":true,"issues":[{"key":"LTA-150","fields":{"summary":"T150 last"}}]}`
		}
		return 200, `{"nextPageToken":"p2","isLast":false,"issues":[{"key":"LTA-1","fields":{"summary":"T001 first"}}]}`
	})
	got, err := j.SearchAll("project = LTA")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Key != "LTA-1" || got[1].Key != "LTA-150" {
		t.Fatalf("got %+v, want both pages", got)
	}
	if len(asked) != 2 {
		t.Errorf("asked %d pages, want 2: %v", len(asked), asked)
	}

	// Search itself is unchanged: one page, no token followed.
	asked = nil
	if _, err := j.Search("project = LTA", 100); err != nil {
		t.Fatal(err)
	}
	if len(asked) != 1 {
		t.Errorf("Search followed the token (%d requests); its cap is deliberate", len(asked))
	}
}

// A server that never says isLast still ends, with an error.
func TestSearchAllIsBounded(t *testing.T) {
	n := 0
	j := fakeJira(t, func(string, string, []byte) (int, string) {
		n++
		return 200, `{"nextPageToken":"again","issues":[]}`
	})
	if _, err := j.SearchAll("project = LTA"); err == nil {
		t.Error("an endless token did not end in an error")
	}
	if n != maxSearchPages {
		t.Errorf("made %d requests, want the bound of %d", n, maxSearchPages)
	}
}
