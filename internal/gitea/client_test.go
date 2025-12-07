package gitea

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"gitea.swiftstrike.ai/swiftstrike/tugboat/internal/remote"
)

func TestNewClient(t *testing.T) {
	client := NewClient("https://gitea.example.com", "test-token")

	if client.baseURL != "https://gitea.example.com" {
		t.Errorf("baseURL = %q, want %q", client.baseURL, "https://gitea.example.com")
	}

	if client.token != "test-token" {
		t.Errorf("token = %q, want %q", client.token, "test-token")
	}
}

func TestNewClientTrimsTrailingSlash(t *testing.T) {
	client := NewClient("https://gitea.example.com/", "test-token")

	if client.baseURL != "https://gitea.example.com" {
		t.Errorf("baseURL = %q, want trailing slash trimmed", client.baseURL)
	}
}

func TestListOrgRepos(t *testing.T) {
	repos := []Repository{
		{ID: 1, Name: "repo1", CloneURL: "https://gitea.example.com/org/repo1.git"},
		{ID: 2, Name: "repo2", CloneURL: "https://gitea.example.com/org/repo2.git"},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify auth header
		if auth := r.Header.Get("Authorization"); auth != "token test-token" {
			t.Errorf("Authorization header = %q, want %q", auth, "token test-token")
		}

		// Return repos on first page, empty on second
		if r.URL.Query().Get("page") == "1" {
			json.NewEncoder(w).Encode(repos)
		} else {
			json.NewEncoder(w).Encode([]Repository{})
		}
	}))
	defer server.Close()

	client := NewClient(server.URL, "test-token")
	result, err := client.ListOrgRepos("testorg")
	if err != nil {
		t.Fatalf("ListOrgRepos() error = %v", err)
	}

	if len(result) != 2 {
		t.Errorf("len(result) = %d, want 2", len(result))
	}

	if result[0].Name != "repo1" {
		t.Errorf("result[0].Name = %q, want %q", result[0].Name, "repo1")
	}
}

func TestListOrgReposAPIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte("Unauthorized"))
	}))
	defer server.Close()

	client := NewClient(server.URL, "bad-token")
	_, err := client.ListOrgRepos("testorg")
	if err == nil {
		t.Error("ListOrgRepos() should return error for unauthorized")
	}
}

func TestGetRepo(t *testing.T) {
	repo := Repository{
		ID:            1,
		Name:          "testrepo",
		FullName:      "org/testrepo",
		CloneURL:      "https://gitea.example.com/org/testrepo.git",
		SSHURL:        "git@gitea.example.com:org/testrepo.git",
		DefaultBranch: "main",
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(repo)
	}))
	defer server.Close()

	client := NewClient(server.URL, "test-token")
	result, err := client.GetRepo("org", "testrepo")
	if err != nil {
		t.Fatalf("GetRepo() error = %v", err)
	}

	if result.Name != "testrepo" {
		t.Errorf("result.Name = %q, want %q", result.Name, "testrepo")
	}

	if result.DefaultBranch != "main" {
		t.Errorf("result.DefaultBranch = %q, want %q", result.DefaultBranch, "main")
	}
}

func TestGetRepoNotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	client := NewClient(server.URL, "test-token")
	result, err := client.GetRepo("org", "nonexistent")
	if err != nil {
		t.Fatalf("GetRepo() error = %v", err)
	}

	if result != nil {
		t.Error("GetRepo() should return nil for not found")
	}
}

func TestRepositoryGetCloneURL(t *testing.T) {
	repo := remote.Repository{
		CloneURL: "https://gitea.example.com/org/repo.git",
		SSHURL:   "git@gitea.example.com:org/repo.git",
	}

	tests := []struct {
		name      string
		preferSSH bool
		expected  string
	}{
		{
			name:      "prefer SSH",
			preferSSH: true,
			expected:  "git@gitea.example.com:org/repo.git",
		},
		{
			name:      "prefer HTTPS",
			preferSSH: false,
			expected:  "https://gitea.example.com/org/repo.git",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := repo.GetCloneURL(tt.preferSSH)
			if result != tt.expected {
				t.Errorf("GetCloneURL(%v) = %q, want %q", tt.preferSSH, result, tt.expected)
			}
		})
	}
}

func TestRepositoryGetCloneURLNoSSH(t *testing.T) {
	repo := remote.Repository{
		CloneURL: "https://gitea.example.com/org/repo.git",
		SSHURL:   "",
	}

	// Should fall back to HTTPS when SSH URL is empty
	result := repo.GetCloneURL(true)
	if result != "https://gitea.example.com/org/repo.git" {
		t.Errorf("GetCloneURL(true) with no SSH = %q, want HTTPS fallback", result)
	}
}
