package config

import (
	"testing"
)

func TestReadV2_ValidGiteaConfig(t *testing.T) {
	data := []byte(`{
		"providers": {
			"gitea": {
				"type": "gitea",
				"api_url": "https://gitea.example.com",
				"token": "test-token"
			}
		},
		"targets": [
			{"provider": "gitea", "org": "myorg", "path": "/home/user/myorg", "name": "myorg"}
		]
	}`)

	cfg, err := ReadV2(data)
	if err != nil {
		t.Fatalf("ReadV2() error = %v", err)
	}

	if len(cfg.Providers) != 1 {
		t.Errorf("expected 1 provider, got %d", len(cfg.Providers))
	}

	gitea := cfg.Providers["gitea"]
	if gitea.Type != "gitea" {
		t.Errorf("provider type = %q, want %q", gitea.Type, "gitea")
	}
	if gitea.APIURL != "https://gitea.example.com" {
		t.Errorf("provider api_url = %q, want %q", gitea.APIURL, "https://gitea.example.com")
	}

	if len(cfg.Targets) != 1 {
		t.Errorf("expected 1 target, got %d", len(cfg.Targets))
	}
}

func TestReadV2_ValidGitHubConfig(t *testing.T) {
	data := []byte(`{
		"providers": {
			"github": {
				"type": "github",
				"token": "ghp_test"
			}
		},
		"targets": [
			{"provider": "github", "org": "myorg", "repo": "myrepo", "path": "/home/user/myrepo"}
		]
	}`)

	cfg, err := ReadV2(data)
	if err != nil {
		t.Fatalf("ReadV2() error = %v", err)
	}

	github := cfg.Providers["github"]
	if github.Type != "github" {
		t.Errorf("provider type = %q, want %q", github.Type, "github")
	}
	// GitHub should default api_url to api.github.com
	if github.APIURL != "https://api.github.com" {
		t.Errorf("github api_url = %q, want default %q", github.APIURL, "https://api.github.com")
	}
}

func TestReadV2_MultipleProviders(t *testing.T) {
	data := []byte(`{
		"providers": {
			"gitea": {"type": "gitea", "api_url": "https://gitea.example.com", "token": "gitea-token"},
			"github": {"type": "github", "token": "ghp_token"}
		},
		"targets": [
			{"provider": "gitea", "org": "org1", "path": "/path/org1"},
			{"provider": "github", "org": "org2", "repo": "repo2", "path": "/path/repo2"}
		]
	}`)

	cfg, err := ReadV2(data)
	if err != nil {
		t.Fatalf("ReadV2() error = %v", err)
	}

	if len(cfg.Providers) != 2 {
		t.Errorf("expected 2 providers, got %d", len(cfg.Providers))
	}
	if len(cfg.Targets) != 2 {
		t.Errorf("expected 2 targets, got %d", len(cfg.Targets))
	}
}

func TestReadV2_WithWorkers(t *testing.T) {
	data := []byte(`{
		"workers": 16,
		"providers": {
			"gitea": {"type": "gitea", "api_url": "https://gitea.example.com", "token": "token"}
		},
		"targets": [
			{"provider": "gitea", "org": "myorg", "path": "/path"}
		]
	}`)

	cfg, err := ReadV2(data)
	if err != nil {
		t.Fatalf("ReadV2() error = %v", err)
	}

	if cfg.Workers != 16 {
		t.Errorf("workers = %d, want 16", cfg.Workers)
	}
}

func TestReadV2_MissingProviders(t *testing.T) {
	data := []byte(`{
		"targets": [{"provider": "gitea", "org": "myorg", "path": "/path"}]
	}`)

	_, err := ReadV2(data)
	if err == nil {
		t.Error("ReadV2() should return error for missing providers")
	}
}

func TestReadV2_MissingTargets(t *testing.T) {
	data := []byte(`{
		"providers": {
			"gitea": {"type": "gitea", "api_url": "https://gitea.example.com", "token": "token"}
		}
	}`)

	_, err := ReadV2(data)
	if err == nil {
		t.Error("ReadV2() should return error for missing targets")
	}
}

