package github

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRepositoryIDsSurviveNameReuse(t *testing.T) {
	archived := map[string]any{"id": 101, "name": "perception-yolo", "full_name": "t1/perception-yolo", "archived": true, "default_branch": "main"}
	replacement := map[string]any{"id": 202, "name": "perception", "full_name": "t1/perception", "archived": false, "default_branch": "main"}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "token test-token" {
			t.Error("missing authentication")
		}
		switch {
		case strings.HasPrefix(r.URL.Path, "/orgs/"):
			_ = json.NewEncoder(w).Encode([]any{archived, replacement})
		case strings.HasSuffix(r.URL.Path, "/perception-yolo"):
			_ = json.NewEncoder(w).Encode(archived)
		default:
			_ = json.NewEncoder(w).Encode(replacement)
		}
	}))
	defer server.Close()
	client := NewClient(server.URL, "test-token")
	repos, err := client.ListOrgRepos("t1")
	if err != nil || len(repos) != 2 || repos[0].ID != 101 || repos[1].ID != 202 {
		t.Fatalf("listing lost identities: %+v, %v", repos, err)
	}
	for name, want := range map[string]int64{"perception-yolo": 101, "perception": 202} {
		r, err := client.GetRepo("t1", name)
		if err != nil || r == nil || r.ID != want {
			t.Fatalf("%s identity: %+v, %v", name, r, err)
		}
	}
}

func TestArchiveDiscoveryIncludesTransferredRepositories(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/user/repos" || r.Header.Get("Authorization") != "token test-token" {
			t.Errorf("unexpected archive request: %s", r.URL)
		}
		json.NewEncoder(w).Encode([]map[string]any{{"id": 33, "name": "perception-yolo", "full_name": "t1-archive/perception-yolo", "archived": true}, {"id": 187, "name": "perception", "full_name": "t1/perception", "archived": false}})
	}))
	defer server.Close()
	repos, err := NewClient(server.URL, "test-token").ListArchivedRepos()
	if err != nil || len(repos) != 1 || repos[0].ID != 33 || repos[0].FullName != "t1-archive/perception-yolo" {
		t.Fatalf("archive identities: %+v, %v", repos, err)
	}
}
