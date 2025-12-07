package config

import (
	"testing"
)

func TestReadV1_ValidConfig(t *testing.T) {
	data := []byte(`{
		"gitea_url": "https://gitea.example.com",
		"gitea_token": "test-token",
		"organizations": [
			{"name": "org1", "path": "/home/user/org1"},
			{"name": "org2", "path": "/home/user/org2"}
		]
	}`)

	cfg, err := ReadV1(data)
	if err != nil {
		t.Fatalf("ReadV1() error = %v", err)
	}

	// Should be migrated to V2 format
	if len(cfg.Providers) != 1 {
		t.Errorf("expected 1 provider, got %d", len(cfg.Providers))
	}

	gitea, ok := cfg.Providers["gitea"]
	if !ok {
		t.Fatal("expected 'gitea' provider")
	}
	if gitea.Type != "gitea" {
		t.Errorf("provider type = %q, want %q", gitea.Type, "gitea")
	}
	if gitea.APIURL != "https://gitea.example.com" {
		t.Errorf("provider api_url = %q, want %q", gitea.APIURL, "https://gitea.example.com")
	}
	if gitea.Token != "test-token" {
		t.Errorf("provider token = %q, want %q", gitea.Token, "test-token")
	}

	if len(cfg.Targets) != 2 {
		t.Errorf("expected 2 targets, got %d", len(cfg.Targets))
	}

	// Check first target
	if cfg.Targets[0].Provider != "gitea" {
		t.Errorf("target[0].Provider = %q, want %q", cfg.Targets[0].Provider, "gitea")
	}
	if cfg.Targets[0].Org != "org1" {
		t.Errorf("target[0].Org = %q, want %q", cfg.Targets[0].Org, "org1")
	}
	if cfg.Targets[0].Path != "/home/user/org1" {
		t.Errorf("target[0].Path = %q, want %q", cfg.Targets[0].Path, "/home/user/org1")
	}
	if cfg.Targets[0].Name != "org1" {
		t.Errorf("target[0].Name = %q, want %q", cfg.Targets[0].Name, "org1")
	}
}

func TestReadV1_MissingGiteaURL(t *testing.T) {
	data := []byte(`{
		"gitea_token": "test-token",
		"organizations": [{"name": "org1", "path": "/path"}]
	}`)

	_, err := ReadV1(data)
	if err == nil {
		t.Error("ReadV1() should return error for missing gitea_url")
	}
}

func TestReadV1_MissingGiteaToken(t *testing.T) {
	data := []byte(`{
		"gitea_url": "https://gitea.example.com",
		"organizations": [{"name": "org1", "path": "/path"}]
	}`)

	_, err := ReadV1(data)
	if err == nil {
		t.Error("ReadV1() should return error for missing gitea_token")
	}
}

func TestReadV1_EmptyOrganizations(t *testing.T) {
	data := []byte(`{
		"gitea_url": "https://gitea.example.com",
		"gitea_token": "test-token",
		"organizations": []
	}`)

	_, err := ReadV1(data)
	if err == nil {
		t.Error("ReadV1() should return error for empty organizations")
	}
}

func TestReadV1_InvalidJSON(t *testing.T) {
	data := []byte(`{invalid json}`)

	_, err := ReadV1(data)
	if err == nil {
		t.Error("ReadV1() should return error for invalid JSON")
	}
}

func TestReadV1_ExpandsTildePath(t *testing.T) {
	data := []byte(`{
		"gitea_url": "https://gitea.example.com",
		"gitea_token": "test-token",
		"organizations": [
			{"name": "org1", "path": "~/myorg"}
		]
	}`)

	cfg, err := ReadV1(data)
	if err != nil {
		t.Fatalf("ReadV1() error = %v", err)
	}

	// Path should be expanded (not start with ~/)
	if cfg.Targets[0].Path == "~/myorg" {
		t.Error("expected path to be expanded, still has ~/")
	}
}

func TestReadV1_TrimsTrailingSlashFromURL(t *testing.T) {
	data := []byte(`{
		"gitea_url": "https://gitea.example.com/",
		"gitea_token": "test-token",
		"organizations": [{"name": "org1", "path": "/path"}]
	}`)

	cfg, err := ReadV1(data)
	if err != nil {
		t.Fatalf("ReadV1() error = %v", err)
	}

	if cfg.Providers["gitea"].APIURL != "https://gitea.example.com" {
		t.Errorf("expected trailing slash to be trimmed, got %q", cfg.Providers["gitea"].APIURL)
	}
}