func TestReadV2_UnknownProviderType(t *testing.T) {
	data := []byte(`{
		"providers": {
			"gitlab": {"type": "gitlab", "api_url": "https://gitlab.com", "token": "token"}
		},
		"targets": [{"provider": "gitlab", "org": "myorg", "path": "/path"}]
	}`)

	_, err := ReadV2(data)
	if err == nil {
		t.Error("ReadV2() should return error for unknown provider type")
	}
}

func TestReadV2_MissingToken(t *testing.T) {
	data := []byte(`{
		"providers": {
			"gitea": {"type": "gitea", "api_url": "https://gitea.example.com"}
		},
		"targets": [{"provider": "gitea", "org": "myorg", "path": "/path"}]
	}`)

	_, err := ReadV2(data)
	if err == nil {
		t.Error("ReadV2() should return error for missing token")
	}
}

func TestReadV2_GiteaMissingAPIURL(t *testing.T) {
	data := []byte(`{
		"providers": {
			"gitea": {"type": "gitea", "token": "token"}
		},
		"targets": [{"provider": "gitea", "org": "myorg", "path": "/path"}]
	}`)

	_, err := ReadV2(data)
	if err == nil {
		t.Error("ReadV2() should return error for gitea missing api_url")
	}
}

func TestReadV2_TargetReferencesUnknownProvider(t *testing.T) {
	data := []byte(`{
		"providers": {
			"gitea": {"type": "gitea", "api_url": "https://gitea.example.com", "token": "token"}
		},
		"targets": [{"provider": "github", "org": "myorg", "path": "/path"}]
	}`)

	_, err := ReadV2(data)
	if err == nil {
		t.Error("ReadV2() should return error for target referencing unknown provider")
	}
}

func TestReadV2_DuplicateTargetNames(t *testing.T) {
	data := []byte(`{
		"providers": {
			"gitea": {"type": "gitea", "api_url": "https://gitea.example.com", "token": "token"}
		},
		"targets": [
			{"provider": "gitea", "org": "org1", "path": "/path1", "name": "myname"},
			{"provider": "gitea", "org": "org2", "path": "/path2", "name": "myname"}
		]
	}`)

	_, err := ReadV2(data)
	if err == nil {
		t.Error("ReadV2() should return error for duplicate target names")
	}
}

func TestReadV2_DefaultsTargetName(t *testing.T) {
	data := []byte(`{
		"providers": {
			"gitea": {"type": "gitea", "api_url": "https://gitea.example.com", "token": "token"}
		},
		"targets": [
			{"provider": "gitea", "org": "myorg", "path": "/path"}
		]
	}`)

	cfg, err := ReadV2(data)
	if err != nil {
		t.Fatalf("ReadV2() error = %v", err)
	}

	// Name should default to org name
	if cfg.Targets[0].Name != "myorg" {
		t.Errorf("target name = %q, want default %q", cfg.Targets[0].Name, "myorg")
	}
}

func TestReadV2_DefaultsCloneProtocol(t *testing.T) {
	data := []byte(`{
		"providers": {
			"gitea": {"type": "gitea", "api_url": "https://gitea.example.com", "token": "token"}
		},
		"targets": [{"provider": "gitea", "org": "myorg", "path": "/path"}]
	}`)

	cfg, err := ReadV2(data)
	if err != nil {
		t.Fatalf("ReadV2() error = %v", err)
	}

	if cfg.Providers["gitea"].Options.Clone.Protocol != "https" {
		t.Errorf("clone protocol = %q, want default %q", cfg.Providers["gitea"].Options.Clone.Protocol, "https")
	}
}

func TestReadV2_InvalidJSON(t *testing.T) {
	data := []byte(`{invalid}`)

	_, err := ReadV2(data)
	if err == nil {
		t.Error("ReadV2() should return error for invalid JSON")
	}
}
