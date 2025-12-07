package config

import (
	"bytes"
	"encoding/json"
	"testing"
)

// Test that V1 config is detected as deprecated
func TestLoadResult_V1IsDeprecated(t *testing.T) {
	v1Data := []byte(`{
		"gitea_url": "https://gitea.example.com",
		"gitea_token": "token",
		"organizations": [{"name": "org1", "path": "/path"}]
	}`)

	result, err := LoadFromBytesWithMetadata(v1Data)
	if err != nil {
		t.Fatalf("LoadFromBytesWithMetadata() error = %v", err)
	}

	if !result.IsDeprecated {
		t.Error("V1 config should be marked as deprecated")
	}
	if result.Version != 1 {
		t.Errorf("Version = %d, want 1", result.Version)
	}
}

// Test that V2 config is NOT deprecated
func TestLoadResult_V2NotDeprecated(t *testing.T) {
	v2Data := []byte(`{
		"providers": {"gitea": {"type": "gitea", "api_url": "https://gitea.example.com", "token": "token"}},
		"targets": [{"provider": "gitea", "org": "org1", "path": "/path"}]
	}`)

	result, err := LoadFromBytesWithMetadata(v2Data)
	if err != nil {
		t.Fatalf("LoadFromBytesWithMetadata() error = %v", err)
	}

	if result.IsDeprecated {
		t.Error("V2 config should NOT be marked as deprecated")
	}
	if result.Version != 2 {
		t.Errorf("Version = %d, want 2", result.Version)
	}
}

// Test that deprecation warning is emitted for V1 config
func TestLoadWithWarning_V1EmitsWarning(t *testing.T) {
	v1Data := []byte(`{
		"gitea_url": "https://gitea.example.com",
		"gitea_token": "token",
		"organizations": [{"name": "org1", "path": "/path"}]
	}`)

	var warningBuf bytes.Buffer
	_, err := LoadFromBytesWithWarning(v1Data, &warningBuf)
	if err != nil {
		t.Fatalf("LoadFromBytesWithWarning() error = %v", err)
	}

	warning := warningBuf.String()
	if warning == "" {
		t.Error("Expected deprecation warning for V1 config, got none")
	}
	if !bytes.Contains(warningBuf.Bytes(), []byte("deprecated")) {
		t.Errorf("Warning should contain 'deprecated', got: %s", warning)
	}
	if !bytes.Contains(warningBuf.Bytes(), []byte("migrate")) {
		t.Errorf("Warning should mention 'migrate' command, got: %s", warning)
	}
}

// Test that V2 config does NOT emit warning
func TestLoadWithWarning_V2NoWarning(t *testing.T) {
	v2Data := []byte(`{
		"providers": {"gitea": {"type": "gitea", "api_url": "https://gitea.example.com", "token": "token"}},
		"targets": [{"provider": "gitea", "org": "org1", "path": "/path"}]
	}`)

	var warningBuf bytes.Buffer
	_, err := LoadFromBytesWithWarning(v2Data, &warningBuf)
	if err != nil {
		t.Fatalf("LoadFromBytesWithWarning() error = %v", err)
	}

	if warningBuf.Len() > 0 {
		t.Errorf("V2 config should not emit warning, got: %s", warningBuf.String())
	}
}

// Test migration produces valid V2 JSON that can be re-parsed
func TestMigration_ProducesValidV2(t *testing.T) {
	v1Data := []byte(`{
		"gitea_url": "https://gitea.example.com",
		"gitea_token": "secret-token",
		"organizations": [
			{"name": "org1", "path": "/home/user/org1"},
			{"name": "org2", "path": "/home/user/org2"}
		]
	}`)

	// Load as V1
	cfg, err := ReadV1(v1Data)
	if err != nil {
		t.Fatalf("ReadV1() error = %v", err)
	}

	// Convert to JSON
	v2JSON, err := cfg.ToJSON()
	if err != nil {
		t.Fatalf("ToJSON() error = %v", err)
	}

	// Re-parse as V2
	cfg2, err := ReadV2(v2JSON)
	if err != nil {
		t.Fatalf("ReadV2() error = %v", err)
	}

	// Verify data integrity
	if len(cfg2.Providers) != 1 {
		t.Errorf("Expected 1 provider, got %d", len(cfg2.Providers))
	}
	gitea, ok := cfg2.Providers["gitea"]
	if !ok {
		t.Fatal("Expected 'gitea' provider")
	}
	if gitea.APIURL != "https://gitea.example.com" {
		t.Errorf("api_url = %q, want %q", gitea.APIURL, "https://gitea.example.com")
	}
	if gitea.Token != "secret-token" {
		t.Errorf("token = %q, want %q", gitea.Token, "secret-token")
	}
	if len(cfg2.Targets) != 2 {
		t.Errorf("Expected 2 targets, got %d", len(cfg2.Targets))
	}
}

// Test migration preserves all organization data
func TestMigration_PreservesOrgData(t *testing.T) {
	v1Data := []byte(`{
		"gitea_url": "https://gitea.example.com",
		"gitea_token": "token",
		"organizations": [
			{"name": "alpha", "path": "/path/to/alpha"},
			{"name": "beta", "path": "/path/to/beta"},
			{"name": "gamma", "path": "/path/to/gamma"}
		]
	}`)

	cfg, err := ReadV1(v1Data)
	if err != nil {
		t.Fatalf("ReadV1() error = %v", err)
	}

	if len(cfg.Targets) != 3 {
		t.Fatalf("Expected 3 targets, got %d", len(cfg.Targets))
	}

	// Verify each org was converted to a target
	expected := map[string]string{
		"alpha": "/path/to/alpha",
		"beta":  "/path/to/beta",
		"gamma": "/path/to/gamma",
	}

	for _, target := range cfg.Targets {
		expectedPath, ok := expected[target.Name]
		if !ok {
			t.Errorf("Unexpected target name: %s", target.Name)
			continue
		}
		if target.Path != expectedPath {
			t.Errorf("Target %s path = %q, want %q", target.Name, target.Path, expectedPath)
		}
		if target.Provider != "gitea" {
			t.Errorf("Target %s provider = %q, want 'gitea'", target.Name, target.Provider)
		}
		if target.Org != target.Name {
			t.Errorf("Target %s org = %q, want %q", target.Name, target.Org, target.Name)
		}
	}
}

// Test ToJSON produces valid JSON
func TestToJSON_ProducesValidJSON(t *testing.T) {
	cfg := &Config{
		Workers: 8,
		Providers: map[string]Provider{
			"gitea": {Type: "gitea", APIURL: "https://gitea.example.com", Token: "token"},
		},
		Targets: []Target{
			{Name: "myorg", Provider: "gitea", Org: "myorg", Path: "/path"},
		},
	}

	data, err := cfg.ToJSON()
	if err != nil {
		t.Fatalf("ToJSON() error = %v", err)
	}

	// Should be valid JSON
	var parsed map[string]interface{}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Errorf("ToJSON() produced invalid JSON: %v", err)
	}

	// Should contain expected keys
	if _, ok := parsed["providers"]; !ok {
		t.Error("JSON missing 'providers' key")
	}
	if _, ok := parsed["targets"]; !ok {
		t.Error("JSON missing 'targets' key")
	}
}
